# WhatsApp Notifier

<img src="custom_components/whatsapp_notifier/brand/icon.png" width="128" alt="WhatsApp Notifier icon">

Send WhatsApp messages from Home Assistant to a person or a group, and react to replies, **without an external server or subscription**. Everything runs on your own Home Assistant.

**Why this integration?**

- 👥 **Send to WhatsApp groups**, not just to people, with formatting and images.
- 💬 **Reply in WhatsApp and let Home Assistant act on it.** A reply ("yes", "done") starts an automation, for example stopping a reminder or flipping a switch. You can even tie a reply to one specific message.
- 🔒 **Self-hosted.** No external server, no subscription.
- 🧩 **One device per chat**, with its own notify entity and last-message sensor.

It has two parts:

1. **Bridge** (`whatsapp_bridge/`): a standalone Go service using [whatsmeow](https://github.com/tulir/whatsmeow). It links to your WhatsApp account as a *linked device* (like WhatsApp Web).
2. **Integration** (`custom_components/whatsapp_notifier/`): a status sensor, a test button, a `send_message` service, and **one device per chat** with its own notify entity and "Last message" sensor.

Needs Home Assistant 2025.3 or newer.

> **Disclaimer:** whatsmeow uses the unofficial WhatsApp Web Multi-Device protocol. WhatsApp may change this protocol and use may put the account at risk of restrictions. Use a dedicated number, send low volumes, never spam.

### Examples

Messages sent from Home Assistant with this integration (the texts are Dutch, yours can say anything):

<p>
  <img src="docs/screenshots/afval-herinnering.png" alt="Waste reminder that stops when you reply" width="280">
  <img src="docs/screenshots/wasmachine-klaar.png" alt="Washing machine finished, with a reminder if nobody replies" width="280">
  <img src="docs/screenshots/weerbericht.png" alt="Weather warning" width="420">
</p>

The first two ask for a reply. A reply to the message can stop the reminder, see [Replies](#replies).

### Install

The project has two components: the Home Assistant integration and the Go WhatsApp bridge. Choose the bridge deployment that matches your Home Assistant installation. The bridge exposes the same authenticated HTTP/WebSocket API in every deployment.

#### Home Assistant OS / Supervised (recommended)

1. Add this repository in **Settings → Apps → App store → Repositories**:
   `https://github.com/anonymous-writer/ha-whatsapp-notifier`
2. Install **WhatsApp Bridge** and open its configuration. Set a strong `api_token` (for example, generate one with `openssl rand -hex 32`). `pairing_phone` is optional; leave it empty to use QR pairing.
3. Start the app. Its `/data` directory is persistent and contains the linked-device session. Do not delete it when updating or reinstalling.
4. Open the app's **Web UI** and scan the QR code with WhatsApp → Linked devices → Link a device.
5. Install the **WhatsApp Notifier** integration through HACS (or copy `custom_components/whatsapp_notifier` into your HA config folder), then restart Home Assistant.
6. Add the integration. Use host `whatsapp-bridge`, port `8099`, and the same API token configured in the app.

#### Home Assistant Container

Run the bridge as a separate container on the same Docker network as Home Assistant. Persist `/data` and set `API_TOKEN` (required). For example:

```bash
docker run -d \
  --name whatsapp-bridge \
  --restart unless-stopped \
  --network <your-ha-network> \
  -p 8099:8099 \
  -e API_TOKEN="YOUR_RANDOM_64_CHARACTER_TOKEN" \
  -v whatsapp-bridge-data:/data \
  ghcr.io/anonymous-writer/ha-whatsapp-notifier:latest
```

If both containers share a Docker network, configure the integration with host `whatsapp-bridge` and port `8099`. Otherwise use the bridge host's reachable IP address. Open `http://BRIDGE_HOST:8099/qr?token=YOUR_TOKEN` to pair the device.

#### Home Assistant Core / native Linux

Download the matching Linux binary (ARM64 for a 64-bit Raspberry Pi), create a JSON or YAML config file, and run it as a systemd service. Example `config.yaml`:

```yaml
host: "127.0.0.1"
port: "8099"
api_token: "YOUR_RANDOM_64_CHARACTER_TOKEN"
pairing_phone: ""
data_dir: "/home/homeassistant/.local/share/whatsapp-bridge/data"
auth_dir: "/home/homeassistant/.local/share/whatsapp-bridge/data/auth"
```

Start with `whatsapp-bridge-linux --config /path/to/config.yaml`. Protect the config file (`chmod 600`) and keep `data_dir` and `auth_dir` stable across restarts and upgrades. The session is stored in SQLite under `auth_dir`; do not delete it or run two bridge instances against the same database. Open `http://BRIDGE_HOST:8099/qr?token=YOUR_TOKEN` to pair.

#### Install the integration

In HACS, open **Integrations → Custom repositories**, add this repository URL with category **Integration**, install **WhatsApp Notifier**, and restart Home Assistant. Alternatively, copy `custom_components/whatsapp_notifier` into your Home Assistant `custom_components` directory. Then go to **Settings → Devices & services → Add integration → WhatsApp Notifier**. The host, port and token must point to the bridge; the default host suggestion `whatsapp-bridge` is suitable for the Home Assistant App deployment.

### Finding chat IDs

**A person:** the phone number with country code, digits only, no `+`, no leading `0`.
`06 12345678` in the Netherlands becomes `31612345678`. (`31612345678@c.us` and `31612345678@s.whatsapp.net` also work, so existing automations using that format keep working.)

**A group:** open this in a browser on your network:

```
http://homeassistant.local:8099/groups?token=<api_token>
```

You get a list like `[{"chat_id":"120363012345678901@g.us","name":"Family"}, ...]`. Copy the whole `chat_id`, including `@g.us`. The linked number must be a member of the group. If the group is missing, wait a minute and reload.

### Chats: one device per chat

On the integration page (Settings > Devices & services > WhatsApp Notifier) press **Add chat** and fill in:

- **Name**: shown as the device name, e.g. "Family group".
- **Chat ID**: a number or group ID, see above.
- **Only replies to messages sent by this bridge**: see [Replies](#replies).

Each chat becomes its own device with:

| Entity | What it does |
|---|---|
| Message (`notify`) | Sends to this chat. |
| Send test message (`button`) | Sends a test message to this chat. |
| Last message (`sensor`) | State is the text of the last message received in this chat (max 255 characters). Attributes: `text`, `sender`, `chat_id`, `from_me`, `is_group`, `id`, `quoted_message_id`, `quoted_text`, `quoted_from_me`. |

Use *⋮ > Reconfigure* on the chat to edit it, or delete it to remove the device. **Incoming messages are only passed on from chats you added** (privacy: everything else is dropped in the bridge).

The main **WhatsApp** device (the bridge) has:

| Entity | What it does |
|---|---|
| Status (`sensor`) | `connected`, `connecting`, `pairing_required`, `logged_out`, `error` or `bridge_unreachable`. |
| Send test message (`button`) | Sends a test message to the default chat. |
| Message (`notify`) | Sends to the **default chat** from the integration options. Shows *unknown* until the first message; that is normal. |

Entity IDs follow your HA language (check them under Developer tools > States).

### Sending

To a chat you added, with its notify entity:

```yaml
action: notify.send_message
target:
  entity_id: notify.family_group_message
data:
  title: "Bins"
  message: "The bins go out tomorrow"
```

To any chat (or the default chat when `chat_id` is left out); it also returns the id of the sent message:

```yaml
action: whatsapp_notifier.send_message
data:
  chat_id: "120363012345678901@g.us"
  message: "Dinner is ready"
response_variable: sent   # sent.message_id
```

WhatsApp formatting works in the text: `*bold*`, `_italic_`, `~strike~`.

### Testing

1. `Status` shows *Connected*.
2. Press **Send test message** on a chat device (it goes to that chat), or on the main WhatsApp device (it goes to the default chat set in *Configure*).
3. Call `whatsapp_notifier.send_message` in Developer tools > Actions with a `chat_id`.
4. If something fails, check the bridge log and the *Status* sensor (`pairing_required` means: link via `/qr`; after `logged_out`, the bridge automatically creates a fresh session and shows a new QR code).

### Replies

When someone writes in a chat you added, its **Last message** sensor updates and the event `whatsapp_notifier_message_received` fires with the same fields as the sensor attributes.

**Send an image:** give `whatsapp_notifier.send_message` an `image` (path of a PNG or JPEG, max 5 MB). The `message` becomes the caption. The file must be in a folder Home Assistant allows, such as `/config/www` (or one listed in `allowlist_external_dirs`). This needs Go Bridge 0.5.0 or newer.

```yaml
action: whatsapp_notifier.send_message
data:
  chat_id: "120363012345678901@g.us"
  message: "Weekly chart"
  image: /config/www/chart.png
```

**Reply to one specific message:** WhatsApp lets people swipe a message and answer on it. Then `quoted_message_id` is the id of the message that was answered, `quoted_text` is its text, and `quoted_from_me` is `true` when it was a message sent by the bridge. Tick *Only replies to messages sent by this bridge* on a chat and its sensor ignores everything else. To tie an answer to one exact message, compare `quoted_message_id` with the `message_id` returned by `whatsapp_notifier.send_message`.

Example: ask, then wait up to 10 minutes for "yes" as a reply *to that message*:

```yaml
actions:
  - action: whatsapp_notifier.send_message
    data: { message: "Bins out tomorrow. Already outside?" }
    response_variable: sent
  # keep waiting until someone answers our message; other chat messages are skipped.
  # On timeout the script stops.
  - repeat:
      sequence:
        - wait_for_trigger:
            - trigger: event
              event_type: whatsapp_notifier_message_received
          timeout: "00:10:00"
          continue_on_timeout: false
      # answer words in any language you like
      until: >
        {{ wait.trigger.event.data.quoted_message_id == sent.message_id
           and wait.trigger.event.data.text | lower | trim in ['yes', 'ja', 'oui'] }}
  - action: input_boolean.turn_on
    target: { entity_id: input_boolean.bins_outside }
```

#### Notes for automations on the sensor

- If someone sends the same text twice in a row ("yes", "yes"), the sensor **state** does not change, only the `id` attribute does. A state trigger with `to:` then fires only once. Trigger on *any* change of the sensor (no `to:`), or use the event as above.
- The sensor is empty after a Home Assistant restart until the next message arrives.
- `sender` can be a WhatsApp internal id (`...@lid`) in groups instead of a phone number.

### Ready-made script

A script that sends a formatted message (emoji, bold title, text, italic footer) to a chat and falls back to a phone notification when WhatsApp fails. Replace `notify.mobile_app_YOUR_PHONE`. Leave `chat_id` empty to use the default chat.

```yaml
alias: WhatsApp notification
description: Send a formatted WhatsApp message, with a phone notification as fallback.
icon: mdi:whatsapp
mode: parallel
max: 20
fields:
  emoji:
    name: Emoji
    example: "🧺"
    selector: { text: }
  title:
    name: Title
    description: Shown in bold
    required: true
    example: Washing machine done!
    selector: { text: }
  text:
    name: Text
    required: true
    example: The laundry is ready.
    selector:
      text:
        multiline: true
  footer:
    name: Footer
    description: Optional italic line at the bottom
    selector: { text: }
  chat_id:
    name: Chat ID
    description: Leave empty for the default chat
    selector: { text: }
sequence:
  - variables:
      body: |-
        {% if emoji | default('') %}{{ emoji }} {% endif %}*{{ title }}*

        {{ text }}{% if footer | default('') %}

        _{{ footer }}_{% endif %}
  - action: whatsapp_notifier.send_message
    data:
      chat_id: "{{ chat_id | default('') }}"
      message: "{{ body }}"
    response_variable: sent
    continue_on_error: true
  - alias: Fall back to the phone when WhatsApp fails
    if:
      - condition: template
        value_template: "{{ sent is not defined or sent.message_id is not defined }}"
    then:
      - action: notify.mobile_app_YOUR_PHONE
        data:
          title: "{{ title }}"
          message: "{{ text }} (WhatsApp failed)"
```

Use it from an automation with `action: script.whatsapp_notification` and `data: { title: ..., text: ... }`.

### Same number for sending and replying

Messages you type on your own phone arrive on the linked device too (`from_me: true`); the bridge only drops its *own* sends, so your replies should reach HA. Replying from your own phone to a bridge message in a group should work too (`quoted_from_me` is then `true`), but this is not confirmed yet. In groups WhatsApp may report senders as `@lid` ids. If it misbehaves, use a separate number for the bridge (recommended).

### Security

- Only one bridge per Home Assistant is supported; `whatsapp_notifier.send_message` uses the first configured one.
- The bridge sends at most 20 messages per minute (it answers with an error above that), to protect your number from a runaway automation.
- Anyone with the `api_token` can send messages as your account. Keep port 8099 inside your network and never forward it to the internet.

### Status

Linking, sending to a group and the *Last message* sensor are confirmed to work. Per-chat devices and *Only replies* need real-world feedback: please open an issue.

### Works with

Other integrations by the same author that send their messages through this one:

- [Workout Buddy](https://github.com/anonymouse-labs/ha-workout-buddy): workout summaries and daily overviews with a chart image, to a person or group.
- [Brandweerrooster](https://github.com/anonymouse-labs/ha-brandweerrooster): fire service turnout alerts and statistics.
- [RWM Afvalkalender](https://github.com/anonymouse-labs/ha-rwm-afval): waste collection reminders (Echt-Susteren, Dutch).

### Support

If this integration saves you some time, you can [support me on Ko-fi](https://ko-fi.com/anonymouse0501). It's entirely optional, and the integration stays free either way.


## Release and build workflow

This repository uses the release workflow from `.github/workflows`:

- **CI** runs Go tests, `go vet`, a Go build, a Docker API smoke test, HACS validation and Home Assistant `hassfest` validation.
- **Native ARM64 test build** can be started manually from Actions → *Build native Linux ARM64 binary*. Leave `release_tag` empty to get a downloadable development artifact. The build runs on a native ARM64 GitHub-hosted runner, keeps CGO enabled for SQLite, and links against musl statically so the binary does not depend on the runner’s glibc version.
- **Release Please** opens or updates a release PR from Conventional Commits on `main`. Merge the PR to create a GitHub Release.
- **Published release** builds the ARM64 standalone binary, attaches it with a `.tar.gz` archive and `SHA256SUMS`, and publishes the Docker image to GHCR.
- Stable releases receive semver tags and `latest`; pre-releases receive their exact version tag and the moving `go-beta` tag.

### One-time GitHub setup

Create a fine-grained PAT or GitHub Bridge token with repository contents (read/write), pull requests (read/write), and issues (read/write) permissions. Save it as the repository Actions secret `RELEASE_PLEASE_TOKEN`. This is needed so the release created by Release Please can trigger the build workflows. Also allow GitHub Actions to write packages for GHCR under Settings → Actions → General if repository policy restricts it.

Use Conventional Commit messages, for example `feat: add delivery receipts`, `fix: reconnect after timeout`, or `docs: clarify pairing setup`.


## Configuration reference

The native Go bridge accepts YAML (`.yaml`/`.yml`) and JSON configuration files. Select the file explicitly:

```bash
whatsapp-bridge-linux --config /absolute/path/to/config.yaml
```

You can also set `CONFIG_FILE`. If neither is set, the bridge checks its built-in default locations. Explicit `--config` is recommended for services because it makes the selected configuration unambiguous.

Supported configuration keys and their environment-variable overrides:

| Configuration key | Environment variable | Purpose |
|---|---|---|
| `host` | `HOST` | Interface to bind to (for example `127.0.0.1` or `0.0.0.0`) |
| `port` | `PORT` | HTTP/API port (default `8099`) |
| `api_token` | `API_TOKEN` | API authentication token |
| `pairing_phone` | `PAIRING_PHONE` | Optional phone-number pairing; leave empty for QR pairing |
| `data_dir` | `DATA_DIR` | Bridge data directory |
| `auth_dir` | `AUTH_DIR` | WhatsApp session/database directory |

Command-line help is available with:

```bash
whatsapp-bridge-linux --help
```

`host: "127.0.0.1"` only accepts connections from the same machine. Use `0.0.0.0` only when Home Assistant connects from another host or container, and restrict access with your firewall. Anyone with the API token can send messages through your WhatsApp account. Never expose the bridge port directly to the internet.

Keep the same `data_dir` and `auth_dir` to retain the linked WhatsApp session. The bridge stores its whatsmeow session in SQLite under `auth_dir`. A `pairing_required` status after a restart usually means the bridge is looking at a different or inaccessible authentication directory. Check the configured path and file permissions before pairing again.

## License

This project is distributed under the MIT License. See [LICENSE](LICENSE).


## Project structure

See [docs/PROJECT_STRUCTURE.md](docs/PROJECT_STRUCTURE.md) for the repository layout and the separation between the Home Assistant integration and Go bridge.
