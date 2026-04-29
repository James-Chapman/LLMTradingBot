"""BDD coverage for shared LLM client helpers."""

import unittest
from datetime import datetime, timedelta

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.common import CircuitBreakerMixin, loads_model_json, messages_to_prompt


class FakeClock:
    """Small controllable clock for shared circuit tests."""

    def __init__(self) -> None:
        self.now = datetime(2026, 4, 29, 12, 0, 0)

    # Return the current fake UTC timestamp.
    def utcnow(self) -> datetime:
        return self.now

    # Advance fake time without sleeping.
    def advance(self, seconds: int) -> None:
        self.now += timedelta(seconds=seconds)


class DummyCircuitClient(CircuitBreakerMixin):
    """Minimal client using the shared circuit breaker."""

    def __init__(self, clock: FakeClock) -> None:
        self.logged_failures: list[str] = []
        self._init_circuit_breaker(clock.utcnow)

    # Record failure logs without binding the test to a real logger.
    def _log_failure(self, reason: str) -> None:
        self.logged_failures.append(reason)


class LLMCommonJsonBDDTests(unittest.TestCase):
    # GIVEN fenced JSON with a missing comma WHEN parsed THEN a dict is returned.
    def test_given_fenced_json_missing_comma_when_loaded_then_response_is_repaired(self) -> None:
        content = """```json
{
  "action": "hold"
  "confidence": 0.0
}
```"""

        result = loads_model_json(content)

        self.assertEqual(result["action"], "hold")
        self.assertEqual(result["confidence"], 0.0)

    # GIVEN prose around JSON WHEN parsed THEN the first JSON object is extracted.
    def test_given_prose_around_json_when_loaded_then_first_object_is_extracted(self) -> None:
        result = loads_model_json('Result follows: {"sentiment": 0.4} done.')

        self.assertEqual(result["sentiment"], 0.4)


class LLMCommonPromptBDDTests(unittest.TestCase):
    # GIVEN OpenAI-style messages WHEN rendered THEN a stable prompt transcript is returned.
    def test_given_messages_when_rendered_then_roles_are_labelled(self) -> None:
        prompt = messages_to_prompt(
            [
                {"role": "system", "content": "Return JSON."},
                {"role": "user", "content": "Analyse BTC/EUR."},
            ],
            append_json_instruction=True,
        )

        self.assertEqual(
            prompt,
            "System: Return JSON.\nUser: Analyse BTC/EUR.\nRespond with valid JSON only.",
        )


class LLMCommonCircuitBDDTests(unittest.TestCase):
    # GIVEN failure WHEN cooldown is active THEN attempts are blocked until retry time.
    def test_given_failure_when_cooldown_active_then_half_open_after_delay(self) -> None:
        clock = FakeClock()
        client = DummyCircuitClient(clock)

        client._mark_failed("timeout")

        self.assertEqual(client.circuit_state, "open")
        self.assertFalse(client.can_attempt)
        self.assertFalse(client._should_attempt())

        clock.advance(30)

        self.assertTrue(client.can_attempt)
        self.assertTrue(client._should_attempt())
        self.assertEqual(client.circuit_state, "half_open")
        self.assertEqual(client.logged_failures, ["timeout"])

    # GIVEN repeated failures WHEN recorded THEN retry delay grows and caps.
    def test_given_repeated_failures_when_recorded_then_retry_delay_caps(self) -> None:
        clock = FakeClock()
        client = DummyCircuitClient(clock)

        delays = []
        for _ in range(6):
            client._mark_failed("timeout")
            delays.append(client.retry_delay_seconds)

        self.assertEqual(delays, [30, 60, 120, 240, 300, 300])


if __name__ == "__main__":
    unittest.main()
