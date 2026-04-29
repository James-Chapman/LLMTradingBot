"""
Async Transformers client.

Loads a local Hugging Face model via Transformers pipeline.
Falls back gracefully when the model fails to load or generate.
"""

import asyncio
import json
from datetime import datetime
from typing import Any, Callable, Optional

from transformers import pipeline

from llm.common import CircuitBreakerMixin, loads_model_json, messages_to_prompt, utc_now
from observability.logging import get_logger

logger = get_logger("transformers")


class TransformersClient(CircuitBreakerMixin):
    def __init__(
        self,
        model: str,
        timeout: int = 15,
        clock: Callable[[], datetime] = utc_now,
    ):
        self.model = model
        self.timeout = timeout
        self._init_circuit_breaker(clock)
        self._pipeline = None  # Loaded on first probe

    @property
    def is_configured(self) -> bool:
        """Return True when a model ID has been set."""
        return bool(self.model)

    @property
    def llm_model(self) -> str:
        """Return the model name with a Local suffix for dashboard display."""
        return f"{self.model} (Local)" if self.model else ""

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

        prompt = messages_to_prompt(messages, append_json_instruction=expect_json)

        raw_output = ""
        try:
            # max_new_tokens limits generated tokens only (not input length).
            # return_full_text=False returns only the model's new output so JSON
            # extraction never matches { characters embedded in the prompt itself.
            outputs = await asyncio.to_thread(
                self._pipeline,
                prompt,
                max_new_tokens=512,
                num_return_sequences=1,
                return_full_text=False,
            )
            raw_output = outputs[0]["generated_text"].strip()
            self._mark_success()
            if expect_json:
                return loads_model_json(raw_output, "Transformers JSON response")
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

    def _log_failure(self, reason: str) -> None:
        """Log a failed Transformers client attempt."""
        logger.warning(f"Transformers unavailable: {reason}; retry in {self._retry_delay}")

    def unload(self) -> None:
        """Release the in-process model pipeline to free GPU/CPU memory.

        Resets the circuit to closed so the next probe() call reloads the model
        when it is needed again (e.g. OpenAI becomes unavailable).
        """
        if self._pipeline is not None:
            del self._pipeline
            self._pipeline = None
            self.available = False
            self._circuit_state = "closed"
            logger.info(f"Transformers model unloaded — model: {self.model}")
