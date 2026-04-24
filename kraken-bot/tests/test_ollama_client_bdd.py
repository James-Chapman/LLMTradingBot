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
        self.assertFalse(client._should_attempt())

        clock.advance(29)
        self.assertFalse(client._should_attempt())

        clock.advance(1)
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


if __name__ == "__main__":
    unittest.main()
