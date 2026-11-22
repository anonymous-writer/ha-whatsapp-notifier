package main

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeJID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"+31 6 12345678", "31612345678@s.whatsapp.net"},
		{"31612345678@c.us", "31612345678@s.whatsapp.net"},
		{"120363012345@g.us", "120363012345@g.us"},
		{"123456789@lid", "123456789@lid"},
	}
	for _, test := range tests {
		got, err := normalizeJID(test.input)
		if err != nil {
			t.Fatalf("normalizeJID(%q): %v", test.input, err)
		}
		if got.String() != test.want {
			t.Errorf("normalizeJID(%q) = %q, want %q", test.input, got.String(), test.want)
		}
	}
	if _, err := normalizeJID("not-a-chat"); err == nil {
		t.Fatal("expected invalid chat ID to fail")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Now()
	limiter := &rateLimiter{max: 2, window: time.Minute}
	limiter.times = []time.Time{now.Add(-2 * time.Minute)}
	if !limiter.allow() || !limiter.allow() {
		t.Fatal("first two sends should be allowed")
	}
	if limiter.allow() {
		t.Fatal("third send should be rate limited")
	}
}

func TestShouldForwardMatchesAlternateJID(t *testing.T) {
	b := &bridge{forward: map[string]bool{
		"31612345678@s.whatsapp.net": true,
	}}
	if !b.shouldForward([]string{"123456789@lid", "31612345678@c.us"}) {
		t.Fatal("expected alternate phone JID to match the configured forward chat")
	}
	if b.shouldForward([]string{"120363012345@g.us"}) {
		t.Fatal("unexpected match for an unconfigured group")
	}
}

func TestMessageTextCompatibility(t *testing.T) {
	if got := messageText(&waE2E.Message{Conversation: proto.String("plain")}); got != "plain" {
		t.Fatalf("plain message text = %q, want plain", got)
	}
	if got := messageText(&waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("extended")},
	}); got != "extended" {
		t.Fatalf("extended message text = %q, want extended", got)
	}
	if got := messageText(&waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{Caption: proto.String("caption")},
	}); got != "" {
		t.Fatalf("image caption should not be forwarded as text, got %q", got)
	}
}
