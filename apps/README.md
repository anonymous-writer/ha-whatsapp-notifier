# Home Assistant App repository

Add this repository URL in Home Assistant under **Settings → Apps → App store → Repositories**:

`https://github.com/anonymous-writer/ha-whatsapp-notifier`

Install **WhatsApp Bridge**, configure an API token, start the app, then add the **WhatsApp Notifier** integration. For the bridge host use `whatsapp-bridge` and port `8099` when Home Assistant and the app share the Supervisor network. Enter the same API token in the integration.

Open the app's Web UI to scan the WhatsApp QR code. The app stores its WhatsApp session in the persistent `/data` mount; do not remove that data when updating or restarting.
