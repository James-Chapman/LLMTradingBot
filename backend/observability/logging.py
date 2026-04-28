"""
Structured logging setup
"""

import json
import logging
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


class JSONFormatter(logging.Formatter):
    """JSON formatter for structured logging"""

    def format(self, record: logging.LogRecord) -> str:
        log_entry = {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
            "module": record.module,
            "function": record.funcName,
            "line": record.lineno,
        }

        # Add extra fields if present
        if hasattr(record, "extra_fields"):
            log_entry.update(record.extra_fields)

        # Add exception info if present
        if record.exc_info:
            log_entry["exception"] = self.formatException(record.exc_info)

        return json.dumps(log_entry)


def setup_logging(log_level: str = "INFO", log_file: str = "trading_bot.log") -> None:
    """Setup structured logging"""

    # Create logger
    logger = logging.getLogger("trading_bot")
    logger.setLevel(getattr(logging, log_level.upper()))

    # Remove existing handlers
    for handler in logger.handlers[:]:
        logger.removeHandler(handler)

    # JSON formatter
    formatter = JSONFormatter()

    # Console handler
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setFormatter(formatter)
    logger.addHandler(console_handler)

    # File handler
    log_path = Path(log_file)
    file_handler = logging.FileHandler(log_path)
    file_handler.setFormatter(formatter)
    logger.addHandler(file_handler)

    # Prevent duplicate logs
    logger.propagate = False

    # Dedicated LLM log — Transformers client and LLM analyser write here in
    # addition to the main log so LLM activity can be tailed separately.
    llm_log_path = log_path.with_name("llm.log")
    llm_handler = logging.FileHandler(llm_log_path)
    llm_handler.setFormatter(formatter)
    for _name in ("trading_bot.transformers", "trading_bot.llm_analyser"):
        _child = logging.getLogger(_name)
        for _h in list(_child.handlers):
            _child.removeHandler(_h)
        _child.addHandler(llm_handler)


def get_logger(name: str) -> logging.Logger:
    """Get a logger instance"""
    return logging.getLogger(f"trading_bot.{name}")


def log_event(event_type: str, **kwargs: Any) -> None:
    """Log a structured event"""
    logger = get_logger("events")
    extra = {"extra_fields": {"event_type": event_type, **kwargs}}
    logger.info(f"Event: {event_type}", extra=extra)
