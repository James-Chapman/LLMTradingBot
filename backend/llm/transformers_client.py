"""
Async Transformers client.

Loads a local Hugging Face model via Transformers pipeline.
Falls back gracefully when the model fails to load or generate.
"""

import asyncio
import json
import re
from datetime import datetime, timedelta, timezone
from typing import Any, Callable, Optional

from transformers import pipeline

from observability.logging import get_logger

logger = get_logger("transformers")

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
            loaded: Any = json.loads(candidate)
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
    raise json.JSONDecodeError("Transformers JSON response was not an object", content, 0)


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
            loaded, _ = decoder.raw_decode(content[index:])
        except json.JSONDecodeError:
            continue
        if isinstance(loaded, dict):
            return loaded
    return None


# Insert missing commas between newline-separated JSON object fields.
def _repair_missing_field_commas(content: str) -> str:
    """Return content with obvious missing field separators repaired."""
    lines = content.splitlines()
    repaired: list[str] = []
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


def _utc_now() -> datetime:
    """Return the current UTC time as a timezone-aware datetime."""
    return datetime.now(timezone.utc)


class TransformersClient:
    def __init__(
        self,
        model: str,
        timeout: int = 15,
        clock: Callable[[], datetime] = _utc_now,
    ):
        self.model = model
        self.timeout = timeout
        self._clock = clock
        self.available: bool = False
        self._last_failure: Optional[datetime] = None
        self._next_retry_at: Optional[datetime] = None
        self._failure_count = 0
        self._retry_delay = _INITIAL_RETRY_DELAY
        self._circuit_state = "closed"
        self._pipeline = None  # Loaded on first probe

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

    @property
    def llm_model(self) -> str:
        """Return the model name this client is configured to use."""
        return self.model

    async def probe(self) -> bool:
        """Load the model if not already loaded."""
        if self._pipeline is not None:
            self._mark_success()
            logger.info(f"Transformers model available - model: {self.model}")
            return True
        try:
            # Load the pipeline in a thread to avoid blocking the event loop.
            # device_map="auto" places layers on GPU when available; torch_dtype="auto"
            # uses the model's native dtype (bfloat16/float16) instead of float32,
            # cutting VRAM usage roughly in half.
            self._pipeline = await asyncio.to_thread(
                pipeline,
                "text-generation",
                model=self.model,
                device_map="auto",
                dtype="auto",
            )
            self._mark_success()
            logger.info(f"Transformers model loaded - model: {self.model}")
            return True
        except Exception as e:
            reason = str(e)
            # GGUF-only repos have no config.json/model_type and cannot be loaded
            # via pipeline(). Set TRANSFORMERS_LLM_MODEL to a standard HuggingFace
            # model such as Qwen/Qwen2.5-1.5B-Instruct or google/gemma-2-2b-it.
            if "model_type" in reason or "config.json" in reason:
                reason = (
                    f"GGUF-only repository '{self.model}' is not supported by the "
                    f"transformers pipeline. Set TRANSFORMERS_LLM_MODEL to a standard "
                    f"HuggingFace model (e.g. Qwen/Qwen2.5-1.5B-Instruct)."
                )
            logger.error(f"Transformers probe failed — model: {self.model} — {reason}")
            self._mark_failed(reason)
            return False

    async def chat(self, messages: list[dict[str, Any]], expect_json: bool = True) -> Optional[dict[str, Any] | str]:
        """Generate a response using the local model. Returns parsed dict if expect_json, else str. None on failure."""
        if not self._should_attempt() or self._pipeline is None:
            return None

        # Convert messages to a single prompt string
        prompt_parts: list[str] = []
        for msg in messages:
            role: str = str(msg.get("role", "user"))
            content: str = str(msg.get("content", ""))
            if role == "system":
                prompt_parts.append(f"System: {content}")
            elif role == "user":
                prompt_parts.append(f"User: {content}")
            elif role == "assistant":
                prompt_parts.append(f"Assistant: {content}")
        prompt = "\n".join(prompt_parts)
        if expect_json:
            prompt += "\nRespond with valid JSON only."

        raw_output = ""
        try:
            # Generate in a thread to keep async
            outputs = await asyncio.to_thread(self._pipeline, prompt, max_length=512, num_return_sequences=1)
            raw_output = outputs[0]["generated_text"].strip()
            self._mark_success()
            if expect_json:
                return _loads_model_json(raw_output)
            return raw_output
        except json.JSONDecodeError as e:
            logger.warning(
                f"Transformers returned non-JSON: {e}",
                extra={"raw_response": raw_output},
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
        logger.warning(f"Transformers unavailable: {reason}; retry in {self._retry_delay}")
        self.available = False
        self._last_failure = self._clock()
        self._next_retry_at = self._last_failure + self._retry_delay
        self._circuit_state = "open"
