"""BDD coverage for OpenAiClient — circuit breaker, probe, and chat."""

import json
import unittest
from datetime import datetime, timedelta
from unittest.mock import AsyncMock, MagicMock, patch

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.openai_client import OpenAiClient


class FakeClock:
    def __init__(self) -> None:
        self.now = datetime(2026, 4, 29, 10, 0, 0)

    def utcnow(self) -> datetime:
        return self.now

    def advance(self, seconds: int) -> None:
        self.now += timedelta(seconds=seconds)


_BASE_URL = "http://localhost:1234/v1"
_MODEL = "gpt-4o-mini"


class OpenAiClientCircuitBreakerTests(unittest.TestCase):
    # GIVEN OpenAiClient has failed WHEN cooldown is active THEN can_attempt is False.
    def test_given_failure_when_cooldown_active_then_client_does_not_attempt(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "", _MODEL, clock=clock.utcnow)

        client._mark_failed("connection refused")

        self.assertEqual(client.circuit_state, "open")
        self.assertFalse(client.can_attempt)
        self.assertFalse(client._should_attempt())

        clock.advance(29)
        self.assertFalse(client.can_attempt)

        clock.advance(1)
        self.assertTrue(client.can_attempt)
        self.assertEqual(client._should_attempt() and client.circuit_state, "half_open")

    # GIVEN repeated failures WHEN recorded THEN retry delay grows and caps at 5 min.
    def test_given_repeated_failures_when_recorded_then_backoff_caps(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "", _MODEL, clock=clock.utcnow)

        delays = []
        for _ in range(6):
            client._mark_failed("timeout")
            delays.append(client.retry_delay_seconds)

        self.assertEqual(delays, [30, 60, 120, 240, 300, 300])

    # GIVEN circuit is open and success is marked THEN circuit resets to closed.
    def test_given_success_after_failure_when_marked_then_circuit_resets(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "", _MODEL, clock=clock.utcnow)

        client._mark_failed("timeout")
        client._mark_success()

        self.assertEqual(client.circuit_state, "closed")
        self.assertTrue(client.available)
        self.assertTrue(client.can_attempt)

    # GIVEN base_url and model are not set WHEN is_configured is checked THEN False.
    def test_given_no_config_when_is_configured_checked_then_false(self) -> None:
        client = OpenAiClient("", "", "", clock=lambda: datetime(2026, 1, 1))
        self.assertFalse(client.is_configured)

    # GIVEN base_url and model are set WHEN is_configured is checked THEN True.
    def test_given_config_when_is_configured_checked_then_true(self) -> None:
        client = OpenAiClient(_BASE_URL, "key", _MODEL, clock=lambda: datetime(2026, 1, 1))
        self.assertTrue(client.is_configured)


class OpenAiClientAsyncTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN OpenAI returns valid JSON WHEN chat is called THEN parsed dict is returned.
    async def test_given_valid_json_response_when_chat_then_dict_returned(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "key", _MODEL, clock=clock.utcnow)
        client._mark_success()

        mock_resp = MagicMock()
        mock_resp.raise_for_status = MagicMock()
        mock_resp.json.return_value = {
            "choices": [{"message": {"content": '{"sentiment": 0.4, "confidence_scale": 1.1, "reasoning": "bullish"}'}}]
        }

        with patch("llm.openai_client.httpx.AsyncClient") as mock_cls:
            mock_cls.return_value.__aenter__ = AsyncMock(return_value=MagicMock(
                post=AsyncMock(return_value=mock_resp)
            ))
            mock_cls.return_value.__aexit__ = AsyncMock(return_value=False)
            result = await client.chat([{"role": "user", "content": "analyse"}])

        self.assertIsNotNone(result)
        self.assertEqual(result["sentiment"], 0.4)
        self.assertTrue(client.available)

    # GIVEN OpenAI returns non-JSON WHEN chat is called THEN None returned, circuit stays closed.
    async def test_given_non_json_response_when_chat_then_none_and_circuit_stays_closed(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "key", _MODEL, clock=clock.utcnow)
        client._mark_success()

        mock_resp = MagicMock()
        mock_resp.raise_for_status = MagicMock()
        mock_resp.json.return_value = {
            "choices": [{"message": {"content": "I cannot answer that in JSON"}}]
        }

        with patch("llm.openai_client.httpx.AsyncClient") as mock_cls:
            mock_cls.return_value.__aenter__ = AsyncMock(return_value=MagicMock(
                post=AsyncMock(return_value=mock_resp)
            ))
            mock_cls.return_value.__aexit__ = AsyncMock(return_value=False)
            result = await client.chat([{"role": "user", "content": "analyse"}])

        self.assertIsNone(result)
        # JSON parse failure must not trip the circuit breaker
        self.assertTrue(client.available)
        self.assertEqual(client.circuit_state, "closed")

    # GIVEN network error WHEN chat is called THEN None returned and circuit opens.
    async def test_given_network_error_when_chat_then_circuit_opens(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "key", _MODEL, clock=clock.utcnow)
        client._mark_success()

        import httpx as _httpx
        with patch("llm.openai_client.httpx.AsyncClient") as mock_cls:
            mock_cls.return_value.__aenter__ = AsyncMock(
                side_effect=_httpx.ConnectError("refused")
            )
            mock_cls.return_value.__aexit__ = AsyncMock(return_value=False)
            result = await client.chat([{"role": "user", "content": "analyse"}])

        self.assertIsNone(result)
        self.assertFalse(client.available)
        self.assertEqual(client.circuit_state, "open")

    # GIVEN client is not configured WHEN probe is called THEN False returned immediately.
    async def test_given_not_configured_when_probe_then_false_without_network_call(self) -> None:
        client = OpenAiClient("", "", "", clock=lambda: datetime(2026, 1, 1))
        result = await client.probe()
        self.assertFalse(result)
        self.assertFalse(client.available)

    # GIVEN successful probe WHEN probe is called THEN available is True.
    async def test_given_successful_probe_when_probe_then_available(self) -> None:
        clock = FakeClock()
        client = OpenAiClient(_BASE_URL, "key", _MODEL, clock=clock.utcnow)

        mock_resp = MagicMock()
        mock_resp.raise_for_status = MagicMock()

        with patch("llm.openai_client.httpx.AsyncClient") as mock_cls:
            mock_cls.return_value.__aenter__ = AsyncMock(return_value=MagicMock(
                post=AsyncMock(return_value=mock_resp)
            ))
            mock_cls.return_value.__aexit__ = AsyncMock(return_value=False)
            result = await client.probe()

        self.assertTrue(result)
        self.assertTrue(client.available)
        self.assertEqual(client.circuit_state, "closed")


if __name__ == "__main__":
    unittest.main()
