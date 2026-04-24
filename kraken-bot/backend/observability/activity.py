"""
Activity log — rolling buffer of recent bot events, persisted to DB.

The in-memory deque (maxlen=200) keeps the dashboard fast.
Every entry is also written to the activity_log table so history
survives restarts.  Call `activity.set_repo(repo)` once at startup
after the repository is available.
"""
from collections import deque
from datetime import datetime
from typing import List


class ActivityLog:
    def __init__(self, maxlen: int = 200) -> None:
        self._entries: deque = deque(maxlen=maxlen)
        self._repo = None  # injected after init to avoid circular imports

    def set_repo(self, repo) -> None:
        """Wire in the Repository so entries are persisted to DB."""
        self._repo = repo

    def seed_from_db(self) -> None:
        """Pre-populate the in-memory deque from DB history (call once at startup)."""
        if self._repo is None:
            return
        try:
            rows = self._repo.get_recent_activity(limit=self._entries.maxlen)
            # rows are newest-first; reverse so appendleft fills correctly
            for row in reversed(rows):
                self._entries.appendleft(row)
        except Exception:
            pass  # non-fatal — we just start with an empty log

    def _add(self, level: str, message: str, detail: str = "") -> None:
        entry = {
            "timestamp": datetime.utcnow().isoformat(),
            "level": level,
            "message": message,
            "detail": detail,
        }
        self._entries.appendleft(entry)
        if self._repo is not None:
            try:
                self._repo.save_activity_log(level, message, detail)
            except Exception:
                pass  # never let DB errors break the bot loop

    def info(self, message: str, detail: str = "") -> None:
        self._add("info", message, detail)

    def warn(self, message: str, detail: str = "") -> None:
        self._add("warn", message, detail)

    def error(self, message: str, detail: str = "") -> None:
        self._add("error", message, detail)

    def success(self, message: str, detail: str = "") -> None:
        self._add("success", message, detail)

    def recent(self, n: int = 100) -> List[dict]:
        return list(self._entries)[:n]


activity = ActivityLog()
