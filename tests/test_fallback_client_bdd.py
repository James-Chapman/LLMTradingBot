"""BDD coverage for the FallbackLLMClient probe-and-lock-in chain."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.fallback_client import FallbackLLMClient


class FakeLLMClient:
    """Controllable stand-in for any LLM client."""

    def __init__(self, name: str, probe_result: bool, chat_result=None) -> None:
        self.name = name
        self._probe_result = probe_result
        self._chat_result = chat_result
        self._available = False
        self._can_attempt = probe_result  # open circuit when probe would fail

    @property
    def available(self) -> bool:
        return self._available

    @property
    def can_attempt(self) -> bool:
        return self._can_attempt

    @property
    def circuit_state(self) -> str:
        return "closed" if self._can_attempt else "open"

    async def probe(self) -> bool:
        self._available = self._probe_result
        self._can_attempt = self._probe_result
        return self._probe_result

    async def chat(self, messages: list[dict], expect_json: bool = True):
        if not self._can_attempt:
            return None
        return self._chat_result

    def open_circuit(self) -> None:
        """Simulate the circuit opening after a failure."""
        self._available = False
        self._can_attempt = False


class FallbackClientProbeBDDTests(unittest.IsolatedAsyncioTestCase):

    # GIVEN LM Studio is available WHEN probe is called
    # THEN LM Studio is the active client.
    async def test_given_lm_studio_available_when_probe_then_lm_studio_is_active(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=True, chat_result={"ok": True})
        ol = FakeLLMClient("Ollama",   probe_result=False)
        tr = FakeLLMClient("Transformers", probe_result=False)
        client = FallbackLLMClient([lm, ol, tr])

        result = await client.probe()

        self.assertTrue(result)
        self.assertTrue(client.available)
        self.assertIs(client._active, lm)

    # GIVEN LM Studio is unavailable and Ollama is available WHEN probe is called
    # THEN Ollama is the active client.
    async def test_given_lm_studio_unavailable_ollama_available_when_probe_then_ollama_is_active(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=False)
        ol = FakeLLMClient("Ollama",   probe_result=True, chat_result={"ok": True})
        tr = FakeLLMClient("Transformers", probe_result=False)
        client = FallbackLLMClient([lm, ol, tr])

        result = await client.probe()

        self.assertTrue(result)
        self.assertTrue(client.available)
        self.assertIs(client._active, ol)

    # GIVEN LM Studio and Ollama are unavailable WHEN probe is called
    # THEN Transformers is the active client.
    async def test_given_lm_studio_and_ollama_unavailable_when_probe_then_transformers_is_active(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=False)
        ol = FakeLLMClient("Ollama",   probe_result=False)
        tr = FakeLLMClient("Transformers", probe_result=True, chat_result={"ok": True})
        client = FallbackLLMClient([lm, ol, tr])

        result = await client.probe()

        self.assertTrue(result)
        self.assertTrue(client.available)
        self.assertIs(client._active, tr)

    # GIVEN all backends are unavailable WHEN probe is called
    # THEN active is None and available is False.
    async def test_given_all_unavailable_when_probe_then_no_active_client(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=False)
        ol = FakeLLMClient("Ollama",   probe_result=False)
        tr = FakeLLMClient("Transformers", probe_result=False)
        client = FallbackLLMClient([lm, ol, tr])

        result = await client.probe()

        self.assertFalse(result)
        self.assertFalse(client.available)
        self.assertIsNone(client._active)


class FallbackClientChatBDDTests(unittest.IsolatedAsyncioTestCase):

    # GIVEN LM Studio is the active client WHEN chat is called
    # THEN the response comes from LM Studio.
    async def test_given_active_client_when_chat_then_response_from_active(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=True, chat_result={"source": "lm_studio"})
        ol = FakeLLMClient("Ollama",   probe_result=False)
        tr = FakeLLMClient("Transformers", probe_result=False)
        client = FallbackLLMClient([lm, ol, tr])
        await client.probe()

        result = await client.chat([{"role": "user", "content": "test"}])

        self.assertEqual(result["source"], "lm_studio")

    # GIVEN LM Studio was locked in but its circuit opens WHEN chat is called
    # THEN the client promotes to Ollama and returns its response.
    async def test_given_active_circuit_opens_when_chat_then_promotes_to_next(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=True, chat_result={"source": "lm_studio"})
        ol = FakeLLMClient("Ollama",   probe_result=True, chat_result={"source": "ollama"})
        tr = FakeLLMClient("Transformers", probe_result=False)
        client = FallbackLLMClient([lm, ol, tr])
        await client.probe()
        self.assertIs(client._active, lm)

        # Simulate LM Studio failing after lock-in
        lm.open_circuit()

        result = await client.chat([{"role": "user", "content": "test"}])

        self.assertEqual(result["source"], "ollama")
        self.assertIs(client._active, ol)

    # GIVEN no active client WHEN chat is called THEN None is returned.
    async def test_given_no_active_client_when_chat_then_returns_none(self) -> None:
        lm = FakeLLMClient("LMStudio", probe_result=False)
        ol = FakeLLMClient("Ollama",   probe_result=False)
        tr = FakeLLMClient("Transformers", probe_result=False)
        client = FallbackLLMClient([lm, ol, tr])
        await client.probe()

        result = await client.chat([{"role": "user", "content": "test"}])

        self.assertIsNone(result)


if __name__ == "__main__":
    unittest.main()
