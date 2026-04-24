"""
Performance learner — adjusts signal confidence based on historical trade outcomes.

Tracks win rate per (strategy_id, market, direction) using exponential recency
weighting so recent trades count more than old ones.  Requires at least
MIN_SAMPLES outcomes before applying any adjustment.
"""
from collections import defaultdict
from typing import Dict, Tuple

from observability.logging import get_logger

logger = get_logger("learner")

MIN_SAMPLES = 5       # minimum outcomes before adjustments kick in
DECAY = 0.92          # weight of each older trade relative to the next newer one
                      # e.g. most-recent trade weight = 1.0, previous = 0.92, ...


class _Stats:
    __slots__ = (
        "weighted_wins",
        "weighted_total",
        "raw_count",
        "win_pnl_total",
        "win_count",
        "loss_pnl_total",
        "loss_count",
    )

    def __init__(self):
        self.weighted_wins = 0.0
        self.weighted_total = 0.0
        self.raw_count = 0
        self.win_pnl_total = 0.0
        self.win_count = 0
        self.loss_pnl_total = 0.0
        self.loss_count = 0

    def record_pnl(self, pnl: float) -> None:
        """Record raw P&L magnitude for average win/loss calculations."""
        if pnl > 0:
            self.win_pnl_total += pnl
            self.win_count += 1
        elif pnl < 0:
            self.loss_pnl_total += pnl
            self.loss_count += 1

    def mean_win_pnl(self) -> float:
        """Return average winning P&L, or 0 when no wins exist."""
        return self.win_pnl_total / self.win_count if self.win_count else 0.0

    def mean_loss_pnl(self) -> float:
        """Return average losing P&L, or 0 when no losses exist."""
        return self.loss_pnl_total / self.loss_count if self.loss_count else 0.0

    def quality_score(self) -> float:
        """Return a magnitude-aware score in roughly the -1 to +1 range."""
        win_rate = self.weighted_wins / self.weighted_total if self.weighted_total else 0.5
        mean_win = self.mean_win_pnl()
        mean_loss = self.mean_loss_pnl()
        expectancy = (win_rate * mean_win) + ((1.0 - win_rate) * mean_loss)
        normaliser = max(abs(mean_win), abs(mean_loss), 1.0)
        return max(-1.0, min(1.0, expectancy / normaliser))


Key = Tuple[str, str, str]  # (strategy_id, market, direction)


class PerformanceLearner:
    def __init__(self):
        self._stats: Dict[Key, _Stats] = defaultdict(_Stats)

    # ── Population ────────────────────────────────────────────────────────

    def load_from_outcomes(self, outcomes) -> None:
        """Seed from a list of SignalOutcomeModel rows (newest-first from DB)."""
        # Group by key so we can apply decay within each group
        grouped: Dict[Key, list] = defaultdict(list)
        for o in outcomes:
            key = (o.strategy_id, o.market, o.direction)
            grouped[key].append(o.pnl)  # already newest-first

        for key, pnls in grouped.items():
            stats = self._stats[key]
            weight = 1.0
            for pnl in pnls:
                stats.weighted_total += weight
                if pnl > 0:
                    stats.weighted_wins += weight
                stats.record_pnl(pnl)
                stats.raw_count += 1
                weight *= DECAY

        if grouped:
            logger.info("Learner seeded from DB", extra={
                "keys": len(grouped),
                "total_outcomes": sum(len(v) for v in grouped.values()),
            })

    def record_outcome(self, strategy_id: str, market: str, direction: str,
                       pnl: float) -> None:
        """Record a new outcome incrementally (does not require a DB read)."""
        key = (strategy_id, market, direction)
        stats = self._stats[key]

        # Decay all existing weights before adding the new trade at weight=1
        stats.weighted_wins *= DECAY
        stats.weighted_total *= DECAY

        stats.weighted_total += 1.0
        if pnl > 0:
            stats.weighted_wins += 1.0
        stats.record_pnl(pnl)
        stats.raw_count += 1

    # ── Adjustment ────────────────────────────────────────────────────────

    def adjust_confidence(self, strategy_id: str, market: str, direction: str,
                          base_confidence: float) -> float:
        """Return confidence adjusted by historical win rate for this signal type.

        Scale factor:
          win_rate = 0%  → × 0.50  (strong suppression)
          win_rate = 50% → × 1.00  (no change — baseline)
          win_rate = 100%→ × 1.50  (boost, capped at 0.95)
        """
        key = (strategy_id, market, direction)
        stats = self._stats.get(key)
        if stats is None or stats.raw_count < MIN_SAMPLES:
            return base_confidence

        scale = 1.0 + (stats.quality_score() * 0.5)
        adjusted = min(0.95, base_confidence * scale)

        logger.debug("Confidence adjusted", extra={
            "key": key, "quality_score": round(stats.quality_score(), 3),
            "base": base_confidence, "adjusted": adjusted,
        })
        return adjusted

    def summary(self) -> list:
        """Dashboard-friendly list of per-key stats."""
        rows = []
        for (sid, mkt, d), s in self._stats.items():
            if s.raw_count == 0:
                continue
            wr = s.weighted_wins / s.weighted_total if s.weighted_total else 0.0
            rows.append({
                "strategy": sid, "market": mkt, "direction": d,
                "trades": s.raw_count, "win_rate": round(wr, 3),
                "mean_win_pnl": round(s.mean_win_pnl(), 2),
                "mean_loss_pnl": round(s.mean_loss_pnl(), 2),
                "quality_score": round(s.quality_score(), 3),
            })
        return sorted(rows, key=lambda r: r["market"])
