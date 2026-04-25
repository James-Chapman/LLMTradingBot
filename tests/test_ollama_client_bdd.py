"""BDD coverage for the Ollama client circuit breaker."""
import unittest
from datetime import datetime, timedelta

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.client import OllamaClient


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


class OllamaClientBDDTests(unittest.TestCase):

    # GIVEN Ollama has failed WHEN the circuit breaker is checked before cooldown
    # THEN requests are skipped until the half-open retry window.
    def test_given_failure_when_cooldown_active_then_client_does_not_attempt(self) -> None:
        clock = FakeClock()
        client = OllamaClient("http://localhost:11434", "phi3:mini", clock=clock.utcnow)

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

    # GIVEN repeated Ollama failures WHEN failures are recorded THEN retry delays
    # grow exponentially and cap at five minutes.
    def test_given_repeated_failures_when_recorded_then_backoff_increases_to_cap(self) -> None:
        clock = FakeClock()
        client = OllamaClient("http://localhost:11434", "phi3:mini", clock=clock.utcnow)

        delays = []
        for _ in range(6):
            client._mark_failed("timeout")
            delays.append(client.retry_delay_seconds)

        self.assertEqual(delays, [30, 60, 120, 240, 300, 300])

    # GIVEN the half-open retry succeeds WHEN success is marked THEN the circuit closes.
    def test_given_success_after_failure_when_marked_then_circuit_resets(self) -> None:
        clock = FakeClock()
        client = OllamaClient("http://localhost:11434", "phi3:mini", clock=clock.utcnow)

        client._mark_failed("timeout")
        clock.advance(30)
        self.assertTrue(client._should_attempt())

        client._mark_success()

        self.assertEqual(client.circuit_state, "closed")
        self.assertEqual(client.retry_delay_seconds, 30)
        self.assertTrue(client._should_attempt())


class FakeOllamaHTTPResponse:
    """Small httpx.Response stand-in for chat tests."""

    def __init__(self, content: str) -> None:
        self._content = content

    # No-op because the fake response is always HTTP 200.
    def raise_for_status(self) -> None:
        pass

    # Return the Ollama chat response shape with configurable text content.
    def json(self) -> dict:
        return {"message": {"content": self._content}}


class FakeOllamaHTTPClient:
    """Async HTTP client stand-in used by OllamaClient.chat tests."""

    def __init__(self, response_content: str) -> None:
        self.response_content = response_content

    # Return a fake successful Ollama chat response.
    async def post(self, *_args, **_kwargs):
        return FakeOllamaHTTPResponse(self.response_content)


class OllamaClientAsyncBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN Ollama returns JSON-like content with a missing comma WHEN JSON is expected
    # THEN the client repairs the response and keeps the service available.
    async def test_given_json_response_missing_comma_when_chat_runs_then_response_is_repaired(self) -> None:
        clock = FakeClock()
        client = OllamaClient("http://localhost:11434", "phi3:mini", clock=clock.utcnow)
        client._mark_success()
        client._client = FakeOllamaHTTPClient(
            '{\n'
            '  "action": "hold"\n'
            '  "confidence": 0.0,\n'
            '  "sentiment": 0.0,\n'
            '  "reasoning": "No trade"\n'
            '}'
        )

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertEqual(result["action"], "hold")
        self.assertEqual(result["confidence"], 0.0)
        self.assertTrue(client.available)
        self.assertEqual(client.circuit_state, "closed")

    # GIVEN Ollama returns HTTP 200 with malformed JSON WHEN JSON is expected
    # THEN the task fails but the Ollama service is not marked unavailable.
    async def test_given_non_json_model_response_when_chat_runs_then_service_stays_available(self) -> None:
        clock = FakeClock()
        client = OllamaClient("http://localhost:11434", "phi3:mini", clock=clock.utcnow)
        client._mark_success()
        client._client = FakeOllamaHTTPClient("not valid json")

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertIsNone(result)
        self.assertTrue(client.available)
        self.assertTrue(client.can_attempt)
        self.assertEqual(client.circuit_state, "closed")


if __name__ == "__main__":
    unittest.main()
