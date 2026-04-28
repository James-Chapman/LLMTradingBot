"""
BDD tests for NewsItemModel URL uniqueness removal (T2.B3).

GIVEN / WHEN / THEN style — verifies that two news items sharing the same URL
but with different IDs can both be persisted without IntegrityError.
"""

import pytest
from storage.models import NewsItemModel


class TestNewsUrlConstraintBDD:
    def test_given_two_items_with_same_url_when_added_then_no_integrity_error(self, db_session):
        """
        GIVEN two NewsItemModel rows with the same URL but different primary keys,
        WHEN both are added to the session,
        THEN no IntegrityError is raised and both rows persist.
        """
        item_a = NewsItemModel(
            id="hash-a",
            source="CoinDesk",
            title="Bitcoin rallies",
            content="BTC/EUR up 5%",
            published_at=None,
            url="https://example.com/article-1",
        )
        item_b = NewsItemModel(
            id="hash-b",
            source="CoinNews",
            title="Bitcoin rallies (syndicated)",
            content="BTC/EUR up 5%",
            published_at=None,
            url="https://example.com/article-1",  # same URL, different source
        )

        db_session.add(item_a)
        db_session.add(item_b)
        db_session.flush()  # raises IntegrityError here if unique constraint exists

        rows = db_session.query(NewsItemModel).filter_by(url="https://example.com/article-1").all()
        assert len(rows) == 2

    def test_given_null_urls_when_added_then_no_integrity_error(self, db_session):
        """
        GIVEN two NewsItemModel rows with url=None,
        WHEN both are added,
        THEN no IntegrityError (SQLite UNIQUE allows multiple NULLs but this confirms null handling).
        """
        item_a = NewsItemModel(id="hash-c", source="A", title="T1", content="C1", published_at=None, url=None)
        item_b = NewsItemModel(id="hash-d", source="B", title="T2", content="C2", published_at=None, url=None)

        db_session.add(item_a)
        db_session.add(item_b)
        db_session.flush()

        rows = db_session.query(NewsItemModel).filter_by(url=None).all()
        assert len(rows) == 2
