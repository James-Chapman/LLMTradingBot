"""BDD coverage for the Transformers client circuit breaker."""

import unittest
from datetime import datetime, timedelta
from unittest.mock import AsyncMock, MagicMock

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.transformers_client import TransformersClient

_TRANSFORMERS_LLM_MODEL = "midnigter/CryptoGemma-4B-v1"


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


class TransformersClientBDDTests(unittest.TestCase):
    # GIVEN Transformers has failed WHEN the circuit breaker is checked before cooldown
    # THEN requests are skipped until the half-open retry window.
    def test_given_failure_when_cooldown_active_then_client_does_not_attempt(self) -> None:
        clock = FakeClock()
        client = TransformersClient(_TRANSFORMERS_LLM_MODEL, clock=clock.utcnow)

        client._mark_failed("model load failed")

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

    # GIVEN repeated Transformers failures WHEN failures are recorded THEN retry delays
    # grow exponentially and cap at five minutes.
    def test_given_repeated_failures_when_recorded_then_backoff_increases_to_cap(self) -> None:
        clock = FakeClock()
        client = TransformersClient(_TRANSFORMERS_LLM_MODEL, clock=clock.utcnow)

        delays = []
        for _ in range(6):
            client._mark_failed("timeout")
            delays.append(client.retry_delay_seconds)

        self.assertEqual(delays, [30, 60, 120, 240, 300, 300])

    # GIVEN the half-open retry succeeds WHEN success is marked THEN the circuit closes.
    def test_given_success_after_failure_when_marked_then_circuit_resets(self) -> None:
        clock = FakeClock()
        client = TransformersClient(_TRANSFORMERS_LLM_MODEL, clock=clock.utcnow)

        client._mark_failed("timeout")
        clock.advance(30)
        self.assertTrue(client._should_attempt())

        client._mark_success()

        self.assertEqual(client.circuit_state, "closed")
        self.assertEqual(client.retry_delay_seconds, 30)
        self.assertTrue(client._should_attempt())


class TransformersClientAsyncBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN Transformers returns JSON-like content with a missing comma WHEN JSON is expected
    # THEN the client repairs the response and keeps the service available.
    async def test_given_json_response_missing_comma_when_chat_runs_then_response_is_repaired(self) -> None:
        clock = FakeClock()
        client = TransformersClient(_TRANSFORMERS_LLM_MODEL, clock=clock.utcnow)
        client._mark_success()

        # Mock the pipeline to return malformed JSON with missing comma
        mock_pipeline = MagicMock()
        mock_pipeline.return_value = [
            {
                "generated_text": (
                    '{\n  "action": "hold"\n  "confidence": 0.0,\n  "sentiment": 0.0,\n  "reasoning": "No trade"\n}'
                )
            }
        ]
        client._pipeline = mock_pipeline

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertEqual(result["action"], "hold")
        self.assertEqual(result["confidence"], 0.0)
        self.assertTrue(client.available)
        self.assertEqual(client.circuit_state, "closed")

    # GIVEN Transformers returns malformed JSON WHEN JSON is expected
    # THEN the task fails but the Transformers service is not marked unavailable.
    async def test_given_non_json_model_response_when_chat_runs_then_service_stays_available(self) -> None:
        clock = FakeClock()
        client = TransformersClient(_TRANSFORMERS_LLM_MODEL, clock=clock.utcnow)
        client._mark_success()

        # Mock the pipeline to return non-JSON response
        mock_pipeline = MagicMock()
        mock_pipeline.return_value = [{"generated_text": "not valid json"}]
        client._pipeline = mock_pipeline

        result = await client.chat([{"role": "user", "content": "Return JSON"}], expect_json=True)

        self.assertIsNone(result)
        self.assertTrue(client.available)
        self.assertTrue(client.can_attempt)
        self.assertEqual(client.circuit_state, "closed")

    # GIVEN a long system prompt WHEN chat runs THEN the pipeline is called with
    # max_new_tokens (not max_length) and return_full_text=False so generation
    # is not truncated by input length and JSON is not confused with prompt text.
    async def test_given_long_prompt_when_chat_runs_then_pipeline_uses_max_new_tokens_and_no_full_text(self) -> None:
        clock = FakeClock()
        client = TransformersClient(_TRANSFORMERS_LLM_MODEL, clock=clock.utcnow)
        client._mark_success()

        mock_pipeline = MagicMock()
        mock_pipeline.return_value = [{"generated_text": '{"sentiment": 0.5, "confidence_scale": 1.2, "reasoning": "strong uptrend"}'}]
        client._pipeline = mock_pipeline

        long_system = "You are an analyst. " * 100  # simulate a long prompt
        await client.chat([{"role": "system", "content": long_system}, {"role": "user", "content": "Analyse"}], expect_json=True)

        _, call_kwargs = mock_pipeline.call_args
        self.assertIn("max_new_tokens", call_kwargs, "must use max_new_tokens, not max_length")
        self.assertNotIn("max_length", call_kwargs, "max_length must not be passed")
        self.assertEqual(call_kwargs.get("return_full_text"), False)


if __name__ == "__main__":
    unittest.main()
