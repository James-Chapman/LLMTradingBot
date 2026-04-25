"""BDD coverage for risk engine daily_loss persistence across restarts."""
import sys
import unittest
from datetime import date, datetime, timezone
from pathlib import Path

BACKEND_DIR = Path(__file__).resolve().parents[1] / "backend"
if str(BACKEND_DIR) not in sys.path:
    sys.path.insert(0, str(BACKEND_DIR))

from storage.database import init_database  # noqa: E402
from storage.repository import Repository   # noqa: E402
from risk.engine import RiskEngine          # noqa: E402
from risk.persistence import record_trade_result_and_persist  # noqa: E402


def _fresh_repo() -> Repository:
    """Initialise an in-memory DB and return a fresh repository."""
    init_database("sqlite://")
    return Repository()


class RiskStatePersistenceBDDTests(unittest.TestCase):

    # GIVEN a risk engine that recorded losses WHEN save_risk_state is called
    # THEN the state can be reloaded by a new engine instance.
    def test_given_recorded_loss_when_state_saved_then_new_engine_restores_it(self) -> None:
        repo = _fresh_repo()
        engine = RiskEngine()
        engine.record_trade_result(-30.0)   # accumulate a loss

        repo.save_risk_state(
            daily_loss=engine.daily_loss,
            daily_start_equity=engine.daily_start_equity,
            last_reset_date=engine._last_reset_date,
        )

        # Simulate restart — new engine loads from DB
        new_engine = RiskEngine()
        state = repo.load_risk_state()
        if state and state["last_reset_date"] == datetime.now(timezone.utc).date():
            new_engine.daily_loss = state["daily_loss"]
            new_engine.daily_start_equity = state["daily_start_equity"]

        self.assertAlmostEqual(new_engine.daily_loss, 30.0, places=6)

    # GIVEN no prior risk state in DB WHEN load_risk_state is called THEN None is returned.
    def test_given_no_prior_state_when_load_called_then_none_returned(self) -> None:
        repo = _fresh_repo()
        state = repo.load_risk_state()
        self.assertIsNone(state)

    # GIVEN a risk state saved on a previous day WHEN a new engine checks the date
    # THEN the stale state is ignored (daily reset applies).
    def test_given_stale_state_when_date_differs_then_state_is_not_restored(self) -> None:
        repo = _fresh_repo()
        yesterday = date(2000, 1, 1)   # clearly in the past
        repo.save_risk_state(
            daily_loss=99.0,
            daily_start_equity=500.0,
            last_reset_date=yesterday,
        )

        state = repo.load_risk_state()
        today = datetime.now(timezone.utc).date()
        restored = state and state["last_reset_date"] == today

        self.assertFalse(restored, "Stale state from a previous day must not be restored")

    # GIVEN consecutive saves WHEN load_risk_state is called THEN the most recent values are returned.
    def test_given_multiple_saves_when_loaded_then_latest_values_returned(self) -> None:
        repo = _fresh_repo()
        repo.save_risk_state(daily_loss=10.0, daily_start_equity=500.0,
                             last_reset_date=datetime.now(timezone.utc).date())
        repo.save_risk_state(daily_loss=25.0, daily_start_equity=500.0,
                             last_reset_date=datetime.now(timezone.utc).date())

        state = repo.load_risk_state()

        self.assertIsNotNone(state)
        self.assertAlmostEqual(state["daily_loss"], 25.0, places=6)

    # GIVEN a losing realised trade WHEN the bot records the result
    # THEN the updated daily risk state is persisted immediately.
    def test_given_losing_trade_when_result_recorded_then_risk_state_is_saved(self) -> None:
        repo = _fresh_repo()
        engine = RiskEngine()

        record_trade_result_and_persist(engine, repo, -12.5)

        state = repo.load_risk_state()
        self.assertIsNotNone(state)
        self.assertAlmostEqual(state["daily_loss"], 12.5, places=6)
        self.assertAlmostEqual(state["daily_start_equity"], engine.daily_start_equity, places=6)


if __name__ == "__main__":
    unittest.main()
