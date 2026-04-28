"""
Shared pytest fixtures for the LLMTradingBot test suite.
"""

import os
import sys

import pytest
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

# Ensure backend modules are importable
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "backend"))

# Point pydantic-settings to a test .env so the real one is not required
os.environ.setdefault("DATABASE_URL", "sqlite:///:memory:")
os.environ.setdefault("LOG_LEVEL", "WARNING")


@pytest.fixture()
def in_memory_engine():
    """SQLite in-memory engine with all tables created."""
    from storage.models import Base

    engine = create_engine(
        "sqlite:///:memory:",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
    )
    Base.metadata.create_all(engine)
    yield engine
    engine.dispose()


@pytest.fixture()
def db_session(in_memory_engine):
    """Scoped DB session for a single test; rolls back on teardown."""
    Session = sessionmaker(bind=in_memory_engine)
    session = Session()
    yield session
    session.rollback()
    session.close()
