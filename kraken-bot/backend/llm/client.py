"""
Async Ollama client.

Talks to a locally-running Ollama instance via its REST API.
Falls back gracefully when Ollama is not available so the rest of the
bot keeps running without it.
"""
import json
from datetime import datetime, timedelta
from typing import Callable, Optional

import httpx

from observability.logging import get_logger

logger = get_logger("ollama")

_INITIAL_RETRY_DELAY = timedelta(seconds=30)
_MAX_RETRY_DELAY = timedelta(minutes=5)


class OllamaClient:
    def __init__(
        self,
        base_url: str,
        model: str,
        timeout: int = 15,
        clock: Callable[[], datetime] = datetime.utcnow,
    ):
        self.base_url = base_url.rstrip("/")
        self.model = model
        self.timeout = timeout
        self._clock = clock
        self.available: bool = False
        self._last_failure: Optional[datetime] = None
        self._next_retry_at: Optional[datetime] = None
        self._failure_count = 0
        self._retry_delay = _INITIAL_RETRY_DELAY
        self._circuit_state = "closed"
        self._client = httpx.AsyncClient(timeout=timeout)

    @property
    def circuit_state(self) -> str:
        """Return the circuit breaker state: closed, open, or half_open."""
        return self._circuit_state

    @property
    def retry_delay_seconds(self) -> int:
        """Return the current retry delay in whole seconds."""
        return int(self._retry_delay.total_seconds())

    async def probe(self) -> bool:
        """Check whether Ollama is running and the configured model is loaded."""
        try:
            r = await self._client.get(f"{self.base_url}/api/tags", timeout=5)
            if r.status_code != 200:
                self._mark_failed(f"probe status {r.status_code}")
                return False
            tags = r.json().get("models", [])
            names = [t.get("name", "").split(":")[0] for t in tags]
            model_base = self.model.split(":")[0]
            if model_base not in names:
                logger.warning(
                    f"Ollama running but model '{self.model}' not found. "
                    f"Run: ollama pull {self.model}  |  Available: {names}"
                )
                self._mark_failed(f"model '{self.model}' not found")
                return False
            self._mark_success()
            logger.info(f"Ollama available - model: {self.model}")
            return True
        except Exception as e:
            self._mark_failed(str(e))
            return False

    async def chat(self, messages: list[dict], expect_json: bool = True) -> Optional[dict | str]:
        """Send a chat request. Returns parsed dict if expect_json, else str. None on failure."""
        if not self._should_attempt():
            return None
        payload = {
            "model": self.model,
            "messages": messages,
            "stream": False,
        }
        if expect_json:
            payload["format"] = "json"
        try:
            r = await self._client.post(
                f"{self.base_url}/api/chat",
                json=payload,
                timeout=self.timeout,
            )
            r.raise_for_status()
            content = r.json()["message"]["content"]
            self._mark_success()
            if expect_json:
                return json.loads(content)
            return content
        except json.JSONDecodeError as e:
            logger.warning(f"Ollama returned non-JSON: {e}")
            self._mark_failed(str(e))
            return None
        except Exception as e:
            self._mark_failed(str(e))
            return None

    def _should_attempt(self) -> bool:
        if self._circuit_state == "closed":
            return True
        if self._next_retry_at and self._clock() >= self._next_retry_at:
            self._circuit_state = "half_open"
            return True
        return False

    def _mark_success(self) -> None:
        self.available = True
        self._last_failure = None
        self._next_retry_at = None
        self._failure_count = 0
        self._retry_delay = _INITIAL_RETRY_DELAY
        self._circuit_state = "closed"

    def _mark_failed(self, reason: str) -> None:
        delay_seconds = min(
            int(_INITIAL_RETRY_DELAY.total_seconds()) * (2 ** self._failure_count),
            int(_MAX_RETRY_DELAY.total_seconds()),
        )
        self._retry_delay = timedelta(seconds=delay_seconds)
        self._failure_count += 1
        if self.available:
            logger.warning(f"Ollama unavailable: {reason}; retry in {self._retry_delay}")
        self.available = False
        self._last_failure = self._clock()
        self._next_retry_at = self._last_failure + self._retry_delay
        self._circuit_state = "open"
