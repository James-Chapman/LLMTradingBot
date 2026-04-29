"""Shared helpers for LLM client implementations."""

import json
import re
from datetime import datetime, timedelta, timezone
from typing import Any, Callable, Optional

_INITIAL_RETRY_DELAY = timedelta(seconds=30)
_MAX_RETRY_DELAY = timedelta(minutes=5)


# Return a timezone-aware UTC timestamp for circuit breaker clocks.
def utc_now() -> datetime:
    """Return the current UTC time as a timezone-aware datetime."""
    return datetime.now(timezone.utc)


# Render OpenAI-style messages as a readable plain-text prompt.
def messages_to_prompt(messages: list[dict[str, Any]], append_json_instruction: bool = False) -> str:
    """Return a prompt transcript from OpenAI-style chat messages."""
    prompt_parts: list[str] = []
    for msg in messages:
        role = str(msg.get("role", "user")).lower()
        content = str(msg.get("content", ""))
        if role == "system":
            prompt_parts.append(f"System: {content}")
        elif role == "user":
            prompt_parts.append(f"User: {content}")
        elif role == "assistant":
            prompt_parts.append(f"Assistant: {content}")
        else:
            prompt_parts.append(f"{role.capitalize()}: {content}")
    if append_json_instruction:
        prompt_parts.append("Respond with valid JSON only.")
    return "\n".join(prompt_parts)


# Parse model content as JSON, allowing common LLM formatting mistakes.
def loads_model_json(content: str, error_label: str = "LLM JSON response") -> dict[str, Any]:
    """Return a parsed JSON object from raw model content."""
    first_error = None
    stripped = strip_json_code_fence(content)
    attempts = [
        content,
        stripped,
        repair_missing_field_commas(stripped),
    ]
    for candidate in attempts:
        try:
            loaded: Any = json.loads(candidate)
            if isinstance(loaded, dict):
                return loaded
        except json.JSONDecodeError as exc:
            if first_error is None:
                first_error = exc

        extracted = extract_first_json_object(candidate)
        if extracted is not None:
            return extracted

    if first_error is not None:
        raise first_error
    raise json.JSONDecodeError(f"{error_label} was not an object", content, 0)


# Remove Markdown code fences that some models still emit around JSON.
def strip_json_code_fence(content: str) -> str:
    """Return content without a surrounding JSON Markdown fence."""
    stripped = content.strip()
    if not stripped.startswith("```"):
        return stripped
    lines = stripped.splitlines()
    if len(lines) >= 3 and lines[0].startswith("```") and lines[-1].strip() == "```":
        return "\n".join(lines[1:-1]).strip()
    return stripped


# Extract the first complete JSON object from content with surrounding prose.
def extract_first_json_object(content: str) -> Optional[dict[str, Any]]:
    """Return the first JSON object embedded in content, if one parses."""
    decoder = json.JSONDecoder()
    for index, character in enumerate(content):
        if character != "{":
            continue
        try:
            loaded, _ = decoder.raw_decode(content[index:])
        except json.JSONDecodeError:
            continue
        if isinstance(loaded, dict):
            return loaded
    return None


# Insert missing commas between newline-separated JSON object fields.
def repair_missing_field_commas(content: str) -> str:
    """Return content with obvious missing field separators repaired."""
    lines = content.splitlines()
    repaired: list[str] = []
    for index, line in enumerate(lines):
        next_line = lines[index + 1] if index + 1 < len(lines) else ""
        if line_needs_field_comma(line, next_line):
            line = f"{line.rstrip()},"
        repaired.append(line)
    return "\n".join(repaired)


# Detect a JSON value line followed by another object field without a comma.
def line_needs_field_comma(line: str, next_line: str) -> bool:
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


class CircuitBreakerMixin:
    """Shared exponential-backoff circuit breaker for LLM clients."""

    # Initialise the shared circuit breaker state.
    def _init_circuit_breaker(self, clock: Callable[[], datetime] = utc_now) -> None:
        """Initialise circuit breaker attributes on the client instance."""
        self._clock = clock
        self.available: bool = False
        self._last_failure: Optional[datetime] = None
        self._next_retry_at: Optional[datetime] = None
        self._failure_count = 0
        self._retry_delay = _INITIAL_RETRY_DELAY
        self._circuit_state = "closed"

    @property
    def circuit_state(self) -> str:
        """Return the circuit breaker state: closed, open, or half_open."""
        return self._circuit_state

    @property
    def retry_delay_seconds(self) -> int:
        """Return the current retry delay in whole seconds."""
        return int(self._retry_delay.total_seconds())

    @property
    def can_attempt(self) -> bool:
        """Return whether a chat call may be attempted now."""
        if self._circuit_state == "closed":
            return True
        return bool(self._next_retry_at and self._clock() >= self._next_retry_at)

    # Return whether a call should proceed, moving to half-open when retry time arrives.
    def _should_attempt(self) -> bool:
        """Return True when the circuit allows a new attempt."""
        if self._circuit_state == "closed":
            return True
        if self._next_retry_at and self._clock() >= self._next_retry_at:
            self._circuit_state = "half_open"
            return True
        return False

    # Reset the circuit breaker after a successful call.
    def _mark_success(self) -> None:
        """Mark the client as available and close the circuit."""
        self.available = True
        self._last_failure = None
        self._next_retry_at = None
        self._failure_count = 0
        self._retry_delay = _INITIAL_RETRY_DELAY
        self._circuit_state = "closed"

    # Open the circuit and advance the retry delay after a failed call.
    def _mark_failed(self, reason: str) -> None:
        """Mark the client as unavailable and schedule the next retry."""
        delay_seconds = min(
            int(_INITIAL_RETRY_DELAY.total_seconds()) * (2**self._failure_count),
            int(_MAX_RETRY_DELAY.total_seconds()),
        )
        self._retry_delay = timedelta(seconds=delay_seconds)
        self._failure_count += 1
        log_failure = getattr(self, "_log_failure", None)
        if callable(log_failure):
            log_failure(reason)
        self.available = False
        self._last_failure = self._clock()
        self._next_retry_at = self._last_failure + self._retry_delay
        self._circuit_state = "open"
