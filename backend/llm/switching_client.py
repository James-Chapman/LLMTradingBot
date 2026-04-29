"""
SwitchingLLMClient — prefers OpenAiClient, falls back to TransformersClient.

On probe(), OpenAI is tried first. If it is available, the local Transformers
model is unloaded to free GPU/CPU memory. When OpenAI later becomes unavailable
the Transformers model is reloaded automatically.

Call recheck_primary() periodically (e.g. on each news fetch) to detect when
OpenAI comes back online and reclaim it as the preferred backend.
"""

from typing import Any, Optional

from llm.openai_client import OpenAiClient
from llm.transformers_client import TransformersClient
from observability.logging import get_logger

logger = get_logger("switching_client")


class SwitchingLLMClient:
    """Routes LLM calls to OpenAI when available, Transformers otherwise."""

    def __init__(self, primary: OpenAiClient, fallback: TransformersClient):
        self._primary = primary
        self._fallback = fallback

    # ── Protocol properties ───────────────────────────────────────────────

    @property
    def available(self) -> bool:
        """True when at least one backend is marked available."""
        return self._primary.available or self._fallback.available

    @property
    def can_attempt(self) -> bool:
        """True when at least one backend can accept a call right now."""
        return self._primary.can_attempt or self._fallback.can_attempt

    @property
    def llm_model(self) -> str:
        """Return the model name of whichever backend is currently preferred."""
        if self._primary.available:
            return self._primary.llm_model
        if self._fallback.available:
            return self._fallback.llm_model
        # Neither available — show whichever is configured so the UI isn't blank.
        return self._primary.llm_model or self._fallback.llm_model

    @property
    def active_backend(self) -> str:
        """Return 'openai', 'transformers', or 'none' for dashboard display."""
        if self._primary.available:
            return "openai"
        if self._fallback.available:
            return "transformers"
        return "none"

    # ── Startup probe ─────────────────────────────────────────────────────

    async def probe(self) -> bool:
        """Try OpenAI first; fall back to Transformers.

        When OpenAI succeeds, the Transformers pipeline is unloaded immediately
        to free memory — it will be reloaded automatically if OpenAI goes away.
        """
        if self._primary.is_configured:
            ok = await self._primary.probe()
            if ok:
                # Free the local model — OpenAI is handling all calls.
                self._fallback.unload()
                logger.info(
                    "LLM backend: OpenAI active",
                    extra={"model": self._primary.model, "url": self._primary.base_url},
                )
                return True

        if self._fallback.is_configured:
            ok = await self._fallback.probe()
            if ok:
                logger.info(
                    "LLM backend: Transformers active",
                    extra={"model": self._fallback.model},
                )
                return ok

        return False

    # ── Chat delegation ───────────────────────────────────────────────────

    async def chat(
        self,
        messages: list[dict[str, Any]],
        expect_json: bool = True,
    ) -> Optional[dict[str, Any] | str]:
        """Prefer OpenAI; fall back to Transformers silently for individual calls."""
        if self._primary.can_attempt:
            result = await self._primary.chat(messages, expect_json)
            if result is not None:
                return result

        if self._fallback.can_attempt:
            return await self._fallback.chat(messages, expect_json)

        return None

    # ── Periodic recheck ──────────────────────────────────────────────────

    async def recheck_primary(self) -> None:
        """Re-probe OpenAI and switch backends if its availability has changed.

        Call this on each news fetch (every ~5 min) to detect when OpenAI comes
        back online after an outage and to detect when it goes away again.
        When switching TO OpenAI, the Transformers pipeline is unloaded.
        When switching TO Transformers, the pipeline is reloaded via probe().
        """
        if not self._primary.is_configured:
            return

        was_available = self._primary.available
        ok = await self._primary.probe()

        if ok and not was_available:
            logger.info("OpenAI back online — switching to OpenAI, unloading local model")
            self._fallback.unload()
        elif not ok and was_available:
            logger.info("OpenAI unavailable — loading local Transformers model as fallback")
            await self._fallback.probe()
