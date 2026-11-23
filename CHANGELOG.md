# Changelog

## 0.5.2

- Fix the options flow HTTP 500 error by relying on Home Assistant's read-only `OptionsFlow.config_entry` property instead of assigning to it.
- Keep the bridge connection validation, reconfiguration flow, guided WhatsApp group selection, manual chat entry, and diagnostics from the current integration package.
- Keep the native WhatsApp bridge and its API unchanged.
