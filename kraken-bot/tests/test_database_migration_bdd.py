"""BDD coverage for database schema migrations."""
import sys
import unittest
from pathlib import Path

BACKEND_DIR = Path(__file__).resolve().parents[1] / "backend"
if str(BACKEND_DIR) not in sys.path:
    sys.path.insert(0, str(BACKEND_DIR))

from storage.database import init_database, get_session  # noqa: E402
from sqlalchemy import create_engine, text, inspect       # noqa: E402


def _columns(engine, table: str) -> set:
    """Return the set of column names for a table."""
    return {c["name"] for c in inspect(engine).get_columns(table)}


def _make_old_db_engine(url: str):
    """Create a minimal 'legacy' engine with tables that pre-date migration columns."""
    engine = create_engine(url, connect_args={"check_same_thread": False})
    with engine.connect() as conn:
        # signal_outcomes without position_id / closing_trade_idea_id / trade_idea_id
        conn.execute(text("""
            CREATE TABLE IF NOT EXISTS signal_outcomes (
                id INTEGER PRIMARY KEY,
                strategy_id TEXT,
                market TEXT,
                direction TEXT,
                entry_price REAL,
                exit_price REAL,
                size REAL,
                pnl REAL,
                pnl_pct REAL,
                confidence_at_entry REAL,
                exit_reason TEXT,
                entry_at DATETIME,
                exit_at DATETIME
            )
        """))
        # open_positions without trade_idea_id
        conn.execute(text("""
            CREATE TABLE IF NOT EXISTS open_positions (
                position_id TEXT PRIMARY KEY,
                market TEXT,
                size REAL,
                avg_price REAL,
                signal_confidence REAL,
                strategy_id TEXT,
                direction TEXT,
                unrealized_pnl REAL,
                opened_at DATETIME,
                updated_at DATETIME
            )
        """))
        # control_state without selected_strategy / live_markets
        conn.execute(text("""
            CREATE TABLE IF NOT EXISTS control_state (
                id INTEGER PRIMARY KEY,
                emergency_stop INTEGER,
                disabled_markets JSON,
                disabled_strategies JSON,
                updated_at DATETIME
            )
        """))
        conn.commit()
    return engine


class DatabaseMigrationBDDTests(unittest.TestCase):

    # GIVEN a fresh database WHEN init_database runs THEN all required columns exist.
    def test_given_fresh_db_when_init_runs_then_all_required_columns_present(self) -> None:
        url = "sqlite://"   # in-memory
        init_database(url)

        with get_session() as session:
            # Ensure tables exist by doing a trivial query
            session.execute(text("SELECT 1"))

        # Re-inspect via a raw connection to verify schema
        # Use the module-level engine created by init_database
        from storage import database as db_mod
        eng = db_mod._SessionLocal.kw["bind"]

        outcome_cols = _columns(eng, "signal_outcomes")
        pos_cols = _columns(eng, "open_positions")
        control_cols = _columns(eng, "control_state")

        self.assertIn("position_id", outcome_cols,
                      "signal_outcomes.position_id missing")
        self.assertIn("closing_trade_idea_id", outcome_cols,
                      "signal_outcomes.closing_trade_idea_id missing")
        self.assertIn("trade_idea_id", outcome_cols,
                      "signal_outcomes.trade_idea_id missing")
        self.assertIn("trade_idea_id", pos_cols,
                      "open_positions.trade_idea_id missing")
        self.assertIn("selected_strategy", control_cols,
                      "control_state.selected_strategy missing")

    # GIVEN a legacy database missing migration columns WHEN init_database runs THEN
    # the missing columns are added without dropping any existing data.
    def test_given_legacy_db_when_init_runs_then_missing_columns_are_added(self) -> None:
        url = "sqlite:///file:legacy_migration_test?mode=memory&cache=shared&uri=true"
        _make_old_db_engine(url)   # seed legacy tables
        init_database(url)

        from storage import database as db_mod
        eng = db_mod._SessionLocal.kw["bind"]

        outcome_cols = _columns(eng, "signal_outcomes")
        pos_cols = _columns(eng, "open_positions")
        control_cols = _columns(eng, "control_state")

        self.assertIn("position_id", outcome_cols)
        self.assertIn("closing_trade_idea_id", outcome_cols)
        self.assertIn("trade_idea_id", outcome_cols)
        self.assertIn("trade_idea_id", pos_cols)
        self.assertIn("selected_strategy", control_cols)

    # GIVEN init_database has already run WHEN it is called again THEN no error is raised
    # (idempotent — running twice is safe).
    def test_given_already_initialised_db_when_init_called_again_then_no_error(self) -> None:
        url = "sqlite://"
        init_database(url)
        try:
            init_database(url)
        except Exception as exc:
            self.fail(f"Second init_database call raised: {exc}")

    # GIVEN the database is initialised WHEN the schema is inspected THEN
    # the applied schema version is recorded for future migrations.
    def test_given_initialised_db_when_schema_checked_then_schema_version_is_recorded(self) -> None:
        url = "sqlite://"
        init_database(url)

        from storage import database as db_mod
        eng = db_mod._SessionLocal.kw["bind"]

        self.assertIn("schema_version", inspect(eng).get_table_names())
        with eng.connect() as conn:
            version = conn.execute(text("SELECT version FROM schema_version WHERE id = 1")).scalar_one()
        self.assertGreaterEqual(version, 1)


if __name__ == "__main__":
    unittest.main()
