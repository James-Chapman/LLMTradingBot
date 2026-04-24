"""BDD coverage for LLM prompt construction."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.analyser import LLMAnalyser


class FakeLLMClient:
    def __init__(self, response: dict | None = None) -> None:
        self.available = True
        self.messages = []
        self.response = response or {
            "action": "long",
            "confidence": 0.82,
            "sentiment": 0.7,
            "reasoning": "Indicator context included",
        }

    # Capture chat messages and return a valid recommendation.
    async def chat(self, messages: list[dict], expect_json: bool = True):
        self.messages = messages
        return self.response


class LLMAnalyserBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN indicator context WHEN the LLM-only recommender runs THEN indicators are included in the prompt.
    async def test_given_indicators_when_recommend_trade_runs_then_prompt_contains_indicator_context(self) -> None:
        client = FakeLLMClient()
        analyser = LLMAnalyser(client)
        indicators = {
            "rsi_14": 72.0,
            "rsi_signal": "overbought",
            "ema9": 101.0,
            "ema21": 99.0,
            "ema_cross": "bullish",
        }

        recommendation = await analyser.recommend_trade(
            market="BTC/EUR",
            current_price=102.0,
            previous_price=100.0,
            indicators=indicators,
            news=[],
            equity=500.0,
            cash=500.0,
            open_positions=[],
        )

        prompt = client.messages[1]["content"]
        self.assertEqual(recommendation.action, "long")
        self.assertIn("Technical indicators", prompt)
        self.assertIn("RSI(14): 72.0", prompt)
        self.assertIn("EMA9/21", prompt)
        self.assertIn("context only", prompt)

    # GIVEN the LLM returns market keys with spaces or dashes WHEN a briefing is parsed
    # THEN the outlooks are mapped back to the exact watched market symbols.
    async def test_given_variant_market_keys_when_briefing_parsed_then_exact_symbols_are_restored(self) -> None:
        client = FakeLLMClient({
            "market_outlooks": {
                "btc eur": {"bias": "bullish", "score": 0.4, "note": "breakout"},
                "ETH-EUR": {"bias": "neutral", "score": 0.0, "note": "mixed"},
            },
            "overall_sentiment": 0.2,
            "key_insight": "Broad risk appetite improving",
        })
        analyser = LLMAnalyser(client)

        briefing = await analyser.brief_market(
            [{"id": "n1", "source": "test", "title": "Crypto", "summary": "Markets rise"}],
            {
                "BTC/EUR": {"price": 100.0, "indicators": {}},
                "ETH/EUR": {"price": 200.0, "indicators": {}},
            },
        )

        prompt = client.messages[1]["content"]
        self.assertEqual(set(briefing.market_outlooks), {"BTC/EUR", "ETH/EUR"})
        self.assertIn("Use exactly these market symbols", prompt)


if __name__ == "__main__":
    unittest.main()
