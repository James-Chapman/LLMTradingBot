"""
OpenAI-compatible external LLM client.

Connects to any endpoint that speaks the OpenAI chat-completions API —
OpenAI, Azure OpenAI, LM Studio, llama.cpp server, Ollama with OpenAI compat, etc.
Configure OPENAI_BASE_URL, OPENAI_API_KEY, and OPENAI_MODEL in .env.
"""

import json
from datetime import datetime, timedelta, timezone
from typing import Any, Callable, Optional

import httpx

from observability.logging import get_logger

logger = get_logger("openai_client")

_INITIAL_RETRY_DELAY = timedelta(seconds=30)
_MAX_RETRY_DELAY = timedelta(minutes=5)


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _extract_first_json_object(content: str) -> Optional[dict[str, Any]]:
    """Return the first JSON object found in content, or None."""
    decoder = json.JSONDecoder()
    for index, character in enumerate(content):
        if character != "{":
            continue
        try:
            loaded, _ = decoder.raw_decode(content[index:])
            if isinstance(loaded, dict):
                return loaded
        except json.JSONDecodeError:
            continue
    return None


def _parse_response_content(content: str) -> dict[str, Any]:
    """Parse a model response string as JSON, stripping markdown fences if present."""
    stripped = content.strip()
    if stripped.startswith("```"):
        lines = stripped.splitlines()
        if len(lines) >= 3 and lines[-1].strip() == "```":
            stripped = "\n".join(lines[1:-1]).strip()
    try:
        loaded = json.loads(stripped)
        if isinstance(loaded, dict):
            return loaded
    except json.JSONDecodeError:
        pass
    extracted = _extract_first_json_object(content)
    if extracted is not None:
        return extracted
    raise json.JSONDecodeError("OpenAI response was not a JSON object", content, 0)


class OpenAiClient:
    """Client for any OpenAI-compatible chat-completions endpoint."""

    def __init__(
        self,
        base_url: str,
        api_key: str,
        model: str,
        timeout: int = 30,
        clock: Callable[[], datetime] = _utc_now,
    ):
        self.base_url = base_url.rstrip("/") if base_url else ""
        self.api_key = api_key or ""
        self.model = model or ""
        self.timeout = timeout
        self._clock = clock
        self.available: bool = False
        self._last_failure: Optional[datetime] = None
        self._next_retry_at: Optional[datetime] = None
        self._failure_count = 0
        self._retry_delay = _INITIAL_RETRY_DELAY
        self._circuit_state = "closed"

    @property
    def is_configured(self) -> bool:
        """Return True when both base_url and model are set."""
        return bool(self.base_url and self.model)

    @property
    def circuit_state(self) -> str:
        return self._circuit_state

    @property
    def retry_delay_seconds(self) -> int:
        return int(self._retry_delay.total_seconds())

    @property
    def can_attempt(self) -> bool:
        """Return whether a chat call may be attempted now."""
        if self._circuit_state == "closed":
            return True
        return bool(self._next_retry_at and self._clock() >= self._next_retry_at)

    @property
    def llm_model(self) -> str:
        return f"{self.model} (OpenAI)" if self.model else ""

    def _headers(self) -> dict[str, str]:
        h: dict[str, str] = {"Content-Type": "application/json"}
        if self.api_key:
            h["Authorization"] = f"Bearer {self.api_key}"
        return h

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
        logger.warning(f"OpenAI client unavailable: {reason}; retry in {self._retry_delay}")
        self.available = False
        self._last_failure = self._clock()
        self._next_retry_at = self._last_failure + self._retry_delay
        self._circuit_state = "open"

    async def probe(self) -> bool:
        """Test connectivity and validate the response contains a 'choices' field."""
        if not self.is_configured:
            return False
        try:
            async with httpx.AsyncClient(timeout=min(self.timeout, 10)) as client:
                resp = await client.post(
                    f"{self.base_url}/chat/completions",
                    json={
                        "model": self.model,
                        "messages": [{"role": "user", "content": "ping"}],
                        "max_tokens": 1,
                    },
                    headers=self._headers(),
                )
                resp.raise_for_status()
                data = resp.json()
            if "choices" not in data:
                self._mark_failed(f"unexpected response (no 'choices'): {str(data)[:120]}")
                return False
            self._mark_success()
            logger.info(f"OpenAI client available — url: {self.base_url}, model: {self.model}")
            return True
        except Exception as e:
            self._mark_failed(str(e))
            return False

    async def chat(
        self,
        messages: list[dict[str, Any]],
        expect_json: bool = True,
    ) -> Optional[dict[str, Any] | str]:
        """Call the chat-completions endpoint and return parsed JSON or raw string."""
        if not self._should_attempt() or not self.is_configured:
            return None

        payload: dict[str, Any] = {
            "model": self.model,
            "messages": messages,
            "temperature": 0.3,
        }
        # json_object mode guarantees valid JSON output; not all local servers support it
        # but we send it anyway and fall back to extraction if parsing fails.
        if expect_json:
            payload["response_format"] = {"type": "json_object"}

        content: str = ""
        try:
            async with httpx.AsyncClient(timeout=self.timeout) as client:
                resp = await client.post(
                    f"{self.base_url}/chat/completions",
                    json=payload,
                    headers=self._headers(),
                )
                resp.raise_for_status()
            data = resp.json()
            content = data["choices"][0]["message"]["content"]
            self._mark_success()
            if expect_json:
                return _parse_response_content(content)
            return content
        except json.JSONDecodeError as e:
            logger.warning(f"OpenAI returned non-JSON: {e}", extra={"content": content[:200]})
            return None
        except Exception as e:
            self._mark_failed(str(e))
            return None
