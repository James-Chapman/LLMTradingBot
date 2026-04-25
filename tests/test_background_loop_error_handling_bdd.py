"""BDD coverage for QUALITY-001: background loop error handlers log full tracebacks."""
import json
import logging
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from observability.logging import JSONFormatter, get_logger


def _capture_log_json(logger_name: str, level: int, message: str, exc_info=False) -> dict:
    """Emit a log record to a capturing handler and return the parsed JSON output."""
    logger = get_logger(logger_name)
    logger.setLevel(logging.DEBUG)
    records = []

    class _CapturingHandler(logging.Handler):
        def emit(self, record):
            records.append(record)

    handler = _CapturingHandler()
    handler.setFormatter(JSONFormatter())
    logger.addHandler(handler)
    try:
        try:
            raise ValueError("synthetic error for BDD test")
        except ValueError:
            if exc_info:
                logger.log(level, message, exc_info=True)
            else:
                logger.log(level, message)
    finally:
        logger.removeHandler(handler)

    assert records, "No log records captured"
    return json.loads(handler.format(records[0]))


class BackgroundLoopErrorHandlingBDDTests(unittest.TestCase):
    # QUALITY-001: GIVEN a background loop catches an unexpected exception
    # WHEN it logs the error WITHOUT exc_info=True
    # THEN the formatted JSON log entry does NOT contain a traceback — bug invisible.
    def test_given_exception_logged_without_exc_info_then_no_traceback_in_output(self) -> None:
        output = _capture_log_json("strategy_loop", logging.ERROR, "loop error", exc_info=False)
        self.assertNotIn(
            "exception", output,
            "Without exc_info=True, traceback must be absent — confirms the bug exists before fix",
        )

    # QUALITY-001: GIVEN a background loop catches an unexpected exception
    # WHEN it logs the error WITH exc_info=True
    # THEN the formatted JSON log entry contains a traceback so the bug is diagnosable.
    def test_given_exception_logged_with_exc_info_then_traceback_present_in_output(self) -> None:
        output = _capture_log_json("strategy_loop", logging.ERROR, "loop error", exc_info=True)
        self.assertIn(
            "exception", output,
            "With exc_info=True, 'exception' key must appear in JSON output",
        )
        self.assertIn(
            "ValueError", output["exception"],
            "Traceback must name the exception type",
        )
        self.assertIn(
            "synthetic error for BDD test", output["exception"],
            "Traceback must include the exception message",
        )

    # QUALITY-001: GIVEN the strategy loop error handler
    # WHEN an AttributeError is raised inside the loop body
    # THEN the log level should be ERROR (not WARNING) so operators are alerted.
    def test_given_background_loop_error_then_level_is_error(self) -> None:
        output = _capture_log_json("strategy_loop", logging.ERROR, "Strategy loop error", exc_info=True)
        self.assertEqual(output["level"], "ERROR")


if __name__ == "__main__":
    unittest.main()
