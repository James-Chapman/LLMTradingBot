"""
OpenAI-compatible external LLM client.

Connects to any endpoint that speaks the OpenAI chat-completions API —
OpenAI, Azure OpenAI, LM Studio, llama.cpp server, Ollama with OpenAI compat, etc.
Configure OPENAI_BASE_URL, OPENAI_API_KEY, and OPENAI_MODEL in .env.
"""

import json
from datetime import datetime
from typing import Any, Callable, Optional

import httpx

from llm.common import CircuitBreakerMixin, loads_model_json, messages_to_prompt, utc_now
from observability.logging import get_logger

logger = get_logger("openai_client")


class OpenAiClient(CircuitBreakerMixin):
    """Client for any OpenAI-compatible chat-completions endpoint."""

    def __init__(
        self,
        base_url: str,
        api_key: str,
        model: str,
        timeout: int = 30,
        clock: Callable[[], datetime] = utc_now,
    ):
        self.base_url = base_url.rstrip("/") if base_url else ""
        self.api_key = api_key or ""
        self.model = model or ""
        self.timeout = timeout
        self._init_circuit_breaker(clock)

    @property
    def is_configured(self) -> bool:
        """Return True when both base_url and model are set."""
        return bool(self.base_url and self.model)

    @property
    def llm_model(self) -> str:
        return f"{self.model} (OpenAI)" if self.model else ""

    def _headers(self) -> dict[str, str]:
        h: dict[str, str] = {"Content-Type": "application/json"}
        if self.api_key:
            h["Authorization"] = f"Bearer {self.api_key}"
        return h

    def _log_failure(self, reason: str) -> None:
        """Log a failed OpenAI-compatible client attempt."""
        # Only log a warning for critical failures (e.g., connection issues),
        # otherwise, just update the state to prevent excessive logging on expected API errors (like 400).
        if "connection" in reason.lower() or "timeout" in reason.lower():
            logger.warning(f"OpenAI client unavailable: {reason}; retry in {self._retry_delay}")
        else:
            logger.info(f"OpenAI client failed (non-critical): {reason}. Circuit tripped.")

    async def probe(self) -> bool:
        """Test connectivity and validate the response contains a 'choices' field."""
        if not self.is_configured:
            return False
        try:
            logger.debug("Probing OpenAI endpoint for availability...", extra={"url": self.base_url})
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

        payload = {
            "model": self.model,
            "messages": messages,
        }
        prompt = messages_to_prompt(messages)

        content: str = ""
        try:
            async with httpx.AsyncClient(timeout=self.timeout) as client:
                logger.debug(f"Sending LLM request prompt: {json.dumps(prompt)}")
                resp = await client.post(
                    f"{self.base_url}/chat/completions",
                    json=payload,
                    headers=self._headers(),
                )
                resp.raise_for_status()
            data = resp.json()
            logger.debug(f"Received LLM response: {json.dumps(data)}")
            content = data["choices"][0]["message"]["content"]
            self._mark_success()
            if expect_json:
                return loads_model_json(content, "OpenAI response")
            return content
        except json.JSONDecodeError as e:
            logger.warning(f"OpenAI returned non-JSON: {e}", extra={"content": content[:200]})
            return None
        except Exception as e:
            self._mark_failed(str(e))
            return None
