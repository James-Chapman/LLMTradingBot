"""
LLM-powered signal analyser, market briefer, and outcome reflector.

brief_market()         — called whenever new news arrives; produces a
                         market-wide outlook for every watched pair.

analyse_signal()       — called before each trade; uses the latest briefing
                         plus live indicators, portfolio state, and news.

reflect_on_outcomes()  — called hourly; finds patterns in closed trades.
"""
import re
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional, Protocol, runtime_checkable

from config.currency import currency_symbol
from config.settings import settings
from observability.logging import get_logger


@runtime_checkable
class LLMClientProtocol(Protocol):
    """Structural interface satisfied by TransformersClient."""

    @property
    def available(self) -> bool: ...

    @property
    def can_attempt(self) -> bool: ...

    async def probe(self) -> bool: ...

    async def chat(self, messages: list[dict], expect_json: bool = True) -> "dict | str | None": ...

logger = get_logger("llm_analyser")
CURRENCY_SYMBOL = currency_symbol(settings.base_currency)


# Return the current time as a timezone-aware UTC datetime.
def _utc_now() -> datetime:
    """Return a timezone-aware UTC timestamp."""
    return datetime.now(timezone.utc)


# Normalize stored timestamps so age calculations always use aware UTC datetimes.
def _ensure_utc(value: datetime) -> datetime:
    """Return a datetime as timezone-aware UTC."""
    if value.tzinfo is None or value.tzinfo.utcoffset(value) is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)


# Parse a repository ISO timestamp as timezone-aware UTC.
def _parse_utc_datetime(value: str) -> datetime:
    """Return a parsed repository timestamp as timezone-aware UTC."""
    if not value:
        return _utc_now()
    return _ensure_utc(datetime.fromisoformat(value))


# Calculate elapsed whole minutes from a stored context timestamp.
def _age_minutes_since(value: datetime) -> int:
    """Return non-negative whole minutes elapsed since value."""
    return max(0, int((_utc_now() - _ensure_utc(value)).total_seconds() / 60))

# ── System prompts ────────────────────────────────────────────────────────────

_SYSTEM_BRIEFER = (
    "You are a crypto market analyst. "
    "You receive current prices, technical indicators, and breaking news. "
    "Give a concise outlook for each watched trading pair. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)

_SYSTEM_ANALYST = (
    "You are a concise crypto trading analyst. "
    "You receive live market data, technical indicators, a market briefing, "
    "portfolio state, and recent news. "
    "Assess whether the signal is supported or contradicted by the evidence. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)

_SYSTEM_RECOMMENDER = (
    "You are a concise crypto trading decision engine. "
    "You receive live market data, technical indicators, a market briefing, "
    "portfolio state, and recent news. "
    "Recommend exactly one action: long, short, or hold. "
    "Use indicators as context, but do not require indicator consensus. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)

_SYSTEM_REFLECTOR = (
    "You are a quantitative trading coach reviewing paper trade results. "
    "Find one clear pattern and give one concrete, actionable improvement. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)

_INDICATOR_ANALYST = (
    "You are a concise crypto trading analyst. "
    "You receive live market data and technical indicators. "
    "Assess whether the signal is supported or contradicted by the evidence. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)

_NEWS_ANALYST = (
    "You are a concise crypto trading analyst. "
    "You receive recent news articles about the relevant asset. "
    "Assess whether the signal is supported or contradicted by the evidence. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)

_MARKET_ANALYST = (
    "You are a concise crypto trading analyst. "
    "You receive a market briefing. "
    "Assess whether the signal is supported or contradicted by the evidence. "
    "Respond ONLY with a valid JSON object — no prose, no markdown."
)


# ── Result dataclasses ────────────────────────────────────────────────────────

@dataclass
class MarketBriefing:
    """Cached result of a news-triggered market-wide LLM assessment."""
    market_outlooks: Dict[str, Dict]   # market → {bias, score, note}
    overall_sentiment: float           # -1.0 … +1.0
    key_insight: str                   # one sentence summary
    article_count: int                 # number of new articles that triggered this
    generated_at: datetime = field(default_factory=_utc_now)

    # Normalize LLM and legacy persisted outlook payloads at the boundary.
    def __post_init__(self) -> None:
        """Ensure market_outlooks always maps each market to a dict payload."""
        self.market_outlooks = _normalise_market_outlooks(self.market_outlooks)


@dataclass
class SignalAnalysis:
    sentiment: float          # -1.0 (bearish) … +1.0 (bullish)
    confidence_scale: float   # multiply base confidence by this (0.5–2.0); below veto_threshold → trade skipped
    reasoning: str
    llm_used: bool = False


@dataclass
class LLMTradeRecommendation:
    action: str               # long | short | hold
    confidence: float         # 0.0 to 0.95
    sentiment: float          # -1.0 (bearish) to +1.0 (bullish)
    reasoning: str
    llm_used: bool = False


@dataclass
class Reflection:
    pattern: str
    suggestion: str
    insight_confidence: float
    generated_at: datetime = field(default_factory=_utc_now)


def _neutral() -> SignalAnalysis:
    return SignalAnalysis(
        sentiment=0.0, confidence_scale=1.0,
        reasoning="LLM unavailable — no adjustment", llm_used=False,
    )


# ── Shared helpers ────────────────────────────────────────────────────────────

# Return a no-trade recommendation when the LLM cannot provide a usable decision.
def _hold(reason: str = "LLM unavailable — no recommendation") -> LLMTradeRecommendation:
    return LLMTradeRecommendation(
        action="hold",
        confidence=0.0,
        sentiment=0.0,
        reasoning=reason,
        llm_used=False,
    )


_ALIASES: Dict[str, List[str]] = {
    "btc":  ["bitcoin", "btc"],
    "eth":  ["ethereum", "eth"],
    "sol":  ["solana", "sol"],
    "xrp":  ["ripple", "xrp"],
    "ada":  ["cardano", "ada"],
    "link": ["chainlink", "link"],
    "ltc":  ["litecoin", "ltc"],
    "dot":  ["polkadot", "dot"],
    "bnb":  ["binance", "bnb"],
    "avax": ["avalanche", "avax"],
}


def _asset_name(market: str) -> str:
    return market.split("/")[0]


def _normalise_market_key(value: str) -> str:
    """Normalise market labels for matching only; display keeps exact symbols."""
    return re.sub(r"[^a-z0-9]", "", value.lower())


def _match_market_key(raw_key: str, known_markets: List[str]) -> str:
    """Map an LLM-returned market key back to an exact known market symbol."""
    if raw_key in known_markets:
        return raw_key
    wanted = _normalise_market_key(raw_key)
    for market in known_markets:
        if _normalise_market_key(market) == wanted:
            if raw_key != market:
                logger.warning(
                    "LLM market key normalised",
                    extra={"raw_key": raw_key, "matched_market": market},
                )
            return market
    logger.warning("LLM market key did not match watched markets", extra={"raw_key": raw_key})
    return raw_key


# Normalize one LLM outlook entry to the shape expected by strategy consumers.
def _normalise_outlook_value(value: Any) -> Dict[str, Any]:
    """Return a safe market outlook dict with bias, score, and note keys."""
    if isinstance(value, dict):
        try:
            score = float(value.get("score", 0.0) or 0.0)
        except (TypeError, ValueError):
            score = 0.0
        return {
            "bias": str(value.get("bias", "neutral") or "neutral"),
            "score": max(-1.0, min(1.0, score)),
            "note": str(value.get("note", "") or ""),
        }
    if isinstance(value, str):
        return {"bias": value or "neutral", "score": 0.0, "note": ""}
    return {"bias": "neutral", "score": 0.0, "note": ""}


# Normalize all LLM outlook entries and optionally map keys to watched markets.
def _normalise_market_outlooks(
    outlooks: Any,
    known_markets: Optional[List[str]] = None,
) -> Dict[str, Dict[str, Any]]:
    """Return market outlooks in a stable dict-of-dicts shape."""
    if not isinstance(outlooks, dict):
        return {}
    normalised: Dict[str, Dict[str, Any]] = {}
    for raw_key, raw_value in outlooks.items():
        market = _match_market_key(str(raw_key), known_markets) if known_markets else str(raw_key)
        normalised[market] = _normalise_outlook_value(raw_value)
    return normalised


def _relevant_news(market: str, news: List[Dict], max_items: int = 5) -> List[Dict]:
    asset = _asset_name(market).lower()
    keywords = _ALIASES.get(asset, [asset])
    relevant = [
        n for n in news
        if any(kw in (n.get("title", "") + n.get("summary", "")).lower() for kw in keywords)
    ][:max_items]
    if len(relevant) < 3:
        general = [n for n in news if n not in relevant]
        relevant += general[: max_items - len(relevant)]
    return relevant


def _format_news_block(items: List[Dict]) -> str:
    lines = []
    for n in items:
        source  = n.get("source", "?")
        title   = n.get("title", "")
        summary = (n.get("summary", "") or "").strip()
        if summary:
            summary = summary[:120].rstrip() + ("…" if len(summary) > 120 else "")
            lines.append(f"  [{source}] {title} — {summary}")
        else:
            lines.append(f"  [{source}] {title}")
    return "\n".join(lines) if lines else "  No recent news"


def _format_indicators(ind: Dict[str, Any]) -> str:
    lines = []
    changes = ind.get("price_changes", {})
    if changes:
        lines.append("  Change — " + " | ".join(f"{k}: {v:+.2f}%" for k, v in sorted(changes.items())))
    rsi = ind.get("rsi_14")
    if rsi is not None:
        lines.append(f"  RSI(14): {rsi} — {ind.get('rsi_signal', '')}")
    e9, e21 = ind.get("ema9"), ind.get("ema21")
    if e9 and e21:
        lines.append(f"  EMA9/21: {e9:,.2f} / {e21:,.2f} — {ind.get('ema_cross', '')} crossover")
    bb = ind.get("bb")
    if bb:
        lines.append(
            f"  BB: pos {bb['position']}% of band | "
            f"upper {bb['upper']:,.2f} lower {bb['lower']:,.2f} | width {bb['width_pct']}%"
        )
    m = ind.get("macd")
    if m:
        hist_str = f" | histogram {m['histogram']:+.4f}" if "histogram" in m else ""
        sig_str  = f" | signal {m['signal']:+.4f} ({m.get('signal_bias','?')})" if "signal" in m else ""
        lines.append(f"  MACD: {m['line']:+.4f} ({m['bias']}){sig_str}{hist_str}")
    stoch = ind.get("stoch")
    if stoch:
        lines.append(f"  Stoch: %K {stoch['k']} / %D {stoch['d']} — {stoch['bias']}")
    wr = ind.get("williams_r")
    if wr is not None:
        lines.append(f"  Williams %R: {wr} — {ind.get('williams_r_signal', '')}")
    atr_pct = ind.get("atr_pct")
    if atr_pct is not None:
        lines.append(f"  ATR: {ind.get('atr', '?')} ({atr_pct:.3f}% of price)")
    return "\n".join(lines) if lines else "  Insufficient price history for indicators"


def _format_positions(positions: List[Any]) -> str:
    if not positions:
        return "  None"
    lines = []
    for p in positions:
        direction = "LONG" if getattr(p, "size", 0) > 0 else "SHORT"
        pnl = getattr(p, "unrealized_pnl", 0.0)
        lines.append(
            f"  {getattr(p, 'market', '?')} {direction} "
            f"× {abs(getattr(p, 'size', 0)):.6f} "
            f"@ {CURRENCY_SYMBOL}{getattr(p, 'avg_price', 0):,.2f} "
            f"unrealised {CURRENCY_SYMBOL}{pnl:+.2f}"
        )
    return "\n".join(lines)


# ── Analyser ──────────────────────────────────────────────────────────────────

class LLMAnalyser:
    def __init__(self, client: LLMClientProtocol):
        self._llm = client
        self.latest_reflection: Optional[Reflection] = None
        self.latest_briefing:   Optional[MarketBriefing] = None
        self._repo = None  # injected after init

    # Return whether the LLM client is ready for a normal or half-open attempt.
    def _can_attempt_llm(self) -> bool:
        """Return True when the client should be allowed to make an LLM call."""
        return bool(getattr(self._llm, "can_attempt", getattr(self._llm, "available", False)))

    # ── Repo wiring ────────────────────────────────────────────────────────

    def set_repo(self, repo) -> None:
        """Wire in the Repository.  Call once at startup."""
        self._repo = repo

    def load_from_db(self) -> None:
        """Restore latest briefing and reflection from DB.  Call once at startup."""
        if self._repo is None:
            return
        try:
            b = self._repo.load_latest_llm_briefing()
            if b:
                self.latest_briefing = MarketBriefing(
                    market_outlooks=_normalise_market_outlooks(b["market_outlooks"]),
                    overall_sentiment=b["overall_sentiment"],
                    key_insight=b["key_insight"],
                    article_count=b["article_count"],
                    generated_at=_parse_utc_datetime(b["generated_at"]),
                )
                logger.info("LLM briefing restored from DB", extra={"insight": b["key_insight"]})
        except Exception as exc:
            logger.warning("Could not restore LLM briefing from DB: %s", exc)

        try:
            r = self._repo.load_latest_llm_reflection()
            if r:
                self.latest_reflection = Reflection(
                    pattern=r["pattern"],
                    suggestion=r["suggestion"],
                    insight_confidence=r["insight_confidence"],
                    generated_at=_parse_utc_datetime(r["generated_at"]),
                )
                logger.info("LLM reflection restored from DB", extra={"pattern": r["pattern"]})
        except Exception as exc:
            logger.warning("Could not restore LLM reflection from DB: %s", exc)

    # ── Market briefing ───────────────────────────────────────────────────

    async def brief_market(
        self,
        new_articles: List[Dict],
        market_data: Dict[str, Dict],   # market → {price, indicators}
    ) -> Optional[MarketBriefing]:
        """Called when new news arrives. Produces a market-wide outlook.

        market_data example:
            {"BTC/EUR": {"price": 85420.0, "indicators": {...}}}
        """
        if not self._can_attempt_llm() or not market_data:
            return None

        # ── Price + indicator summary for each market ─────────────────────
        price_lines = []
        for market, data in market_data.items():
            price = data.get("price", 0)
            ind   = data.get("indicators", {})
            changes = ind.get("price_changes", {})
            chg_str = " | ".join(f"{k}: {v:+.2f}%" for k, v in sorted(changes.items())) if changes else "—"
            rsi      = ind.get("rsi_14", "?")
            cross    = ind.get("ema_cross", "?")
            bb_pos   = ind.get("bb", {}).get("position", "?") if ind.get("bb") else "?"
            macd_b   = (ind.get("macd") or {}).get("bias", "?")
            stoch_k  = (ind.get("stoch") or {}).get("k", "?")
            wr       = ind.get("williams_r", "?")
            price_lines.append(
                f"  {market}: {CURRENCY_SYMBOL}{price:,.2f} | {chg_str} | RSI {rsi} | EMA {cross} "
                f"| BB {bb_pos}% | MACD {macd_b} | Stoch {stoch_k} | WR {wr}"
            )

        markets_list = ", ".join(market_data.keys())
        news_block   = _format_news_block(new_articles[:8])

        user_msg = (
            f"Breaking news update — {len(new_articles)} new article(s).\n\n"
            f"Current market prices:\n" + "\n".join(price_lines) + "\n\n"
            f"New articles:\n{news_block}\n\n"
            f"Assess each pair: {markets_list}\n\n"
            f"Use exactly these market symbols as JSON object keys: {markets_list}\n\n"
            f"Return JSON with exactly these keys:\n"
            f"  market_outlooks   — object: each market key maps to "
            f"{{\"bias\": \"bullish|bearish|neutral\", \"score\": float -1 to 1, \"note\": max 12 words}}\n"
            f"  overall_sentiment — float -1.0 to 1.0\n"
            f"  key_insight       — string, max 20 words, most important takeaway"
        )

        messages = [
            {"role": "system", "content": _SYSTEM_BRIEFER},
            {"role": "user",   "content": user_msg},
        ]

        result = await self._llm.chat(messages, expect_json=True)
        if result is None:
            return None

        try:
            outlooks = result.get("market_outlooks", {})
            known_markets = list(market_data.keys())
            # Normalise keys and nested values so downstream consumers can read safely.
            normalised = _normalise_market_outlooks(outlooks, known_markets)

            briefing = MarketBriefing(
                market_outlooks=normalised,
                overall_sentiment=max(-1.0, min(1.0, float(result.get("overall_sentiment", 0.0)))),
                key_insight=str(result.get("key_insight", "")),
                article_count=len(new_articles),
            )
            self.latest_briefing = briefing
            logger.info("LLM market briefing completed", extra={
                "markets": list(normalised.keys()),
                "overall": briefing.overall_sentiment,
                "insight": briefing.key_insight,
            })
            # Persist so briefing context survives restarts
            if self._repo is not None:
                try:
                    briefed_ids = [n["id"] for n in new_articles if "id" in n]
                    self._repo.save_llm_briefing(
                        key_insight=briefing.key_insight,
                        overall_sentiment=briefing.overall_sentiment,
                        market_outlooks=briefing.market_outlooks,
                        article_count=briefing.article_count,
                        briefed_news_ids=briefed_ids,
                        generated_at=briefing.generated_at,
                    )
                except Exception as exc:
                    logger.warning("Could not persist LLM briefing: %s", exc)
            return briefing
        except Exception as e:
            logger.warning(f"LLM briefing parse error: {e} — raw: {result}")
            return None

    # ── Signal analysis ───────────────────────────────────────────────────

    async def analyse_signal(
        self,
        market: str,
        direction: str,
        momentum_pct: float,
        base_confidence: float,
        news: List[Dict],
        *,
        current_price: float = 0.0,
        indicators: Optional[Dict[str, Any]] = None,
        equity: float = 0.0,
        cash: float = 0.0,
        open_positions: Optional[List[Any]] = None,
    ) -> SignalAnalysis:
        """Full-context signal analysis, optionally enriched by latest briefing."""
        if not self._can_attempt_llm():
            return _neutral()

        ind       = indicators or {}
        positions = open_positions or []

        news_items = _relevant_news(market, news)
        news_block = _format_news_block(news_items)
        ind_block  = _format_indicators(ind)
        pos_block  = _format_positions(positions)
        exposure   = round((equity - cash) / equity * 100, 1) if equity > 0 else 0.0

        # ── Inject latest briefing context if available ───────────────────
        briefing_block = ""
        if self.latest_briefing:
            b = self.latest_briefing
            raw_outlook = b.market_outlooks.get(market)
            age_min = _age_minutes_since(b.generated_at)
            briefing_block = (
                f"\nLatest market briefing ({age_min}m ago, {b.article_count} new article(s)):\n"
                f"  Key insight: {b.key_insight}\n"
                f"  Overall sentiment: {b.overall_sentiment:+.2f}\n"
            )
            if raw_outlook is not None:
                outlook = _normalise_outlook_value(raw_outlook)
                briefing_block += (
                    f"  {market} outlook: {outlook.get('bias','?')} "
                    f"(score {outlook.get('score', 0):+.2f}) — {outlook.get('note','')}\n"
                )

        # ── Inject latest reflection so the LLM acts on its own past advice ──
        reflection_block = ""
        if self.latest_reflection:
            r = self.latest_reflection
            age_min_r = _age_minutes_since(r.generated_at)
            reflection_block = (
                f"\nYour most recent self-reflection ({age_min_r}m ago, "
                f"confidence {r.insight_confidence:.0%}):\n"
                f"  Pattern:    {r.pattern}\n"
                f"  Suggestion: {r.suggestion}\n"
                f"Apply this advice when assessing the current signal.\n"
            )

        user_msg = (
            f"Signal: {market} {direction.upper()}\n"
            f"Confidence: {base_confidence:.0%} | Momentum: {momentum_pct:+.2f}%"
            + (f" | Price: {CURRENCY_SYMBOL}{current_price:,.2f}" if current_price else "") + "\n"
            + briefing_block
            + reflection_block + "\n"
            f"Technical indicators (30s ticks):\n{ind_block}\n\n"
            f"Portfolio:\n"
            f"  Equity: {CURRENCY_SYMBOL}{equity:,.2f} | "
            f"Cash: {CURRENCY_SYMBOL}{cash:,.2f} | Exposure: {exposure}%\n"
            f"Open positions:\n{pos_block}\n\n"
            f"Recent news ({_asset_name(market)}):\n{news_block}\n\n"
            f"Return JSON with exactly these keys:\n"
            f"  sentiment        — float -1.0 to 1.0\n"
            f"  confidence_scale — float 0.5 to 2.0 (multiplied by base confidence; "
            f"use >1.0 when evidence strongly supports the signal, <1.0 when it contradicts, "
            f"<0.7 only when you would actively oppose the trade)\n"
            f"  reasoning        — string, max 20 words"
        )

        messages = [
            {"role": "system", "content": _SYSTEM_ANALYST},
            {"role": "user",   "content": user_msg},
        ]

        result = await self._llm.chat(messages, expect_json=True)
        if result is None:
            return _neutral()

        try:
            sentiment = max(-1.0, min(1.0, float(result.get("sentiment", 0.0))))
            scale     = max(0.5,  min(2.0,  float(result.get("confidence_scale", 1.0))))
            reasoning = str(result.get("reasoning", ""))
            logger.info("LLM signal analysis", extra={
                "market": market, "direction": direction,
                "sentiment": sentiment, "scale": scale, "reasoning": reasoning,
            })
            return SignalAnalysis(
                sentiment=sentiment, confidence_scale=scale,
                reasoning=reasoning, llm_used=True,
            )
        except Exception as e:
            logger.warning(f"LLM response parse error: {e} — raw: {result}")
            return _neutral()

    async def recommend_trade(
        self,
        *,
        market: str,
        current_price: float,
        previous_price: float,
        indicators: Optional[Dict[str, Any]],
        news: List[Dict],
        equity: float = 0.0,
        cash: float = 0.0,
        open_positions: Optional[List[Any]] = None,
    ) -> LLMTradeRecommendation:
        """Ask the LLM to independently recommend long, short, or hold."""
        if not self._can_attempt_llm():
            return _hold()

        ind = indicators or {}
        positions = open_positions or []
        momentum_pct = (
            (current_price - previous_price) / previous_price * 100
            if previous_price else 0.0
        )

        news_items = _relevant_news(market, news)
        news_block = _format_news_block(news_items)
        ind_block = _format_indicators(ind)
        pos_block = _format_positions(positions)
        exposure = round((equity - cash) / equity * 100, 1) if equity > 0 else 0.0

        briefing_block = ""
        if self.latest_briefing:
            b = self.latest_briefing
            raw_outlook = b.market_outlooks.get(market)
            age_min = _age_minutes_since(b.generated_at)
            briefing_block = (
                f"\nLatest market briefing ({age_min}m ago, {b.article_count} new article(s)):\n"
                f"  Key insight: {b.key_insight}\n"
                f"  Overall sentiment: {b.overall_sentiment:+.2f}\n"
            )
            if raw_outlook is not None:
                outlook = _normalise_outlook_value(raw_outlook)
                briefing_block += (
                    f"  {market} outlook: {outlook.get('bias','?')} "
                    f"(score {outlook.get('score', 0):+.2f}) — {outlook.get('note','')}\n"
                )

        reflection_block = ""
        if self.latest_reflection:
            r = self.latest_reflection
            age_min_r = _age_minutes_since(r.generated_at)
            reflection_block = (
                f"\nYour most recent self-reflection ({age_min_r}m ago, "
                f"confidence {r.insight_confidence:.0%}):\n"
                f"  Pattern:    {r.pattern}\n"
                f"  Suggestion: {r.suggestion}\n"
                f"Apply this advice when choosing the current action.\n"
            )

        user_msg = (
            f"Market: {market}\n"
            f"Price: {CURRENCY_SYMBOL}{current_price:,.2f} | "
            f"Previous: {CURRENCY_SYMBOL}{previous_price:,.2f} "
            f"| Momentum: {momentum_pct:+.2f}%\n"
            + briefing_block
            + reflection_block + "\n"
            f"Technical indicators (context only, do not require consensus):\n{ind_block}\n\n"
            f"Portfolio:\n"
            f"  Equity: {CURRENCY_SYMBOL}{equity:,.2f} | "
            f"Cash: {CURRENCY_SYMBOL}{cash:,.2f} | Exposure: {exposure}%\n"
            f"Open positions:\n{pos_block}\n\n"
            f"Recent news ({_asset_name(market)}):\n{news_block}\n\n"
            f"Return JSON with exactly these keys:\n"
            f"  action     — string: long, short, or hold\n"
            f"  confidence — float 0.0 to 0.95, only high when the trade is actionable\n"
            f"  sentiment  — float -1.0 to 1.0\n"
            f"  reasoning  — string, max 20 words"
        )

        messages = [
            {"role": "system", "content": _SYSTEM_RECOMMENDER},
            {"role": "user", "content": user_msg},
        ]

        result = await self._llm.chat(messages, expect_json=True)
        if result is None:
            return _hold()

        try:
            raw_action = str(result.get("action", "hold")).strip().lower()
            action_aliases = {
                "buy": "long",
                "bullish": "long",
                "sell": "short",
                "bearish": "short",
                "none": "hold",
                "no_trade": "hold",
                "no trade": "hold",
            }
            action = action_aliases.get(raw_action, raw_action)
            if action not in {"long", "short", "hold"}:
                action = "hold"
            confidence = max(0.0, min(0.95, float(result.get("confidence", 0.0))))
            sentiment = max(-1.0, min(1.0, float(result.get("sentiment", 0.0))))
            reasoning = str(result.get("reasoning", ""))
            logger.info("LLM trade recommendation", extra={
                "market": market,
                "action": action,
                "confidence": confidence,
                "sentiment": sentiment,
                "reasoning": reasoning,
            })
            return LLMTradeRecommendation(
                action=action,
                confidence=confidence,
                sentiment=sentiment,
                reasoning=reasoning,
                llm_used=True,
            )
        except Exception as e:
            logger.warning(f"LLM recommendation parse error: {e} — raw: {result}")
            return _hold("LLM response could not be parsed")

    # ── Outcome reflection ────────────────────────────────────────────────

    async def reflect_on_outcomes(self, outcomes: List[Dict]) -> Optional[Reflection]:
        """Find patterns in recent closed trades. Expects a list of dicts.

        Each dict may carry an `indicators` key with the full indicator snapshot
        stored at entry time (joined from trade_ideas). These are summarised per
        trade so the LLM can find indicator-level patterns (e.g. "losing longs
        all had RSI > 70 at entry").
        """
        if not self._can_attempt_llm() or len(outcomes) < 5:
            return None

        sample = outcomes[:20]
        pnls   = [float(o.get("pnl", 0)) for o in sample]
        wins   = [p for p in pnls if p > 0]
        losses = [p for p in pnls if p < 0]
        win_rate  = len(wins)   / len(pnls)   if pnls   else 0
        avg_win   = sum(wins)   / len(wins)   if wins   else 0
        avg_loss  = sum(losses) / len(losses) if losses else 0
        total_pnl = sum(pnls)

        trade_lines = []
        for o in sample:
            ind = o.get("indicators") or {}
            # Compact indicator summary — only include fields that are present
            ind_parts = []
            rsi = ind.get("rsi_14")
            if rsi is not None:
                ind_parts.append(f"RSI {rsi}")
            ema = ind.get("ema_cross")
            if ema:
                ind_parts.append(f"EMA {ema}")
            macd = (ind.get("macd") or {}).get("bias")
            if macd:
                ind_parts.append(f"MACD {macd}")
            stoch = (ind.get("stoch") or {}).get("k")
            if stoch is not None:
                ind_parts.append(f"Stoch {stoch}")
            wr = ind.get("williams_r")
            if wr is not None:
                ind_parts.append(f"WR {wr}")
            atr = ind.get("atr_pct")
            if atr is not None:
                ind_parts.append(f"ATR {atr:.2f}%")
            ind_str = " | ".join(ind_parts) if ind_parts else "no indicators"

            trade_lines.append(
                f"  {o.get('market','?')} {str(o.get('direction','')).upper()}"
                f" | entry {CURRENCY_SYMBOL}{float(o.get('entry_price', 0)):.2f}"
                f" exit {CURRENCY_SYMBOL}{float(o.get('exit_price', 0)):.2f}"
                f" | P&L {CURRENCY_SYMBOL}{float(o.get('pnl', 0)):+.2f}"
                f" ({float(o.get('pnl_pct', 0)):+.1%})"
                f" conf {float(o.get('confidence') or 0):.0%}"
                f" | {o.get('exit_reason', '?')}"
                f" | at entry: {ind_str}"
            )

        trades_block = "\n".join(trade_lines)

        user_msg = (
            f"Performance summary ({len(sample)} trades):\n"
            f"  Win rate: {win_rate:.0%} | Total P&L: {CURRENCY_SYMBOL}{total_pnl:+.2f}\n"
            f"  Avg win: {CURRENCY_SYMBOL}{avg_win:+.2f} | "
            f"Avg loss: {CURRENCY_SYMBOL}{avg_loss:+.2f}\n\n"
            f"Individual trades with entry-time indicators:\n{trades_block}\n\n"
            f"Look for indicator-level patterns (e.g. RSI levels, EMA direction, MACD bias) "
            f"that correlate with wins or losses. Give one specific, actionable finding.\n\n"
            f"Return JSON with exactly these keys:\n"
            f"  pattern            — string, one pattern observed (max 25 words)\n"
            f"  suggestion         — string, one concrete adjustment (max 25 words)\n"
            f"  insight_confidence — float 0.0 to 1.0"
        )

        messages = [
            {"role": "system", "content": _SYSTEM_REFLECTOR},
            {"role": "user",   "content": user_msg},
        ]

        result = await self._llm.chat(messages, expect_json=True)
        if result is None:
            return None

        try:
            reflection = Reflection(
                pattern=str(result.get("pattern", "")),
                suggestion=str(result.get("suggestion", "")),
                insight_confidence=max(0.0, min(1.0, float(result.get("insight_confidence", 0.5)))),
            )
            self.latest_reflection = reflection
            logger.info("LLM reflection", extra={
                "pattern": reflection.pattern,
                "suggestion": reflection.suggestion,
            })
            # Persist so reflection survives restarts
            if self._repo is not None:
                try:
                    self._repo.save_llm_reflection(
                        pattern=reflection.pattern,
                        suggestion=reflection.suggestion,
                        insight_confidence=reflection.insight_confidence,
                        generated_at=reflection.generated_at,
                    )
                except Exception as exc:
                    logger.warning("Could not persist LLM reflection: %s", exc)
            return reflection
        except Exception as e:
            logger.warning(f"LLM reflection parse error: {e}")
            return None
