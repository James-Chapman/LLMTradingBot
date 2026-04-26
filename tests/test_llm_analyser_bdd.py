"""BDD coverage for LLM prompt construction."""
import unittest
from datetime import datetime, timezone

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.analyser import LLMAnalyser, MarketBriefing, Reflection


class FakeLLMClient:
    def __init__(self, response: dict | None = None) -> None:
        self.available = True
        self.can_attempt = True
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

    # BUG-028: GIVEN the LLM returns a string outlook WHEN a briefing is parsed
    # THEN the outlook is normalised to the dict shape expected by strategy consumers.
    async def test_given_string_market_outlook_when_briefing_parsed_then_outlook_shape_is_normalised(self) -> None:
        client = FakeLLMClient({
            "market_outlooks": {
                "BTC/EUR": "bullish",
            },
            "overall_sentiment": 0.2,
            "key_insight": "Risk appetite improving",
        })
        analyser = LLMAnalyser(client)

        briefing = await analyser.brief_market(
            [{"id": "n1", "source": "test", "title": "Bitcoin rises", "summary": "BTC bid"}],
            {"BTC/EUR": {"price": 100.0, "indicators": {}}},
        )

        self.assertEqual(briefing.market_outlooks["BTC/EUR"]["bias"], "bullish")
        self.assertEqual(briefing.market_outlooks["BTC/EUR"]["score"], 0.0)
        self.assertEqual(briefing.market_outlooks["BTC/EUR"]["note"], "")

    # GIVEN persisted naive LLM context timestamps WHEN signal analysis builds prompt context
    # THEN UTC age calculations do not crash.
    async def test_given_naive_context_timestamps_when_signal_analysed_then_age_context_is_safe(self) -> None:
        client = FakeLLMClient({
            "sentiment": 0.2,
            "confidence_scale": 1.1,
            "reasoning": "Context accepted",
        })
        analyser = LLMAnalyser(client)
        naive_now = datetime.now(timezone.utc).replace(tzinfo=None)
        analyser.latest_briefing = MarketBriefing(
            market_outlooks={"BTC/EUR": {"bias": "bullish", "score": 0.3, "note": "momentum"}},
            overall_sentiment=0.25,
            key_insight="Market context available",
            article_count=2,
            generated_at=naive_now,
        )
        analyser.latest_reflection = Reflection(
            pattern="Recent longs worked",
            suggestion="Keep position sizing steady",
            insight_confidence=0.7,
            generated_at=naive_now,
        )

        analysis = await analyser.analyse_signal(
            market="BTC/EUR",
            direction="long",
            momentum_pct=1.2,
            base_confidence=0.65,
            news=[],
            current_price=100.0,
            indicators={},
            equity=500.0,
            cash=400.0,
            open_positions=[],
        )

        prompt = client.messages[1]["content"]
        self.assertTrue(analysis.llm_used)
        self.assertIn("Latest market briefing", prompt)
        self.assertIn("Your most recent self-reflection", prompt)

    # BUG-028: GIVEN legacy briefing data has a string market outlook WHEN signal analysis builds context
    # THEN prompt construction does not crash on the malformed outlook.
    async def test_given_string_context_outlook_when_signal_analysed_then_prompt_context_is_safe(self) -> None:
        client = FakeLLMClient({
            "sentiment": 0.2,
            "confidence_scale": 1.1,
            "reasoning": "Context accepted",
        })
        analyser = LLMAnalyser(client)
        analyser.latest_briefing = MarketBriefing(
            market_outlooks={"BTC/EUR": "bullish"},
            overall_sentiment=0.25,
            key_insight="Market context available",
            article_count=2,
        )

        analysis = await analyser.analyse_signal(
            market="BTC/EUR",
            direction="long",
            momentum_pct=1.2,
            base_confidence=0.65,
            news=[],
            current_price=100.0,
            indicators={},
            equity=500.0,
            cash=400.0,
            open_positions=[],
        )

        prompt = client.messages[1]["content"]
        self.assertTrue(analysis.llm_used)
        self.assertIn("BTC/EUR outlook: bullish", prompt)

    # GIVEN the Ollama circuit is ready for a half-open retry WHEN signal analysis runs
    # THEN the analyser attempts the LLM call even though available is still false.
    async def test_given_half_open_retry_ready_when_signal_analysed_then_llm_call_is_attempted(self) -> None:
        client = FakeLLMClient({
            "sentiment": 0.1,
            "confidence_scale": 1.0,
            "reasoning": "Retry succeeded",
        })
        client.available = False
        client.can_attempt = True
        analyser = LLMAnalyser(client)

        analysis = await analyser.analyse_signal(
            market="BTC/EUR",
            direction="long",
            momentum_pct=0.5,
            base_confidence=0.65,
            news=[],
            current_price=100.0,
            indicators={},
            equity=500.0,
            cash=500.0,
            open_positions=[],
        )

        self.assertTrue(analysis.llm_used)
        self.assertEqual(client.messages[0]["role"], "system")


if __name__ == "__main__":
    unittest.main()
