# WhatsApp linked-device identity

The bridge sets whatsmeow's companion device properties before creating a
client:

- `Os`: `Home Assistant`
- `PlatformType`: `DESKTOP`

WhatsApp determines the icon shown in its Linked devices UI. The bridge cannot
upload a custom Home Assistant PNG for that icon.

The identity is registered during pairing. To see the new name/platform on an
existing account, unlink the old companion device in WhatsApp and pair this
bridge again. Existing local SQLite session data should only be removed if a
fresh pairing is intentionally required; unlinking the old companion entry and
re-pairing is the supported user-facing procedure.
