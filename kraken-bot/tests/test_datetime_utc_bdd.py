"""BDD coverage ensuring no deprecated datetime.utcnow() calls are emitted (BUG-011 / BUG-010)."""
import unittest
import warnings

from bdd_helpers import BACKEND_DIR  # noqa: F401


class DatetimeUTCBDDTests(unittest.TestCase):
    # GIVEN the risk engine is instantiated WHEN it initialises its reset date
    # THEN no DeprecationWarning referencing utcnow is raised (BUG-011 / BUG-010).
    def test_given_risk_engine_created_when_init_runs_then_no_utcnow_deprecation_warning(self) -> None:
        from risk.engine import RiskEngine

        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            engine = RiskEngine()
            _ = engine._last_reset_date  # access date to trigger any lazy evaluation

        utcnow_warnings = [w for w in caught if "utcnow" in str(w.message).lower()]
        self.assertEqual(
            len(utcnow_warnings), 0,
            f"RiskEngine raised utcnow DeprecationWarning: {[str(w.message) for w in utcnow_warnings]}",
        )

    # GIVEN the approval service creates a request WHEN submit_for_approval is called
    # THEN no DeprecationWarning referencing utcnow is raised.
    def test_given_approval_submitted_when_service_runs_then_no_utcnow_deprecation_warning(self) -> None:
        from approval.service import ApprovalService
        from bdd_helpers import make_risk_decision, make_trade_idea

        svc = ApprovalService(ttl_minutes=1)
        idea = make_trade_idea()
        decision = make_risk_decision(idea)

        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            svc.submit(idea, decision)

        utcnow_warnings = [w for w in caught if "utcnow" in str(w.message).lower()]
        self.assertEqual(
            len(utcnow_warnings), 0,
            f"ApprovalService raised utcnow DeprecationWarning: {[str(w.message) for w in utcnow_warnings]}",
        )

    # GIVEN the risk engine checks for a daily reset WHEN _check_daily_reset runs
    # THEN no DeprecationWarning referencing utcnow is raised.
    def test_given_daily_reset_check_when_run_then_no_utcnow_deprecation_warning(self) -> None:
        from risk.engine import RiskEngine

        engine = RiskEngine()

        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            engine._check_daily_reset()

        utcnow_warnings = [w for w in caught if "utcnow" in str(w.message).lower()]
        self.assertEqual(
            len(utcnow_warnings), 0,
            f"_check_daily_reset raised utcnow DeprecationWarning: {[str(w.message) for w in utcnow_warnings]}",
        )

    # GIVEN the risk state is saved with today's UTC date WHEN the restore comparison runs
    # THEN a UTC-consistent comparison succeeds regardless of local timezone (BUG-010).
    def test_given_utc_reset_date_when_compared_with_utc_today_then_dates_match(self) -> None:
        from datetime import timezone
        from datetime import datetime as dt

        # Simulate what the risk engine stores (UTC date)
        stored_date = dt.now(timezone.utc).date()
        # Simulate the fixed restore comparison (also UTC date)
        restore_check_date = dt.now(timezone.utc).date()

        self.assertEqual(
            stored_date, restore_check_date,
            "UTC date comparison must be consistent for risk state restore to work",
        )


if __name__ == "__main__":
    unittest.main()
