"""
Async LM Studio client.

Talks to a locally-running LM Studio instance via its OpenAI-compatible REST API.
Falls back gracefully when LM Studio is not available so the rest of the
bot keeps running without it.
"""

import json
import re
from datetime import datetime, timedelta
from typing import Any, Callable, Optional

import httpx

from observability.logging import get_logger

logger = get_logger("lm_studio")

_INITIAL_RETRY_DELAY = timedelta(seconds=30)
_MAX_RETRY_DELAY = timedelta(minutes=5)


# Parse model content as JSON, allowing common LLM formatting mistakes.
def _loads_model_json(content: str) -> dict[str, Any]:
    """Return a parsed JSON object from raw model content."""
    first_error = None
    attempts = [
        content,
        _strip_json_code_fence(content),
        _repair_missing_field_commas(_strip_json_code_fence(content)),
    ]
    for candidate in attempts:
        try:
            loaded = json.loads(candidate)
            if isinstance(loaded, dict):
                return loaded
        except json.JSONDecodeError as exc:
            if first_error is None:
                first_error = exc

        extracted = _extract_first_json_object(candidate)
        if extracted is not None:
            return extracted

    if first_error is not None:
        raise first_error
    raise json.JSONDecodeError("LM Studio JSON response was not an object", content, 0)


# Remove Markdown code fences that some models still emit around JSON.
def _strip_json_code_fence(content: str) -> str:
    """Return content without a surrounding JSON Markdown fence."""
    stripped = content.strip()
    if not stripped.startswith("```"):
        return stripped
    lines = stripped.splitlines()
    if len(lines) >= 3 and lines[0].startswith("```") and lines[-1].strip() == "```":
        return "\n".join(lines[1:-1]).strip()
    return stripped


# Extract the first complete JSON object from content with surrounding prose.
def _extract_first_json_object(content: str) -> Optional[dict[str, Any]]:
    """Return the first JSON object embedded in content, if one parses."""
    decoder = json.JSONDecoder()
    for index, character in enumerate(content):
        if character != "{":
            continue
        try:
            loaded, _end = decoder.raw_decode(content[index:])
        except json.JSONDecodeError:
            continue
        if isinstance(loaded, dict):
            return loaded
    return None


# Insert missing commas between newline-separated JSON object fields.
def _repair_missing_field_commas(content: str) -> str:
    """Return content with obvious missing field separators repaired."""
    lines = content.splitlines()
    repaired = []
    for index, line in enumerate(lines):
        next_line = lines[index + 1] if index + 1 < len(lines) else ""
        if _line_needs_field_comma(line, next_line):
            line = f"{line.rstrip()},"
        repaired.append(line)
    return "\n".join(repaired)


# Detect a JSON value line followed by another object field without a comma.
def _line_needs_field_comma(line: str, next_line: str) -> bool:
    """Return True when a missing comma can be inferred safely."""
    stripped = line.strip()
    next_stripped = next_line.strip()
    if not stripped or stripped.endswith((",", "{", "[")):
        return False
    if not next_stripped.startswith('"') or '":' not in next_stripped:
        return False
    return bool(
        stripped.endswith(('"', "}", "]"))
        or re.search(r"(?<![A-Za-z])(?:true|false|null|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)$", stripped)
    )


class LMStudioClient:
    def __init__(
        self,
        base_url: str,
        model: str,
        timeout: int = 60,
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

    @property
    def llm_model(self) -> str:
        """Return the model name this client is configured to use."""
        return self.model

    @property
    def can_attempt(self) -> bool:
        """Return whether a chat call may be attempted now."""
        if self._circuit_state == "closed":
            return True
        return bool(self._next_retry_at and self._clock() >= self._next_retry_at)

    async def probe(self) -> bool:
        """Check whether LM Studio is running and serving a model."""
        try:
            r = await self._client.get(f"{self.base_url}/v1/models", timeout=5)
            if r.status_code != 200:
                self._mark_failed(f"probe status {r.status_code}")
                return False
            self._mark_success()
            logger.info(f"LM Studio available - model: {self.model}")
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
        try:
            r = await self._client.post(
                f"{self.base_url}/v1/chat/completions",
                json=payload,
                timeout=self.timeout,
            )
            r.raise_for_status()
            content = r.json()["choices"][0]["message"]["content"]
            self._mark_success()
            if expect_json:
                return _loads_model_json(content)
            return content
        except json.JSONDecodeError as e:
            logger.warning(
                f"LM Studio returned non-JSON: {e}",
                extra={"raw_response": content},
            )
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
            int(_INITIAL_RETRY_DELAY.total_seconds()) * (2**self._failure_count),
            int(_MAX_RETRY_DELAY.total_seconds()),
        )
        self._retry_delay = timedelta(seconds=delay_seconds)
        self._failure_count += 1
        if self.available:
            logger.warning(f"LM Studio unavailable: {reason}; retry in {self._retry_delay}")
        self.available = False
        self._last_failure = self._clock()
        self._next_retry_at = self._last_failure + self._retry_delay
        self._circuit_state = "open"
