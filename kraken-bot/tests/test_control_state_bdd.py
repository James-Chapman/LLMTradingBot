"""BDD coverage for operator control state."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from control.state import ControlState


class ControlStateBDDTests(unittest.TestCase):
    # GIVEN a new control state WHEN the snapshot is requested THEN combined is selected by default.
    def test_given_new_control_state_when_snapshot_requested_then_combined_strategy_is_selected(self) -> None:
        control = ControlState()

        snapshot = control.snapshot()

        self.assertEqual(snapshot["selected_strategy"], "combined")

    # GIVEN a strategy is selected WHEN the snapshot is requested THEN only that strategy is active.
    def test_given_strategy_selected_when_snapshot_requested_then_selected_strategy_is_active(self) -> None:
        control = ControlState()

        control.select_strategy("llm")

        self.assertEqual(control.selected_strategy_id, "llm")
        self.assertTrue(control.is_strategy_selected("llm"))
        self.assertFalse(control.is_strategy_selected("combined"))
        self.assertEqual(control.snapshot()["selected_strategy"], "llm")


if __name__ == "__main__":
    unittest.main()
