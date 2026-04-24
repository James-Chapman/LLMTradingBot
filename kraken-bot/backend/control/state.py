"""
Operator control state — emergency stop, market toggles, and strategy selection.

State is written to the control_state DB table on every change so it
survives restarts.  Call `control.set_repo(repo)` and
`control.load_from_db()` once during startup.
"""
from datetime import datetime
from threading import Lock
from typing import Set

from observability.logging import get_logger

logger = get_logger("control")
DEFAULT_SELECTED_STRATEGY = "combined"
LEGACY_STRATEGY_IDS = {
    "basic_trend": "combined",
    "llm_only": "llm",
}


class ControlState:
    def __init__(self) -> None:
        self._lock = Lock()
        self._emergency_stop: bool = False
        self._stop_timestamp: datetime | None = None
        self._disabled_markets: Set[str] = set()
        self._disabled_strategies: Set[str] = set()
        self._selected_strategy_id: str = DEFAULT_SELECTED_STRATEGY
        self._live_markets: Set[str] = set()
        self._repo = None  # injected after init

    # ── Repo wiring ────────────────────────────────────────────────────────

    def set_repo(self, repo) -> None:
        """Wire in the Repository.  Call once at startup."""
        self._repo = repo

    def load_from_db(self) -> None:
        """Restore persisted toggles from DB.  Call once at startup after set_repo()."""
        if self._repo is None:
            return
        try:
            state = self._repo.load_control_state()
            if state is None:
                return
            with self._lock:
                self._emergency_stop = state.get("emergency_stop", False)
                self._disabled_markets = set(state.get("disabled_markets", []))
                self._disabled_strategies = set(state.get("disabled_strategies", []))
                selected = state.get("selected_strategy") or DEFAULT_SELECTED_STRATEGY
                self._selected_strategy_id = LEGACY_STRATEGY_IDS.get(selected, selected)
                self._live_markets = set(state.get("live_markets", []))
            if self._emergency_stop:
                logger.warning("Control state restored — emergency stop is ACTIVE")
            if self._disabled_markets:
                logger.info("Control state restored — disabled markets: %s",
                            sorted(self._disabled_markets))
            if self._disabled_strategies:
                logger.info("Control state restored — disabled strategies: %s",
                            sorted(self._disabled_strategies))
        except Exception as exc:
            logger.warning("Could not restore control state from DB: %s", exc)

    def _persist(self) -> None:
        if self._repo is None:
            return
        try:
            self._repo.save_control_state(
                emergency_stop=self._emergency_stop,
                disabled_markets=sorted(self._disabled_markets),
                disabled_strategies=sorted(self._disabled_strategies),
                live_markets=sorted(self._live_markets),
                selected_strategy=self._selected_strategy_id,
            )
        except Exception:
            pass  # non-fatal

    # ── Emergency stop ─────────────────────────────────────────────────────

    @property
    def emergency_stop(self) -> bool:
        return self._emergency_stop

    def activate_stop(self) -> None:
        with self._lock:
            self._emergency_stop = True
            self._stop_timestamp = datetime.utcnow()
        logger.warning("Emergency stop ACTIVATED")
        self._persist()

    def resume(self) -> None:
        with self._lock:
            self._emergency_stop = False
            self._stop_timestamp = None
        logger.info("Emergency stop cleared — bot resumed")
        self._persist()

    # ── Market toggles ─────────────────────────────────────────────────────

    def disable_market(self, market: str) -> None:
        with self._lock:
            self._disabled_markets.add(market)
        logger.info("Market disabled", extra={"market": market})
        self._persist()

    def enable_market(self, market: str) -> None:
        with self._lock:
            self._disabled_markets.discard(market)
        logger.info("Market enabled", extra={"market": market})
        self._persist()

    def is_market_enabled(self, market: str) -> bool:
        return market not in self._disabled_markets

    def set_market_live(self, market: str, live: bool) -> None:
        """Set whether a market routes to live execution."""
        with self._lock:
            if live:
                self._live_markets.add(market)
            else:
                self._live_markets.discard(market)
        self._persist()

    def is_market_live(self, market: str) -> bool:
        return market in self._live_markets

    # ── Strategy selection ─────────────────────────────────────────────────

    def disable_strategy(self, strategy_id: str) -> None:
        with self._lock:
            self._disabled_strategies.add(strategy_id)
        logger.info("Strategy disabled", extra={"strategy_id": strategy_id})
        self._persist()

    def enable_strategy(self, strategy_id: str) -> None:
        with self._lock:
            self._disabled_strategies.discard(strategy_id)
        logger.info("Strategy enabled", extra={"strategy_id": strategy_id})
        self._persist()

    def is_strategy_enabled(self, strategy_id: str) -> bool:
        return strategy_id not in self._disabled_strategies

    @property
    def selected_strategy_id(self) -> str:
        return self._selected_strategy_id

    def select_strategy(self, strategy_id: str) -> None:
        """Select the single strategy used by the signal loop."""
        canonical_id = LEGACY_STRATEGY_IDS.get(strategy_id, strategy_id)
        with self._lock:
            self._selected_strategy_id = canonical_id
        logger.info("Strategy selected", extra={"strategy_id": canonical_id})
        self._persist()

    def is_strategy_selected(self, strategy_id: str) -> bool:
        """Return whether the supplied strategy is the active strategy."""
        canonical_id = LEGACY_STRATEGY_IDS.get(strategy_id, strategy_id)
        return canonical_id == self._selected_strategy_id

    # ── Snapshot ───────────────────────────────────────────────────────────

    def snapshot(self) -> dict:
        return {
            "emergency_stop": self._emergency_stop,
            "stop_since": self._stop_timestamp.isoformat() if self._stop_timestamp else None,
            "disabled_markets": sorted(self._disabled_markets),
            "disabled_strategies": sorted(self._disabled_strategies),
            "selected_strategy": self._selected_strategy_id,
            "live_markets": sorted(self._live_markets),
        }
