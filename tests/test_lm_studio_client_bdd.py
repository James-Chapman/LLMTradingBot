"""BDD coverage for the LM Studio client circuit breaker."""
import unittest
from datetime import datetime, timedelta

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.lm_studio_client import LMStudioClient


class FakeClock:
    """Small controllable clock for cooldown tests."""

    def __init__(self) -> None:
        self.now = datetime(2026, 4, 24, 12, 0, 0)

    # Return the current fake UTC timestamp.
    def utcnow(self) -> datetime:
        return self.now

    # Advance fake time without sleeping.
    def advance(self, seconds: int) -> None:
        self.now += timedelta(seconds=seconds)


class LMStudioClientBDDTests(unittest.TestCase):

    # GIVEN LM Studio has failed WHEN the circuit breaker is checked before cooldown
    # THEN requests are skipped until the half-open retry window.
    def test_given_failure_when_cooldown_active_then_client_does_not_attempt(self) -> None:
        clock = FakeClock()
        client = LMStudioClient("http://localhost:1234", "google/gemma-4-e4b", clock=clock.utcnow)

        client._mark_failed("connection refused")

        self.assertEqual(client.circuit_state, "open")
        self.assertFalse(client.can_attempt)
        self.assertFalse(client._should_attempt())

        clock.advance(29)
        self.assertFalse(client.can_attempt)
        self.assertFalse(client._should_attempt())

        clock.advance(1)
        self.assertTrue(client.can_attempt)
        self.assertTrue(client._should_attempt())
        self.assertEqual(client.circuit_state, "half_open")

    # GIVEN repeated LM Studio failures WHEN failures are recorded THEN retry delays
    # grow exponentially and cap at five minutes.
    def test_given_repeated_failures_when_recorded_then_backoff_increases_to_cap(self) -> None:
        clock = FakeClock()
        client = LMStudioClient("http://localhost:1234", "google/gemma-4-e4b", clock=clock.utcnow)

        delays = []
        for _ in range(6):
            client._mark_failed("timeout")
            delays.append(client.retry_delay_seconds)

        self.assertEqual(delays, [30, 60, 120, 240, 300, 300])

    # GIVEN the half-open retry succeeds WHEN success is marked THEN the circuit closes.
    def test_given_success_after_failure_when_marked_then_circuit_resets(self) -> None:
        clock = FakeClock()
        client = LMStudioClient("http://localhost:1234", "google/gemma-4-e4b", clock=clock.utcnow)

        client._mark_failed("timeout")
        clock.advance(30)
        self.assertTrue(client._should_attempt())

        client._mark_success()

        self.assertEqual(client.circuit_state, "closed")
        self.assertEqual(client.retry_delay_seconds, 30)
        self.assertTrue(client._should_attempt())


class FakeLMStudioHTTPResponse:
    """Small httpx.Response stand-in for chat tests."""

    def __init__(self, content: str) -> None:
        self._content = content

    # No-op because the fake response is always HTTP 200.
    def raise_for_status(self) -> None:
        pass

    # Return the OpenAI chat completion response shape with configurable content.
    def json(self) -> dict:
        return {"choices": [{"message": {"content": self._content}}]}


class FakeLMStudioHTTPClient:
    """Async HTTP client stand-in used by LMStudioClient.chat tests."""

    def __init__(self, response_content: str) -> None:
        self.response_content = response_content

    # Return a fake successful LM Studio chat response.
    async def post(self, *_args, **_kwargs):
        return FakeLMStudioHTTPResponse(self.response_content)


class LMStudioClientAsyncBDDTests(unittest.IsolatedAsyncioTestCase):

    # GIVEN LM Studio returns valid JSON WHEN chat is called THEN response is parsed correctly.
    async def test_given_valid_json_response_when_chat_runs_then_response_is_parsed(self) -> None:
        clock = FakeClock()
        client = LMStudioClient("http://localhost:1234", "google/gemma-4-e4b", clock=clock.utcnow)
        client._mark_success()
        client._client = FakeLMStudioHTTPClient('{"action": "hold", "confidence": 0.5}')

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertEqual(result["action"], "hold")
        self.assertEqual(result["confidence"], 0.5)
        self.assertTrue(client.available)
        self.assertEqual(client.circuit_state, "closed")

    # GIVEN LM Studio returns JSON-like content with a missing comma WHEN JSON is expected
    # THEN the client repairs the response and keeps the service available.
    async def test_given_json_missing_comma_when_chat_runs_then_response_is_repaired(self) -> None:
        clock = FakeClock()
        client = LMStudioClient("http://localhost:1234", "google/gemma-4-e4b", clock=clock.utcnow)
        client._mark_success()
        client._client = FakeLMStudioHTTPClient(
            '{\n'
            '  "action": "hold"\n'
            '  "confidence": 0.0\n'
            '}'
        )

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertEqual(result["action"], "hold")
        self.assertTrue(client.available)

    # GIVEN LM Studio returns HTTP 200 with malformed JSON WHEN JSON is expected
    # THEN the call returns None but the service is not marked unavailable.
    async def test_given_non_json_response_when_chat_runs_then_service_stays_available(self) -> None:
        clock = FakeClock()
        client = LMStudioClient("http://localhost:1234", "google/gemma-4-e4b", clock=clock.utcnow)
        client._mark_success()
        client._client = FakeLMStudioHTTPClient("not valid json")

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertIsNone(result)
        self.assertTrue(client.available)
        self.assertTrue(client.can_attempt)
        self.assertEqual(client.circuit_state, "closed")


if __name__ == "__main__":
    unittest.main()
