package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	waCompanionReg "go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	defaultPort    = "8099"
	maxImageBytes  = 5 * 1024 * 1024
	maxRequestSize = 8 * 1024 * 1024
)

type options struct {
	APIToken     string `json:"api_token"`
	PairingPhone string `json:"pairing_phone"`
	Port         int    `json:"port"`
}

type bridgeStatus struct {
	Status      string `json:"status"`
	PairingCode string `json:"pairing_code"`
	Error       string `json:"error"`
}

type outgoingRequest struct {
	ChatID  string `json:"chat_id"`
	Message string `json:"message"`
	Image   string `json:"image"`
}

type incomingMessage struct {
	Type            string  `json:"type"`
	ID              string  `json:"id"`
	ChatID          string  `json:"chat_id"`
	IsGroup         bool    `json:"is_group"`
	Sender          string  `json:"sender"`
	FromMe          bool    `json:"from_me"`
	Text            string  `json:"text"`
	QuotedMessageID *string `json:"quoted_message_id"`
	QuotedText      *string `json:"quoted_text"`
	QuotedFromMe    bool    `json:"quoted_from_me"`
}

type groupResponse struct {
	ChatID string `json:"chat_id"`
	Name   string `json:"name"`
}

type wsPeer struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type rateLimiter struct {
	mu     sync.Mutex
	times  []time.Time
	max    int
	window time.Duration
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window}
}

func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-r.window)
	first := 0
	for first < len(r.times) && !r.times[first].After(cutoff) {
		first++
	}
	r.times = append(r.times[:0], r.times[first:]...)
	if len(r.times) >= r.max {
		return false
	}
	r.times = append(r.times, now)
	return true
}

type bridge struct {
	token       string
	pairPhone   string
	forwardFile string

	clientMu  sync.RWMutex
	client    *whatsmeow.Client
	container *sqlstore.Container

	statusMu        sync.RWMutex
	status          bridgeStatus
	lastQR          string
	pairingComplete bool

	forwardMu    sync.RWMutex
	forward      map[string]bool
	forwardOrder []string

	peerMu sync.RWMutex
	peers  map[*wsPeer]struct{}

	sentMu    sync.Mutex
	sentIDs   map[string]struct{}
	sentOrder []string

	pairingMu        sync.Mutex
	pairingRequested bool
	limiter          *rateLimiter
	upgrader         websocket.Upgrader
}

func main() {
	// WhatsApp session credentials and the SQLite database are private to this process user.
	syscall.Umask(0o077)
	opts := readOptions()
	token := firstNonEmpty(opts.APIToken, os.Getenv("API_TOKEN"))
	if token == "" {
		log.Fatal("api_token is required; refusing to start without API authentication")
	}

	port := defaultPort
	if opts.Port > 0 {
		port = fmt.Sprint(opts.Port)
	}
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		if _, err := os.Stat("/data"); err == nil {
			dataDir = "/data"
		} else {
			dataDir = "./data"
		}
	}
	authDir := os.Getenv("AUTH_DIR")
	if authDir == "" {
		authDir = filepath.Join(dataDir, "auth")
	}
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		log.Fatalf("create auth directory: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("create data directory: %v", err)
	}

	b := &bridge{
		token:       token,
		pairPhone:   digitsOnly(firstNonEmpty(opts.PairingPhone, os.Getenv("PAIRING_PHONE"))),
		forwardFile: filepath.Join(dataDir, "forward.json"),
		status:      bridgeStatus{Status: "connecting"},
		forward:     make(map[string]bool),
		peers:       make(map[*wsPeer]struct{}),
		sentIDs:     make(map[string]struct{}),
		limiter:     newRateLimiter(20, time.Minute),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true // Server-to-server Home Assistant connections omit Origin.
				}
				parsed, err := url.Parse(origin)
				return err == nil && parsed.Host == r.Host
			},
		},
	}
	b.loadForwardList()

	if err := b.startWhatsApp(authDir); err != nil {
		log.Fatalf("initialize WhatsApp client: %v", err)
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           b.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		log.Printf("Go WhatsApp bridge listening on :%s", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down WhatsApp bridge...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	if client := b.getClient(); client != nil {
		client.Disconnect()
	}
	if b.container != nil {
		_ = b.container.Close()
	}
}

func readOptions() options {
	var opts options
	raw, err := os.ReadFile("/data/options.json")
	if err == nil {
		if err := json.Unmarshal(raw, &opts); err != nil {
			log.Printf("warning: could not parse /data/options.json: %v", err)
		}
	}
	return opts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func digitsOnly(value string) string {
	return regexp.MustCompile(`[^0-9]`).ReplaceAllString(value, "")
}

func (b *bridge) startWhatsApp(authDir string) error {
	dbPath := filepath.Join(authDir, "whatsapp.db")
	dsn := "file:" + dbPath + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
	container, err := sqlstore.New(context.Background(), "sqlite3", dsn, nil)
	if err != nil {
		return fmt.Errorf("open WhatsApp session database: %w", err)
	}
	b.container = container

	device, err := container.GetFirstDevice(context.Background())
	if err != nil {
		return fmt.Errorf("load WhatsApp device: %w", err)
	}
	if device == nil {
		device = container.NewDevice()
	}

	// Customize the companion-device identity shown in WhatsApp's
	// Linked devices screen. WhatsApp controls the actual device icon.
	store.DeviceProps.Os = proto.String("Home Assistant")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()

	client := whatsmeow.NewClient(device, nil)
	b.setClient(client)
	b.registerEventHandler(client)
	if err := client.Connect(); err != nil {
		b.setStatus("error", "", err.Error())
		log.Printf("initial WhatsApp connection failed; retrying in the background: %v", err)
		go b.retryInitialConnection()
	}
	return nil
}

func (b *bridge) setClient(client *whatsmeow.Client) {
	b.clientMu.Lock()
	b.client = client
	b.clientMu.Unlock()
}

func (b *bridge) getClient() *whatsmeow.Client {
	b.clientMu.RLock()
	defer b.clientMu.RUnlock()
	return b.client
}

func (b *bridge) registerEventHandler(client *whatsmeow.Client) {
	client.AddEventHandler(func(evt any) {
		// Ignore late events from an old client after its session was replaced.
		if b.getClient() == client {
			b.handleWhatsAppEvent(evt)
		}
	})
}

func (b *bridge) retryInitialConnection() {
	for {
		time.Sleep(10 * time.Second)
		if b.statusSnapshot().Status == "logged_out" {
			return
		}
		client := b.getClient()
		if client == nil || client.IsConnected() {
			return
		}
		b.setStatus("connecting", "", "")
		if err := client.Connect(); err != nil {
			b.setStatus("error", "", err.Error())
			log.Printf("WhatsApp connection retry failed: %v", err)
			continue
		}
		return
	}
}

func (b *bridge) retryAfterPermanentDisconnect() {
	for {
		time.Sleep(5 * time.Second)
		if b.statusSnapshot().Status == "logged_out" {
			return
		}
		client := b.getClient()
		if client == nil || client.IsConnected() {
			return
		}
		if err := client.Connect(); err != nil {
			b.setStatus("error", "", err.Error())
			log.Printf("manual WhatsApp reconnect failed: %v", err)
			continue
		}
		return
	}
}

func (b *bridge) handleWhatsAppEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Connected:
		b.setStatus("connected", "", "")
		log.Println("WhatsApp connected")
	case *events.Disconnected:
		b.pairingMu.Lock()
		b.pairingRequested = false
		b.pairingMu.Unlock()
		if b.statusSnapshot().Status != "logged_out" {
			b.setStatus("connecting", "", "")
		}
		log.Println("WhatsApp disconnected; whatsmeow will reconnect")
	case *events.PairError:
		message := "WhatsApp pairing failed"
		if evt.Error != nil {
			message = evt.Error.Error()
		}
		b.setStatus("error", "", message)
		log.Printf("WhatsApp pairing failed: %s", message)
	case *events.LoggedOut:
		log.Printf("WhatsApp logged out (on_connect=%t, reason=%s); clearing the old device session", evt.OnConnect, evt.Reason)
		b.setStatus("logged_out", "", "WhatsApp session was unlinked; preparing a fresh pairing session.")
		b.pairingMu.Lock()
		b.pairingRequested = false
		b.pairingMu.Unlock()
		oldClient := b.getClient()
		if oldClient != nil && oldClient.Store != nil {
			if err := oldClient.Store.Delete(context.Background()); err != nil {
				log.Printf("could not delete old WhatsApp device session: %v", err)
			}
		}
		newClient := whatsmeow.NewClient(b.container.NewDevice(), nil)
		b.setClient(newClient)
		b.registerEventHandler(newClient)
		go func() {
			time.Sleep(2 * time.Second)
			if err := newClient.Connect(); err != nil {
				b.setStatus("error", "", err.Error())
				log.Printf("could not start fresh WhatsApp pairing session: %v", err)
				go b.retryInitialConnection()
			}
		}()
	case *events.QR:
		if len(evt.Codes) == 0 {
			return
		}
		b.statusMu.Lock()
		b.pairingComplete = false
		b.statusMu.Unlock()
		b.setStatus("pairing_required", "", "Open /qr with your API token and scan it using WhatsApp > Linked devices.")
		go b.rotateQRCodes(evt.Codes)
		if b.pairPhone != "" {
			b.pairingMu.Lock()
			shouldRequest := !b.pairingRequested
			b.pairingRequested = true
			b.pairingMu.Unlock()
			if shouldRequest {
				client := b.getClient()
				go func() {
					// PairPhone must be called after the QR event has been received.
					code, err := client.PairPhone(context.Background(), b.pairPhone, true, whatsmeow.PairClientChrome, "Home Assistant")
					if err != nil {
						b.setStatus("error", "", err.Error())
						log.Printf("phone pairing failed: %v", err)
						return
					}
					b.setStatus("pairing_required", code, "")
					log.Printf("WhatsApp pairing code: %s", code)
				}()
			}
		}
	case events.PermanentDisconnect:
		message := evt.PermanentDisconnectDescription()
		b.setStatus("error", "", message)
		log.Printf("WhatsApp requested a manual reconnect: %s", message)
		go b.retryAfterPermanentDisconnect()
	case *events.PairSuccess:
		b.statusMu.Lock()
		b.pairingComplete = true
		b.lastQR = ""
		b.statusMu.Unlock()
		b.setStatus("connecting", "", "")
		log.Printf("WhatsApp pairing completed for %s; waiting for authenticated connection", evt.ID)
	case *events.Message:
		b.handleIncoming(evt)
	}
}

func (b *bridge) rotateQRCodes(codes []string) {
	for index, code := range codes {
		b.statusMu.Lock()
		if b.pairingComplete || b.status.Status == "connected" || b.status.Status == "logged_out" {
			b.lastQR = ""
			b.statusMu.Unlock()
			return
		}
		b.lastQR = code
		b.statusMu.Unlock()
		b.broadcast(map[string]any{"type": "qr_available"})
		if index == 0 {
			time.Sleep(60 * time.Second)
		} else {
			time.Sleep(20 * time.Second)
		}
	}
}

func (b *bridge) setStatus(status, pairingCode, message string) {
	b.statusMu.Lock()
	b.status = bridgeStatus{Status: status, PairingCode: pairingCode, Error: message}
	if status == "connected" || status == "logged_out" {
		b.lastQR = ""
		b.pairingComplete = true
	}
	snapshot := b.status
	b.statusMu.Unlock()
	b.broadcast(map[string]any{
		"type":         "status",
		"status":       snapshot.Status,
		"pairing_code": nullableString(snapshot.PairingCode),
		"error":        nullableString(snapshot.Error),
	})
	log.Printf("status %s", status)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (b *bridge) statusSnapshot() bridgeStatus {
	b.statusMu.RLock()
	defer b.statusMu.RUnlock()
	return b.status
}

func (b *bridge) handleIncoming(evt *events.Message) {
	client := b.getClient()
	if evt == nil || evt.Message == nil || evt.Info.Chat.String() == "status@broadcast" {
		return
	}
	text := messageText(evt.Message)
	if text == "" {
		return
	}
	if evt.Info.IsFromMe && b.wasSent(string(evt.Info.ID)) {
		return
	}

	chatIDs := []string{evt.Info.Chat.String()}
	if !evt.Info.RecipientAlt.IsEmpty() {
		chatIDs = append(chatIDs, evt.Info.RecipientAlt.String())
	}
	if !b.shouldForward(chatIDs) {
		return
	}

	ctx := messageContext(evt.Message)
	quotedID := ""
	var quotedText *string
	if ctx != nil {
		quotedID = ctx.GetStanzaID()
		if text := quotedMessageText(ctx.GetQuotedMessage()); text != "" {
			quotedText = &text
		}
	}
	isGroup := strings.HasSuffix(evt.Info.Chat.String(), "@g.us")
	quotedFromMe := b.isQuotedFromMe(client, ctx, isGroup, evt.Info.IsFromMe, quotedID)

	sender := evt.Info.Sender.String()
	if !evt.Info.SenderAlt.IsEmpty() {
		sender = evt.Info.SenderAlt.String()
	} else if evt.Info.Sender.IsEmpty() {
		if !evt.Info.RecipientAlt.IsEmpty() {
			sender = evt.Info.RecipientAlt.String()
		} else {
			sender = evt.Info.Chat.String()
		}
	}
	chatID := evt.Info.Chat.String()
	if !evt.Info.RecipientAlt.IsEmpty() {
		chatID = evt.Info.RecipientAlt.String()
	}

	b.broadcast(incomingMessage{
		Type:            "message",
		ID:              string(evt.Info.ID),
		ChatID:          chatID,
		IsGroup:         isGroup,
		Sender:          sender,
		FromMe:          evt.Info.IsFromMe,
		Text:            text,
		QuotedMessageID: stringPointerOrNil(quotedID),
		QuotedText:      quotedText,
		QuotedFromMe:    quotedFromMe,
	})
	log.Printf("forwarding message from %s (%s)", sender, chatID)
}

func messageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if msg.GetExtendedTextMessage() != nil {
		return msg.GetExtendedTextMessage().GetText()
	}
	return ""
}

func messageContext(msg *waE2E.Message) interface {
	GetStanzaID() string
	GetParticipant() string
	GetQuotedMessage() *waE2E.Message
} {
	if msg == nil || msg.GetExtendedTextMessage() == nil {
		return nil
	}
	return msg.GetExtendedTextMessage().GetContextInfo()
}

func quotedMessageText(msg *waE2E.Message) string {
	return messageText(msg)
}

func (b *bridge) isQuotedFromMe(client *whatsmeow.Client, ctx interface {
	GetStanzaID() string
	GetParticipant() string
	GetQuotedMessage() *waE2E.Message
}, isGroup, fromMe bool, quotedID string) bool {
	if ctx == nil || quotedID == "" {
		return false
	}
	if b.wasSent(quotedID) {
		return true
	}
	participant := ctx.GetParticipant()
	if participant != "" {
		if client == nil || client.Store == nil {
			return false
		}
		participantJID, err := types.ParseJID(participant)
		if err != nil {
			return false
		}
		participantJID = participantJID.ToNonAD()
		if client.Store.ID != nil && participantJID == client.Store.ID.ToNonAD() {
			return true
		}
		if !client.Store.LID.IsEmpty() && participantJID == client.Store.LID.ToNonAD() {
			return true
		}
		return false
	}
	return !isGroup && !fromMe
}

func (b *bridge) addSent(id string) {
	b.sentMu.Lock()
	defer b.sentMu.Unlock()
	if id == "" {
		return
	}
	if _, exists := b.sentIDs[id]; exists {
		return
	}
	b.sentIDs[id] = struct{}{}
	b.sentOrder = append(b.sentOrder, id)
	if len(b.sentOrder) > 500 {
		oldest := b.sentOrder[0]
		b.sentOrder = b.sentOrder[1:]
		delete(b.sentIDs, oldest)
	}
}

func (b *bridge) wasSent(id string) bool {
	b.sentMu.Lock()
	defer b.sentMu.Unlock()
	_, ok := b.sentIDs[id]
	return ok
}

func normalizeJID(raw string) (types.JID, error) {
	value := strings.TrimSpace(raw)
	if strings.HasSuffix(value, "@c.us") {
		value = strings.TrimSuffix(value, "@c.us") + "@s.whatsapp.net"
	}
	if !strings.Contains(value, "@") {
		digits := digitsOnly(value)
		if digits == "" {
			return types.JID{}, fmt.Errorf("invalid chat_id")
		}
		value = digits + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(value)
	if err != nil {
		return types.JID{}, fmt.Errorf("invalid chat_id: %w", err)
	}
	return jid, nil
}

func (b *bridge) shouldForward(ids []string) bool {
	b.forwardMu.RLock()
	defer b.forwardMu.RUnlock()
	for _, raw := range ids {
		jid, err := normalizeJID(raw)
		if err == nil && b.forward[jid.String()] {
			return true
		}
	}
	return false
}

func (b *bridge) loadForwardList() {
	raw, err := os.ReadFile(b.forwardFile)
	if err != nil {
		return
	}
	var chats []string
	if err := json.Unmarshal(raw, &chats); err != nil {
		log.Printf("warning: invalid forward list: %v", err)
		return
	}
	for _, chat := range chats {
		if jid, err := normalizeJID(chat); err == nil && !b.forward[jid.String()] {
			b.forward[jid.String()] = true
			b.forwardOrder = append(b.forwardOrder, jid.String())
		}
	}
}

func (b *bridge) setForwardList(chats []string) error {
	normalized := make([]string, 0, len(chats))
	next := make(map[string]bool)
	for _, chat := range chats {
		chat = strings.TrimSpace(chat)
		if chat == "" {
			continue
		}
		jid, err := normalizeJID(chat)
		if err != nil {
			return err
		}
		if !next[jid.String()] {
			next[jid.String()] = true
			normalized = append(normalized, jid.String())
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	tmp := b.forwardFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.forwardFile); err != nil {
		return err
	}
	b.forwardMu.Lock()
	b.forward = next
	b.forwardOrder = normalized
	b.forwardMu.Unlock()
	log.Printf("forward list set: %s", strings.Join(normalized, ", "))
	return nil
}

func (b *bridge) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", b.handleStatus)
	mux.HandleFunc("/forward", b.handleForward)
	mux.HandleFunc("/qr", b.handleQR)
	mux.HandleFunc("/groups", b.handleGroups)
	mux.HandleFunc("/send", b.handleSend)
	mux.HandleFunc("/ws", b.handleWebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	return b.authMiddleware(mux)
}

func (b *bridge) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+b.token && r.URL.Query().Get("token") != b.token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (b *bridge) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s := b.statusSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       s.Status,
		"pairing_code": nullableString(s.PairingCode),
		"error":        nullableString(s.Error),
	})
}

func (b *bridge) handleForward(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.forwardMu.RLock()
		chats := append([]string(nil), b.forwardOrder...)
		b.forwardMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
	case http.MethodPost:
		var payload struct {
			Chats []string `json:"chats"`
		}
		if err := decodeJSON(w, r, &payload, 64*1024); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if payload.Chats == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chats must be a list"})
			return
		}
		if err := b.setForwardList(payload.Chats); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		b.forwardMu.RLock()
		chats := append([]string(nil), b.forwardOrder...)
		b.forwardMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (b *bridge) handleQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	b.statusMu.RLock()
	rawQR := b.lastQR
	b.statusMu.RUnlock()
	if rawQR == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no QR available (already linked?)"})
		return
	}
	png, err := qrcode.Encode(rawQR, qrcode.Medium, 320)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta http-equiv="refresh" content="15"><meta name="viewport" content="width=device-width,initial-scale=1"><title>WhatsApp pairing</title></head><body style="font-family:sans-serif;text-align:center;padding:2rem"><h2>Link WhatsApp device</h2><img alt="WhatsApp QR code" width="320" height="320" src="%s"><p>WhatsApp → Linked devices → Link a device.</p><p>This page refreshes every 15 seconds.</p></body></html>`, img)
}

func (b *bridge) handleGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	client := b.getClient()
	if client == nil || !client.IsLoggedIn() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not connected"})
		return
	}
	groups, err := client.GetJoinedGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	result := make([]groupResponse, 0, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		result = append(result, groupResponse{ChatID: group.JID.String(), Name: group.Name})
	}
	writeJSON(w, http.StatusOK, result)
}

func (b *bridge) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	client := b.getClient()
	if client == nil || !client.IsLoggedIn() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not connected"})
		return
	}
	var payload outgoingRequest
	if err := decodeJSON(w, r, &payload, maxRequestSize); err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(payload.ChatID) == "" || (payload.Message == "" && payload.Image == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chat_id and message (or image) required"})
		return
	}
	jid, err := normalizeJID(payload.ChatID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !b.limiter.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many messages (max 20 per minute)"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	var message *waE2E.Message
	if payload.Image != "" {
		data, err := base64.StdEncoding.DecodeString(payload.Image)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "image must be valid base64"})
			return
		}
		if len(data) == 0 || len(data) > maxImageBytes {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "image must be between 1 byte and 5 MB"})
			return
		}
		mime := http.DetectContentType(data)
		if mime != "image/png" && mime != "image/jpeg" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only PNG and JPEG images are supported"})
			return
		}
		upload, err := client.Upload(ctx, data, whatsmeow.MediaImage)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "image upload failed: " + err.Error()})
			return
		}
		imageMessage := &waE2E.ImageMessage{
			Caption:       proto.String(payload.Message),
			Mimetype:      proto.String(mime),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
		}
		message = &waE2E.Message{ImageMessage: imageMessage}
	} else {
		message = &waE2E.Message{Conversation: proto.String(payload.Message)}
	}

	messageID := client.GenerateMessageID()
	// Register the ID before sending so an immediate own-message echo is filtered.
	b.addSent(string(messageID))
	sent, err := client.SendMessage(ctx, jid, message, whatsmeow.SendRequestExtra{ID: messageID})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(sent.ID)})
}

func (b *bridge) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	peer := &wsPeer{conn: conn}
	b.peerMu.Lock()
	b.peers[peer] = struct{}{}
	b.peerMu.Unlock()

	s := b.statusSnapshot()
	peer.writeJSON(map[string]any{
		"type":         "status",
		"status":       s.Status,
		"pairing_code": nullableString(s.PairingCode),
		"error":        nullableString(s.Error),
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	b.peerMu.Lock()
	delete(b.peers, peer)
	b.peerMu.Unlock()
	_ = conn.Close()
}

func (p *wsPeer) writeJSON(value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = p.conn.WriteJSON(value)
}

func (b *bridge) broadcast(value any) {
	b.peerMu.RLock()
	peers := make([]*wsPeer, 0, len(b.peers))
	for peer := range b.peers {
		peers = append(peers, peer)
	}
	b.peerMu.RUnlock()
	for _, peer := range peers {
		peer.writeJSON(value)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request must contain a single JSON value")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
