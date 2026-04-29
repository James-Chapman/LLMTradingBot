"""BDD coverage for SwitchingLLMClient — backend selection, fallback, and switching."""

import unittest
from datetime import datetime
from unittest.mock import AsyncMock, MagicMock, patch

from bdd_helpers import BACKEND_DIR  # noqa: F401
from llm.openai_client import OpenAiClient
from llm.switching_client import SwitchingLLMClient
from llm.transformers_client import TransformersClient


def _make_openai(available: bool = False) -> OpenAiClient:
    client = OpenAiClient("http://localhost:1234/v1", "key", "gpt-4o-mini")
    if available:
        client._mark_success()
    return client


def _make_transformers(available: bool = False) -> TransformersClient:
    client = TransformersClient("google/gemma-4-E2B-it")
    if available:
        client._mark_success()
        client._pipeline = MagicMock()  # simulate loaded pipeline
    return client


class SwitchingClientProbeTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN OpenAI is reachable WHEN probe is called THEN OpenAI becomes active and
    # Transformers pipeline is unloaded to free memory.
    async def test_given_openai_reachable_when_probe_then_openai_active_and_transformers_unloaded(self) -> None:
        openai = _make_openai()
        transformers = _make_transformers(available=True)  # already loaded
        switching = SwitchingLLMClient(openai, transformers)

        openai.probe = AsyncMock(side_effect=lambda: (openai._mark_success() or True))

        await switching.probe()

        self.assertEqual(switching.active_backend, "openai")
        self.assertTrue(switching.available)
        # Pipeline should have been unloaded
        self.assertIsNone(transformers._pipeline)
        self.assertFalse(transformers.available)

    # GIVEN OpenAI is unreachable WHEN probe is called THEN Transformers becomes active.
    async def test_given_openai_unreachable_when_probe_then_transformers_active(self) -> None:
        openai = _make_openai()
        transformers = _make_transformers()
        switching = SwitchingLLMClient(openai, transformers)

        openai.probe = AsyncMock(side_effect=lambda: (openai._mark_failed("refused") or False))
        transformers.probe = AsyncMock(side_effect=lambda: (transformers._mark_success() or True))

        result = await switching.probe()

        self.assertTrue(result)
        self.assertEqual(switching.active_backend, "transformers")

    # GIVEN neither backend is reachable WHEN probe is called THEN available is False.
    async def test_given_neither_reachable_when_probe_then_unavailable(self) -> None:
        openai = _make_openai()
        transformers = _make_transformers()
        switching = SwitchingLLMClient(openai, transformers)

        openai.probe = AsyncMock(side_effect=lambda: (openai._mark_failed("refused") or False))
        transformers.probe = AsyncMock(side_effect=lambda: (transformers._mark_failed("load failed") or False))

        result = await switching.probe()

        self.assertFalse(result)
        self.assertFalse(switching.available)
        self.assertEqual(switching.active_backend, "none")


class SwitchingClientChatTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN OpenAI is available WHEN chat is called THEN OpenAI is used (not Transformers).
    async def test_given_openai_available_when_chat_then_openai_used(self) -> None:
        openai = _make_openai(available=True)
        transformers = _make_transformers(available=True)
        switching = SwitchingLLMClient(openai, transformers)

        openai.chat = AsyncMock(return_value={"sentiment": 0.5})
        transformers.chat = AsyncMock(return_value={"sentiment": -0.1})

        result = await switching.chat([{"role": "user", "content": "analyse"}])

        self.assertEqual(result["sentiment"], 0.5)
        openai.chat.assert_called_once()
        transformers.chat.assert_not_called()

    # GIVEN OpenAI returns None WHEN chat is called THEN Transformers is used as fallback.
    async def test_given_openai_returns_none_when_chat_then_transformers_fallback(self) -> None:
        openai = _make_openai(available=True)
        transformers = _make_transformers(available=True)
        switching = SwitchingLLMClient(openai, transformers)

        openai.chat = AsyncMock(return_value=None)
        transformers.chat = AsyncMock(return_value={"sentiment": 0.2})

        result = await switching.chat([{"role": "user", "content": "analyse"}])

        self.assertEqual(result["sentiment"], 0.2)
        transformers.chat.assert_called_once()

    # GIVEN only Transformers is available WHEN chat is called THEN Transformers is used.
    async def test_given_only_transformers_available_when_chat_then_transformers_used(self) -> None:
        openai = _make_openai()  # unavailable
        transformers = _make_transformers(available=True)
        switching = SwitchingLLMClient(openai, transformers)

        transformers.chat = AsyncMock(return_value={"sentiment": 0.3})

        result = await switching.chat([{"role": "user", "content": "analyse"}])

        self.assertEqual(result["sentiment"], 0.3)


class SwitchingClientRecheckTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN OpenAI was down WHEN recheck finds it online THEN Transformers is unloaded.
    async def test_given_openai_was_down_when_recheck_finds_it_online_then_transformers_unloaded(self) -> None:
        openai = _make_openai()  # starts unavailable
        transformers = _make_transformers(available=True)
        switching = SwitchingLLMClient(openai, transformers)

        # Simulate OpenAI coming back online during recheck
        openai.probe = AsyncMock(side_effect=lambda: (openai._mark_success() or True))

        await switching.recheck_primary()

        self.assertTrue(openai.available)
        # Transformers should be unloaded since OpenAI is back
        self.assertIsNone(transformers._pipeline)

    # GIVEN OpenAI was up WHEN recheck finds it gone THEN Transformers is reloaded.
    async def test_given_openai_was_up_when_recheck_finds_it_gone_then_transformers_reloaded(self) -> None:
        openai = _make_openai(available=True)
        transformers = _make_transformers()  # unloaded (OpenAI was primary)
        switching = SwitchingLLMClient(openai, transformers)

        # OpenAI fails during recheck
        openai.probe = AsyncMock(side_effect=lambda: (openai._mark_failed("timeout") or False))
        # Transformers reloads successfully
        transformers.probe = AsyncMock(side_effect=lambda: (transformers._mark_success() or True))

        await switching.recheck_primary()

        self.assertFalse(openai.available)
        self.assertTrue(transformers.available)

    # GIVEN OpenAI is not configured WHEN recheck is called THEN nothing happens.
    async def test_given_openai_not_configured_when_recheck_then_noop(self) -> None:
        openai = OpenAiClient("", "", "")  # not configured
        transformers = _make_transformers(available=True)
        switching = SwitchingLLMClient(openai, transformers)

        openai.probe = AsyncMock()

        await switching.recheck_primary()

        openai.probe.assert_not_called()
        self.assertTrue(transformers.available)


class SwitchingClientPropertyTests(unittest.TestCase):
    # GIVEN OpenAI is active WHEN llm_model is read THEN it contains "(OpenAI)".
    def test_given_openai_active_when_llm_model_read_then_openai_label(self) -> None:
        openai = _make_openai(available=True)
        transformers = _make_transformers()
        switching = SwitchingLLMClient(openai, transformers)

        self.assertIn("OpenAI", switching.llm_model)

    # GIVEN Transformers is active WHEN llm_model is read THEN it contains "(Local)".
    def test_given_transformers_active_when_llm_model_read_then_local_label(self) -> None:
        openai = _make_openai()
        transformers = _make_transformers(available=True)
        switching = SwitchingLLMClient(openai, transformers)

        self.assertIn("Local", switching.llm_model)


if __name__ == "__main__":
    unittest.main()
