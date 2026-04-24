"""
Database setup and session management.
Call init_database() once at startup; use get_session() everywhere else.
"""
from contextlib import contextmanager

from sqlalchemy import create_engine, inspect, text
from sqlalchemy.engine import Engine
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from .models import Base

# Module-level session factory — set by init_database()
_SessionLocal = None
_SCHEMA_VERSION = 1


# Return a fresh set of table names for the current engine.
def _table_names(engine: Engine) -> set[str]:
    return set(inspect(engine).get_table_names())


# Return the current column names for a table.
def _column_names(engine: Engine, table_name: str) -> set[str]:
    return {c["name"] for c in inspect(engine).get_columns(table_name)}


# Add only the columns still missing from a legacy table.
def _add_missing_columns(
    engine: Engine,
    table_name: str,
    columns: list[tuple[str, str]],
) -> None:
    if table_name not in _table_names(engine):
        return
    existing_cols = _column_names(engine, table_name)
    missing = [(name, ddl) for name, ddl in columns if name not in existing_cols]
    if not missing:
        return
    with engine.begin() as conn:
        for name, ddl in missing:
            conn.execute(text(f"ALTER TABLE {table_name} ADD COLUMN {name} {ddl}"))


# Record the currently applied schema version for later migrations.
def _record_schema_version(engine: Engine) -> None:
    with engine.begin() as conn:
        conn.execute(text(
            """
            CREATE TABLE IF NOT EXISTS schema_version (
                id INTEGER PRIMARY KEY CHECK (id = 1),
                version INTEGER NOT NULL,
                updated_at TEXT NOT NULL
            )
            """
        ))
        conn.execute(
            text(
                """
                INSERT OR REPLACE INTO schema_version (id, version, updated_at)
                VALUES (1, :version, CURRENT_TIMESTAMP)
                """
            ),
            {"version": _SCHEMA_VERSION},
        )


def init_database(database_url: str) -> None:
    global _SessionLocal

    kwargs = {}
    if database_url.startswith("sqlite"):
        kwargs = {
            "connect_args": {"check_same_thread": False},
            "poolclass": StaticPool,
        }

    engine = create_engine(database_url, echo=False, **kwargs)

    inspector = inspect(engine)

    # Migrate open_positions if it still uses the old market-as-PK schema.
    if "open_positions" in inspector.get_table_names():
        existing_cols = {c["name"] for c in inspector.get_columns("open_positions")}
        if "position_id" not in existing_cols:
            with engine.connect() as conn:
                conn.execute(text("DROP TABLE open_positions"))
                conn.commit()

    Base.metadata.create_all(bind=engine)

    # Add new columns to legacy tables without failing on partially applied schemas.
    _add_missing_columns(engine, "order_records", [
        ("position_id", "TEXT"),
        ("pnl", "REAL"),
        ("trade_idea_id", "TEXT"),
    ])

    # Add signal-context columns to trade_ideas (added for trade→signal linkage).
    _add_missing_columns(engine, "trade_ideas", [
        ("momentum_pct", "REAL"),
        ("indicators", "JSON"),
        ("llm_used", "INTEGER"),
        ("llm_sentiment", "REAL"),
        ("llm_confidence_scale", "REAL"),
        ("llm_reasoning", "TEXT"),
        ("news_context", "JSON"),
        ("risk_approved", "INTEGER"),
        ("risk_reason", "TEXT"),
    ])

    # Add columns to signal_outcomes introduced after initial release.
    _add_missing_columns(engine, "signal_outcomes", [
        ("trade_idea_id", "TEXT"),
        ("position_id", "TEXT"),
        ("closing_trade_idea_id", "TEXT"),
    ])

    # Add trade_idea_id to open_positions (links a position back to the originating signal).
    _add_missing_columns(engine, "open_positions", [
        ("trade_idea_id", "TEXT"),
    ])
    _add_missing_columns(engine, "control_state", [
        ("live_markets", "JSON"),
        ("selected_strategy", "TEXT DEFAULT 'combined'"),
    ])
    _record_schema_version(engine)

    _SessionLocal = sessionmaker(autocommit=False, autoflush=False, bind=engine)


@contextmanager
def get_session() -> Session:
    """Context manager: opens a session, commits on success, rolls back on error."""
    if _SessionLocal is None:
        raise RuntimeError("init_database() has not been called")
    session = _SessionLocal()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()
