"""Diagnostics support for WhatsApp Notifier."""
from __future__ import annotations

from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant

from .const import CONF_TOKEN


async def async_get_config_entry_diagnostics(hass: HomeAssistant, entry: ConfigEntry) -> dict:
    """Return diagnostic information without secrets or message contents."""
    bridge = hass.data.get("whatsapp_notifier", {}).get(entry.entry_id)
    data = {key: value for key, value in entry.data.items() if key != CONF_TOKEN}
    return {
        "config_entry": data,
        "options": dict(entry.options),
        "chat_count": len(entry.subentries),
        "bridge_status": getattr(bridge, "status", "unknown"),
        "bridge_attributes": dict(getattr(bridge, "attrs", {})),
    }
