"""
Indicator-consensus trading strategy.

Signal generation
-----------------
1. Momentum gate  — price must move ≥ 0.2 % vs the lookback price.
2. Hard filters   — extreme RSI or BB readings block the trade entirely
                    (don't LONG into RSI ≥ 80; don't SHORT into RSI ≤ 20, etc.).
3. Indicator vote — up to 10 indicators each cast +1 (agrees) / −1 (opposes) / 0.
                    Indicators used: RSI, EMA cross, BB position, MACD line bias,
                    MACD signal-line crossover, Stochastic %K, Williams %R,
                    5-minute price change, 15-minute price change.
4. Coverage gate  — at least 6 non-neutral indicators must be available.
5. Consensus gate — net vote must be ≥ 1.
6. Confidence     — base (from momentum) + indicator bonus + news bonus
                    − ATR volatility penalty; capped at 0.95.
"""
from typing import Any, Dict, List

from config.settings import settings
from domain.models import Direction, TradeIdea, TradingMode
from observability.logging import get_logger

logger = get_logger("basic_strategy")

MOMENTUM_THRESHOLD = 0.002
RSI_LONG_BLOCK = 80
RSI_SHORT_BLOCK = 20
BB_LONG_BLOCK = 95
BB_SHORT_BLOCK = 5
RSI_BULLISH_THRESHOLD = 40
RSI_BEARISH_THRESHOLD = 60
BB_BULLISH_THRESHOLD = 30
BB_BEARISH_THRESHOLD = 70
STOCH_BULLISH_THRESHOLD = 20
STOCH_BEARISH_THRESHOLD = 80
WILLIAMS_BULLISH_THRESHOLD = -80
WILLIAMS_BEARISH_THRESHOLD = -20
MIN_INDICATORS_FOR_SIGNAL = 6
MIN_CONSENSUS_VOTES = 1
BASE_CONFIDENCE_CAP = 0.50
INDICATOR_VOTE_WEIGHT = 0.05
MIN_INDICATOR_BONUS = -0.20
MAX_INDICATOR_BONUS = 0.40
NEWS_SENTIMENT_BONUS = 0.10
LLM_SENTIMENT_BONUS = 0.05
ATR_VOLATILITY_THRESHOLD_PCT = 1.0
ATR_PENALTY_PER_EXTRA_PCT = 0.05
MAX_ATR_PENALTY = 0.10
MIN_CONFIDENCE = 0.10
MAX_CONFIDENCE = 0.95
MIN_ACTIONABLE_CONFIDENCE = 0.20
POSITION_SIZING_PROPOSAL = 0.20
MIN_DYNAMIC_POSITION_SIZE = 0.05
MAX_DYNAMIC_POSITION_SIZE = 0.25


class BasicStrategy:
    """Combined trend-following strategy using indicators plus context."""

    def __init__(
        self,
        strategy_id: str = "combined",
        *,
        use_news_sentiment: bool = True,
        use_llm_sentiment: bool = True,
    ) -> None:
        self.strategy_id = strategy_id
        self.use_news_sentiment = use_news_sentiment
        self.use_llm_sentiment = use_llm_sentiment

    # ── Public interface ──────────────────────────────────────────────────────

    async def evaluate(
        self,
        market_data: Dict[str, Any],
        news_signals: List[Dict],
        learner=None,
    ) -> List[TradeIdea]:
        """Evaluate all markets and return trade ideas."""
        ideas = []

        for symbol, data in market_data.items():
            idea = self._evaluate_market(symbol, data, news_signals, learner)
            if idea is not None:
                ideas.append(idea)

        if not ideas:
            logger.debug("No signals generated this tick")
        return ideas

    # ── Per-market evaluation ─────────────────────────────────────────────────

    def _evaluate_market(
        self,
        symbol: str,
        data: Dict[str, Any],
        news_signals: List[Dict],
        learner,
    ):
        """Evaluate a single market.  Returns a TradeIdea or None."""

        if "price" not in data or "previous_price" not in data:
            return None

        current_price  = data["price"]
        previous_price = data["previous_price"]
        ind: Dict[str, Any] = data.get("indicators", {})

        # ── 1. Momentum gate ──────────────────────────────────────────────────
        momentum = (current_price - previous_price) / previous_price
        if abs(momentum) < MOMENTUM_THRESHOLD:
            return None

        is_long   = momentum > 0
        direction = Direction.LONG if is_long else Direction.SHORT

        htf = data.get("higher_timeframe") or {}
        htf_ema_cross = htf.get("ema_cross")
        if htf_ema_cross:
            htf_supports_long = htf_ema_cross == "bullish"
            htf_supports_short = htf_ema_cross == "bearish"
            if (is_long and not htf_supports_long) or (not is_long and not htf_supports_short):
                logger.debug(
                    "%s %s blocked by higher-timeframe EMA %s",
                    symbol,
                    direction.value,
                    htf_ema_cross,
                )
                return None

        # ── 2. Hard filters ───────────────────────────────────────────────────
        rsi_val = ind.get("rsi_14")
        bb_dict = ind.get("bb") or {}
        bb_pos  = bb_dict.get("position")  # 0–100 %

        if rsi_val is not None:
            if is_long and rsi_val >= RSI_LONG_BLOCK:
                logger.debug("%s LONG blocked — RSI %.1f (overbought)", symbol, rsi_val)
                return None
            if not is_long and rsi_val <= RSI_SHORT_BLOCK:
                logger.debug("%s SHORT blocked — RSI %.1f (oversold)", symbol, rsi_val)
                return None

        if bb_pos is not None:
            if is_long and bb_pos >= BB_LONG_BLOCK:
                logger.debug("%s LONG blocked — BB %.1f%% (upper extreme)", symbol, bb_pos)
                return None
            if not is_long and bb_pos <= BB_SHORT_BLOCK:
                logger.debug("%s SHORT blocked — BB %.1f%% (lower extreme)", symbol, bb_pos)
                return None

        # ── 3. Indicator consensus voting ─────────────────────────────────────
        votes     = 0   # net agreement score
        available = 0   # number of indicators with a non-neutral opinion

        def _vote(long_condition: bool, short_condition: bool) -> None:
            """Cast ±1 depending on whether indicator agrees with direction."""
            nonlocal votes, available
            if long_condition or short_condition:
                available += 1
                if (is_long and long_condition) or (not is_long and short_condition):
                    votes += 1
                elif (is_long and short_condition) or (not is_long and long_condition):
                    votes -= 1

        # RSI  (< 40 → bullish; > 60 → bearish)
        if rsi_val is not None:
            _vote(rsi_val < RSI_BULLISH_THRESHOLD, rsi_val > RSI_BEARISH_THRESHOLD)

        # EMA crossover
        ema_cross = ind.get("ema_cross")
        if ema_cross:
            _vote(ema_cross == "bullish", ema_cross == "bearish")

        # Bollinger Band position  (< 30 % = near lower → bullish; > 70 % = near upper → bearish)
        if bb_pos is not None:
            _vote(bb_pos < BB_BULLISH_THRESHOLD, bb_pos > BB_BEARISH_THRESHOLD)

        # MACD line bias
        macd_dict  = ind.get("macd") or {}
        macd_bias  = macd_dict.get("bias")
        if macd_bias:
            _vote(macd_bias == "bullish", macd_bias == "bearish")

        # MACD signal-line crossover (line > signal → bullish momentum shift)
        sig_bias = macd_dict.get("signal_bias")
        if sig_bias:
            _vote(sig_bias == "bullish", sig_bias == "bearish")

        # Stochastic %K  (< 20 oversold → bullish; > 80 overbought → bearish)
        stoch_dict = ind.get("stoch") or {}
        k_val      = stoch_dict.get("k")
        if k_val is not None:
            _vote(k_val < STOCH_BULLISH_THRESHOLD, k_val > STOCH_BEARISH_THRESHOLD)

        # Williams %R  (≤ −80 oversold → bullish; ≥ −20 overbought → bearish)
        wr_val = ind.get("williams_r")
        if wr_val is not None:
            _vote(wr_val <= WILLIAMS_BULLISH_THRESHOLD, wr_val >= WILLIAMS_BEARISH_THRESHOLD)

        # 5-minute price change
        changes = ind.get("price_changes") or {}
        chg_5m  = changes.get("5m")
        if chg_5m is not None:
            _vote(chg_5m > 0, chg_5m < 0)

        # 15-minute price change
        chg_15m = changes.get("15m")
        if chg_15m is not None:
            _vote(chg_15m > 0, chg_15m < 0)

        # ── 4. Indicator coverage gate ───────────────────────────────────────
        if available < MIN_INDICATORS_FOR_SIGNAL:
            logger.debug(
                "%s %s skipped — only %d/%d indicators available",
                symbol, direction.value, available, MIN_INDICATORS_FOR_SIGNAL,
            )
            return None

        # ── 5. Consensus gate ────────────────────────────────────────────────
        if votes < MIN_CONSENSUS_VOTES:
            logger.debug(
                "%s %s skipped — indicator consensus %+d/%d",
                symbol, direction.value, votes, available,
            )
            return None

        # ── 5. Confidence calculation ─────────────────────────────────────────
        # Base: scales with momentum magnitude, capped at 0.50 to leave room
        base = min(abs(momentum) * 100, BASE_CONFIDENCE_CAP)

        # Indicator bonus: ±0.05 per net vote, bounded to [−0.20, +0.40]
        ind_bonus = 0.0
        if available > 0:
            ind_bonus = max(
                MIN_INDICATOR_BONUS,
                min(MAX_INDICATOR_BONUS, votes * INDICATOR_VOTE_WEIGHT),
            )

        # News sentiment bonus/penalty
        news_sentiment = (
            self._news_sentiment(symbol, news_signals)
            if self.use_news_sentiment else 0.0
        )
        if (is_long and news_sentiment > 0) or (not is_long and news_sentiment < 0):
            news_bonus = NEWS_SENTIMENT_BONUS
        elif (is_long and news_sentiment < 0) or (not is_long and news_sentiment > 0):
            news_bonus = -NEWS_SENTIMENT_BONUS
        else:
            news_bonus = 0.0

        llm_sentiment = float(data.get("llm_sentiment") or 0.0) if self.use_llm_sentiment else 0.0
        if (is_long and llm_sentiment > 0) or (not is_long and llm_sentiment < 0):
            llm_bonus = LLM_SENTIMENT_BONUS
        elif (is_long and llm_sentiment < 0) or (not is_long and llm_sentiment > 0):
            llm_bonus = -LLM_SENTIMENT_BONUS
        else:
            llm_bonus = 0.0

        # ATR volatility penalty: each 1 % above 1 % ATR/price shaves 0.05
        atr_pct = ind.get("atr_pct") or 0.0
        atr_penalty = (
            -min(
                MAX_ATR_PENALTY,
                max(0.0, (atr_pct - ATR_VOLATILITY_THRESHOLD_PCT) * ATR_PENALTY_PER_EXTRA_PCT),
            )
            if atr_pct > ATR_VOLATILITY_THRESHOLD_PCT else 0.0
        )

        confidence = max(
            MIN_CONFIDENCE,
            min(MAX_CONFIDENCE, base + ind_bonus + news_bonus + llm_bonus + atr_penalty),
        )

        # Historical-performance adjustment
        if learner is not None:
            confidence = learner.adjust_confidence(
                self.strategy_id, symbol, direction.value, confidence
            )

        if confidence < MIN_ACTIONABLE_CONFIDENCE:
            return None

        # ── Build idea ────────────────────────────────────────────────────────
        sl_pct = int(settings.stop_loss_pct * 100)
        thesis_parts = [f"Momentum {momentum:.2%}"]
        if rsi_val is not None:
            thesis_parts.append(f"RSI {rsi_val}")
        if ema_cross:
            thesis_parts.append(f"EMA {ema_cross}")
        if macd_bias:
            thesis_parts.append(f"MACD {macd_bias}")
        if sig_bias:
            thesis_parts.append(f"MACD-X {sig_bias}")
        if k_val is not None:
            thesis_parts.append(f"Stoch {k_val}")
        if wr_val is not None:
            thesis_parts.append(f"WR {wr_val}")
        thesis_parts.append(f"consensus {votes:+d}/{available}")

        position_size = self._position_sizing(symbol, direction.value, atr_pct, learner)

        idea = TradeIdea(
            strategy_id=self.strategy_id,
            market=symbol,
            direction=direction,
            thesis=" | ".join(thesis_parts),
            supporting_signals={
                "momentum":           round(momentum, 6),
                "uses_news_sentiment": self.use_news_sentiment,
                "uses_llm_sentiment":  self.use_llm_sentiment,
                "news_sentiment":     round(news_sentiment, 3),
                "llm_sentiment":      round(llm_sentiment, 3),
                "current_price":      current_price,
                "previous_price":     previous_price,
                "indicator_votes":    votes,
                "indicators_available": available,
                "rsi_14":             rsi_val,
                "ema_cross":          ema_cross,
                "bb_position":        bb_pos,
                "macd_bias":          macd_bias,
                "macd_signal_bias":   sig_bias,
                "macd_histogram":     macd_dict.get("histogram"),
                "stoch_k":            k_val,
                "stoch_d":            stoch_dict.get("d"),
                "williams_r":         wr_val,
                "atr_pct":            round(atr_pct, 3) if atr_pct else None,
                "price_change_5m":    chg_5m,
                "price_change_15m":   chg_15m,
            },
            confidence=confidence,
            entry_plan="Enter at market on indicator consensus confirmation",
            exit_plan=(
                f"Exit on momentum reversal or stop-loss at {sl_pct}% loss"
            ),
            stop_or_invalidation=(
                f"Abandon if unrealised loss reaches {sl_pct}%"
            ),
            position_sizing_proposal=position_size,
            mode_eligibility=[
                TradingMode.MANUAL,
                TradingMode.SEMI_AUTOMATED,
                TradingMode.FULLY_AUTOMATED,
            ],
        )

        logger.info(
            "Generated trade idea",
            extra={
                "strategy":   self.strategy_id,
                "market":     symbol,
                "direction":  direction.value,
                "confidence": round(confidence, 3),
                "votes":      f"{votes:+d}/{available}",
                "rsi":        rsi_val,
                "ema_cross":  ema_cross,
            },
        )
        return idea

    # ── Helpers ───────────────────────────────────────────────────────────────

    def _news_sentiment(self, symbol: str, news_signals: List[Dict]) -> float:
        """Average headline sentiment for news articles mentioning this asset."""
        asset = symbol.split("/")[0]
        sentiments = [
            s.get("headline_sentiment", 0)
            for s in news_signals
            if asset in s.get("asset_mentions", [])
        ]
        return sum(sentiments) / len(sentiments) if sentiments else 0.0

    def _position_sizing(self, symbol: str, direction: str, atr_pct: float, learner) -> float:
        """Return volatility- and learner-adjusted position sizing."""
        size = POSITION_SIZING_PROPOSAL
        if atr_pct > 1.0:
            size *= max(0.25, 1.0 / atr_pct)
        if learner is not None:
            for row in learner.summary():
                if (
                    row.get("strategy") == self.strategy_id
                    and row.get("market") == symbol
                    and row.get("direction") == direction
                ):
                    size *= 1.0 + (float(row.get("quality_score", 0.0)) * 0.25)
                    break
        return max(MIN_DYNAMIC_POSITION_SIZE, min(MAX_DYNAMIC_POSITION_SIZE, size))
