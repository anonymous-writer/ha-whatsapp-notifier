# Project structure

The repository contains two cooperating parts:

- `custom_components/whatsapp_notifier/`: the Home Assistant custom integration (Python).
- `whatsapp_bridge/`: the standalone Go bridge, with a thin executable entry point and application code under `internal/app/`.

The Go bridge follows the same organization used by the companion LeaseEditor-Dnsmasq project:

```text
whatsapp_bridge/
├── main.go                 # Minimal executable entry point
├── go.mod
├── Dockerfile
├── config.yaml             # Home Assistant app/add-on metadata and options
├── config.example.json     # Native Linux configuration example
├── config.example.yaml     # Native Linux configuration example
└── internal/
    └── app/
        ├── app.go          # Bridge implementation
        └── app_test.go     # Bridge unit tests
```

Keep Home Assistant integration files in `custom_components/`; HACS expects that location.
