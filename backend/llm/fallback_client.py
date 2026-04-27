"""
FallbackLLMClient — probe-and-lock-in chain for LLM backends.

Probes a priority-ordered list of clients at startup and locks in the first
available one. If the active client's circuit opens during operation, the next
available client in the list is promoted automatically.

Priority order (set by the caller): LM Studio → Ollama → Transformers.
"""

from typing import Any, Optional

from observability.logging import get_logger

logger = get_logger("llm_fallback")


class FallbackLLMClient:
    def __init__(self, clients: list) -> None:
        # clients must be ordered highest-priority first
        self._clients = clients
        self._active: Any | None = None

    @property
    def available(self) -> bool:
        """Return True when the active client reports itself available."""
        if self._active is not None and bool(getattr(self._active, "available", False)):
            logger.debug(f"LLM fallback chain: active client {type(self._active).__name__} reports available")
            return True
        logger.warning(f"LLM fallback chain: no active client available")
        return False

    @property
    def llm_model(self) -> str:
        """Return the model name only when the active client is available."""
        if self._active is not None and bool(getattr(self._active, "available", False)):
            return getattr(self._active, "llm_model", "")
        return ""

    @property
    def can_attempt(self) -> bool:
        """Return True when any client in the chain can attempt a call."""
        return any(getattr(c, "can_attempt", False) for c in self._clients)

    @property
    def circuit_state(self) -> str:
        """Return the active client's circuit state, or 'open' when none is active."""
        if self._active is not None:
            logger.debug(
                f"LLM fallback chain: active client {type(self._active).__name__} circuit state {getattr(self._active, 'circuit_state', 'unknown')}"
            )
            return getattr(self._active, "circuit_state", "closed")
        return "open"

    async def probe(self) -> bool:
        """Probe clients in priority order; lock in the first one that succeeds."""
        for client in self._clients:
            name = type(client).__name__
            if await client.probe():
                self._active = client
                logger.info(f"LLM fallback chain: locked in {name}")
                return True
            logger.info(f"LLM fallback chain: {name} unavailable, trying next")
        self._active = None
        logger.warning("LLM fallback chain: no LLM backend available")
        return False

    async def chat(self, messages: list[dict], expect_json: bool = True) -> Optional[dict | str]:
        """Delegate to the active client, promoting to the next if its circuit is open."""
        self._maybe_promote()
        if self._active is None:
            return None
        result = await self._active.chat(messages, expect_json=expect_json)
        # If the call failed and the circuit opened, try to promote immediately
        if result is None:
            self._maybe_promote()
            if self._active is not None:
                result = await self._active.chat(messages, expect_json=expect_json)
        return result

    def _maybe_promote(self) -> None:
        """Promote to the highest-priority client that can attempt a call."""
        for client in self._clients:
            if getattr(client, "can_attempt", False):
                if client is not self._active:
                    logger.info(
                        f"LLM fallback chain: promoting from "
                        f"{type(self._active).__name__ if self._active else 'None'} "
                        f"to {type(client).__name__}"
                    )
                    self._active = client
                return
        # No client can attempt; leave _active unchanged so callers get None
