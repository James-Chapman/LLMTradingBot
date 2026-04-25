"""Retry helpers for Kraken REST calls."""

import asyncio
import random
from typing import Any, Awaitable, Callable, Iterable

RATE_LIMIT_MARKERS = (
    "rate limit",
    "temporary lockout",
    "service unavailable",
    "too many requests",
)


# Return a small positive jitter to avoid synchronized retry bursts.
def default_backoff_jitter(delay: float) -> float:
    """Return random jitter up to ten percent of the current delay."""
    return random.uniform(0.0, delay * 0.1)


# Return Kraken error strings from a REST response payload.
def kraken_response_errors(response: Any) -> list[str]:
    """Extract Kraken error strings from a REST response-like object."""
    if not isinstance(response, dict):
        return []
    errors = response.get("error") or []
    return [str(error) for error in errors]


# Return whether an exception or Kraken error string is retryable.
def is_retryable_kraken_error(error: object) -> bool:
    """Return True when the error looks like a Kraken rate-limit response."""
    text = str(error).lower()
    return any(marker in text for marker in RATE_LIMIT_MARKERS)


# Return whether any Kraken error in a response should trigger retry.
def has_retryable_kraken_error(errors: Iterable[str]) -> bool:
    """Return True if any error in errors is a retryable Kraken error."""
    return any(is_retryable_kraken_error(error) for error in errors)


# Call an async Kraken operation with exponential backoff for rate-limit errors.
async def call_with_kraken_backoff(
    operation: Callable[[], Awaitable[Any]],
    *,
    max_attempts: int = 3,
    initial_delay: float = 0.5,
    sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    jitter: Callable[[float], float] | None = None,
    logger: Any = None,
    operation_name: str = "Kraken API call",
) -> Any:
    """Retry operation when Kraken reports a transient rate-limit error."""
    backoff_jitter = jitter or default_backoff_jitter
    attempt = 1
    delay = initial_delay
    while True:
        sleep_for = delay + max(0.0, backoff_jitter(delay))
        try:
            response = await operation()
        except Exception as exc:
            if attempt >= max_attempts or not is_retryable_kraken_error(exc):
                raise
            if logger:
                logger.warning(
                    "Kraken call rate-limited, backing off",
                    extra={
                        "operation": operation_name,
                        "attempt": attempt,
                        "delay_seconds": sleep_for,
                        "error": str(exc),
                    },
                )
        else:
            errors = kraken_response_errors(response)
            if not errors or not has_retryable_kraken_error(errors):
                return response
            if attempt >= max_attempts:
                return response
            if logger:
                logger.warning(
                    "Kraken response rate-limited, backing off",
                    extra={
                        "operation": operation_name,
                        "attempt": attempt,
                        "delay_seconds": sleep_for,
                        "errors": errors,
                    },
                )

        await sleep(sleep_for)
        attempt += 1
        delay *= 2
