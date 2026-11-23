from __future__ import annotations

import ipaddress
import logging
from urllib.parse import urlsplit

import voluptuous as vol
from aiohttp import ClientError, ClientTimeout
from homeassistant.config_entries import (
    ConfigEntry,
    ConfigFlow,
    ConfigSubentryFlow,
    OptionsFlow,
    SubentryFlowResult,
)
from homeassistant.const import CONF_HOST, CONF_NAME, CONF_PORT
from homeassistant.core import callback
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .chat_id import normalize
from .const import CONF_CHAT_ID, CONF_DEFAULT_CHAT, CONF_ONLY_REPLIES, CONF_TOKEN, DOMAIN, SUBENTRY_CHAT

_LOGGER = logging.getLogger(__name__)


def _normalize_host(value: str) -> str | None:
    """Accept a hostname, IPv4/IPv6 address, or an http(s) base URL."""
    raw = value.strip()
    if not raw:
        return None
    if "://" in raw:
        parsed = urlsplit(raw)
        if parsed.scheme != "http" or not parsed.hostname or parsed.path not in ("", "/"):
            return None
        if parsed.username or parsed.password or parsed.query or parsed.fragment:
            return None
        raw = parsed.hostname
    else:
        raw = raw.strip("[]")
        if any(char in raw for char in "/?#@"):
            return None
    try:
        return ipaddress.ip_address(raw).compressed
    except ValueError:
        candidate = raw.rstrip(".")
        if len(candidate) > 253 or not all(
            part and len(part) <= 63 and part[0] != "-" and part[-1] != "-"
            and all(ch.isalnum() or ch == "-" for ch in part)
            for part in candidate.split(".")
        ):
            return None
        return candidate


async def _check_bridge(hass, host: str, port: int, token: str) -> tuple[str | None, dict | None]:
    """Return a translated error key and status payload after validating the bridge."""
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    try:
        async with async_get_clientsession(hass).get(
            f"http://{host}:{port}/status", headers=headers, timeout=ClientTimeout(total=10)
        ) as response:
            if response.status == 401:
                return "invalid_auth", None
            if response.status == 404:
                return "incompatible_bridge", None
            if response.status >= 500:
                return "bridge_error", None
            response.raise_for_status()
            payload = await response.json(content_type=None)
            if not isinstance(payload, dict) or not isinstance(payload.get("status"), str):
                return "incompatible_bridge", None
            return None, payload
    except (ClientError, TimeoutError, OSError):
        return "cannot_connect", None
    except Exception:  # noqa: BLE001
        _LOGGER.exception("Unexpected error while checking WhatsApp bridge")
        return "cannot_connect", None


class WhatsAppConfigFlow(ConfigFlow, domain=DOMAIN):
    VERSION = 1

    @staticmethod
    @callback
    def async_get_options_flow(config_entry: ConfigEntry) -> OptionsFlow:
        return WhatsAppOptionsFlow()

    @classmethod
    @callback
    def async_get_supported_subentry_types(cls, config_entry: ConfigEntry) -> dict[str, type[ConfigSubentryFlow]]:
        return {SUBENTRY_CHAT: ChatSubentryFlow}

    async def async_step_user(self, user_input=None):
        errors = {}
        if user_input is not None:
            host = _normalize_host(user_input[CONF_HOST])
            port = user_input[CONF_PORT]
            token = user_input.get(CONF_TOKEN, "").strip()
            if host is None:
                errors["base"] = "invalid_host"
            elif not 1 <= port <= 65535:
                errors["base"] = "invalid_port"
            else:
                error, _status = await _check_bridge(self.hass, host, port, token)
                if error:
                    errors["base"] = error
                else:
                    await self.async_set_unique_id(f"{host}:{port}")
                    self._abort_if_unique_id_configured()
                    return self.async_create_entry(
                        title="WhatsApp",
                        data={CONF_HOST: host, CONF_PORT: port, CONF_TOKEN: token},
                        options={CONF_DEFAULT_CHAT: user_input.get(CONF_DEFAULT_CHAT, "").strip()},
                    )
        schema = vol.Schema({
            vol.Required(CONF_HOST, default="homeassistant.local"): str,
            vol.Required(CONF_PORT, default=8099): vol.All(int, vol.Range(min=1, max=65535)),
            vol.Optional(CONF_TOKEN, default=""): str,
            vol.Optional(CONF_DEFAULT_CHAT, default=""): str,
        })
        schema = self.add_suggested_values_to_schema(schema, user_input or {})
        return self.async_show_form(step_id="user", data_schema=schema, errors=errors,
                                    description_placeholders={"test": "The bridge is checked before saving."})

    async def async_step_reconfigure(self, user_input=None):
        entry = self._get_reconfigure_entry()
        errors = {}
        if user_input is not None:
            host = _normalize_host(user_input[CONF_HOST])
            port = user_input[CONF_PORT]
            token = user_input.get(CONF_TOKEN, "").strip()
            if host is None:
                errors["base"] = "invalid_host"
            elif not 1 <= port <= 65535:
                errors["base"] = "invalid_port"
            else:
                error, _status = await _check_bridge(self.hass, host, port, token)
                if error:
                    errors["base"] = error
                else:
                    return self.async_update_reload_and_abort(
                        entry, data_updates={CONF_HOST: host, CONF_PORT: port, CONF_TOKEN: token}
                    )
        current = entry.data
        schema = vol.Schema({
            vol.Required(CONF_HOST, default=current[CONF_HOST]): str,
            vol.Required(CONF_PORT, default=current[CONF_PORT]): vol.All(int, vol.Range(min=1, max=65535)),
            vol.Optional(CONF_TOKEN, default=current.get(CONF_TOKEN, "")): str,
        })
        schema = self.add_suggested_values_to_schema(schema, user_input or {})
        return self.async_show_form(step_id="reconfigure", data_schema=schema, errors=errors)


class WhatsAppOptionsFlow(OptionsFlow):
    """Manage integration options using the ConfigEntry supplied by Home Assistant."""

    async def async_step_init(self, user_input=None):
        if user_input is not None:
            return self.async_create_entry(data=user_input)
        current = self.config_entry.options.get(CONF_DEFAULT_CHAT, "")
        return self.async_show_form(
            step_id="init", data_schema=vol.Schema({vol.Optional(CONF_DEFAULT_CHAT, default=current): str})
        )


def _chat_schema(name="", chat_id="", only_replies=False) -> vol.Schema:
    return vol.Schema({
        vol.Required(CONF_NAME, default=name): str,
        vol.Required(CONF_CHAT_ID, default=chat_id): str,
        vol.Required(CONF_ONLY_REPLIES, default=only_replies): bool,
    })


class ChatSubentryFlow(ConfigSubentryFlow):
    """One chat: supports selecting an existing WhatsApp group or entering a JID manually."""

    def _existing_chat_ids(self) -> set[str]:
        return {s.unique_id for s in self._get_entry().subentries.values()}

    async def _fetch_groups(self) -> list[dict]:
        entry = self._get_entry()
        data = entry.data
        headers = {"Authorization": f"Bearer {data.get(CONF_TOKEN, '')}"} if data.get(CONF_TOKEN) else {}
        url = f"http://{data[CONF_HOST]}:{data[CONF_PORT]}/groups"
        try:
            async with async_get_clientsession(self.hass).get(
                url, headers=headers, timeout=ClientTimeout(total=20)
            ) as response:
                if response.status != 200:
                    return []
                payload = await response.json(content_type=None)
                if not isinstance(payload, list):
                    return []
                return [g for g in payload if isinstance(g, dict) and g.get("chat_id") and g.get("name")]
        except (ClientError, TimeoutError, OSError):
            return []

    @staticmethod
    def _chat(user_input: dict) -> tuple[str | None, dict[str, str]]:
        try:
            return normalize(user_input[CONF_CHAT_ID]), {}
        except (ValueError, KeyError):
            return None, {CONF_CHAT_ID: "invalid_chat_id"}

    async def async_step_user(self, user_input=None) -> SubentryFlowResult:
        groups = await self._fetch_groups()
        available = {g["chat_id"]: g["name"] for g in groups if g["chat_id"] not in self._existing_chat_ids()}
        if available:
            return await self.async_step_select_group()
        return await self.async_step_manual()

    async def async_step_select_group(self, user_input=None) -> SubentryFlowResult:
        groups = await self._fetch_groups()
        existing = self._existing_chat_ids()
        choices = {g["chat_id"]: g["name"] for g in groups if g["chat_id"] not in existing}
        if user_input is not None:
            if user_input.get("manual_entry"):
                return await self.async_step_manual()
            chat_id = user_input.get(CONF_CHAT_ID, "")
            if chat_id not in choices:
                return self.async_abort(reason="group_not_available")
            return self.async_create_entry(
                title=choices[chat_id],
                data={CONF_CHAT_ID: chat_id, CONF_ONLY_REPLIES: user_input.get(CONF_ONLY_REPLIES, False)},
                unique_id=chat_id,
            )
        if not choices:
            return await self.async_step_manual()
        schema = vol.Schema({
            vol.Optional("manual_entry", default=False): bool,
            vol.Optional(CONF_CHAT_ID): vol.In(choices),
            vol.Optional(CONF_ONLY_REPLIES, default=False): bool,
        })
        return self.async_show_form(step_id="select_group", data_schema=schema)

    async def async_step_manual(self, user_input=None) -> SubentryFlowResult:
        errors: dict[str, str] = {}
        if user_input is not None:
            chat, errors = self._chat(user_input)
            if chat and chat in self._existing_chat_ids():
                return self.async_abort(reason="already_configured")
            if chat:
                return self.async_create_entry(
                    title=user_input[CONF_NAME].strip() or chat,
                    data={CONF_CHAT_ID: chat, CONF_ONLY_REPLIES: user_input[CONF_ONLY_REPLIES]},
                    unique_id=chat,
                )
        return self.async_show_form(
            step_id="manual", data_schema=self.add_suggested_values_to_schema(_chat_schema(), user_input), errors=errors
        )

    async def async_step_reconfigure(self, user_input=None) -> SubentryFlowResult:
        sub = self._get_reconfigure_subentry()
        errors: dict[str, str] = {}
        if user_input is not None:
            chat, errors = self._chat(user_input)
            if chat:
                return self.async_update_and_abort(
                    self._get_entry(), sub, unique_id=chat,
                    title=user_input[CONF_NAME].strip() or chat,
                    data_updates={CONF_CHAT_ID: chat, CONF_ONLY_REPLIES: user_input[CONF_ONLY_REPLIES]},
                )
        return self.async_show_form(
            step_id="reconfigure", data_schema=_chat_schema(
                sub.title, sub.data[CONF_CHAT_ID], sub.data.get(CONF_ONLY_REPLIES, False)
            ), errors=errors,
        )
