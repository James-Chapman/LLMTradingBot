"""
Configuration settings using Pydantic v2
"""

from typing import List, Optional

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class BotSettings(BaseSettings):
    """Main bot configuration"""

    model_config = SettingsConfigDict(
        env_file=".env",
        case_sensitive=True,
        extra="ignore",
    )

    # App settings
    app_name: str = Field(default="Kraken Trading Bot", validation_alias="APP_NAME")
    version: str = Field(default="0.5.3", validation_alias="VERSION")
    debug: bool = Field(default=False, validation_alias="DEBUG")

    # Server settings
    host: str = Field(default="127.0.0.1", validation_alias="HOST")
    port: int = Field(default=8000, validation_alias="PORT")

    # Database
    database_url: str = Field(default="sqlite:///./trading_bot.db", validation_alias="DATABASE_URL")

    # Trading settings
    base_currency: str = Field(default="EUR", validation_alias="BASE_CURRENCY")
    starting_capital: float = Field(default=500.0, validation_alias="STARTING_CAPITAL")
    target_trade_amount: float = Field(default=100.0, validation_alias="TARGET_TRADE_AMOUNT")
    max_loss_per_trade_percent: float = Field(
        default=5.0, validation_alias="MAX_LOSS_PER_TRADE_PERCENT"
    )
    max_daily_loss_percent: float = Field(default=5.0, validation_alias="MAX_DAILY_LOSS_PERCENT")
    min_trade_size: float = Field(default=50.0, validation_alias="MIN_TRADE_SIZE")
    stop_loss_pct: float = Field(default=0.05, validation_alias="STOP_LOSS_PCT")
    trailing_stop_pct: float = Field(default=0.03, validation_alias="TRAILING_STOP_PCT")
    momentum_lookback_ticks: int = Field(default=10, validation_alias="MOMENTUM_LOOKBACK_TICKS")
    fee_and_slippage: float = Field(default=0.0036, validation_alias="FEE_AND_SLIPPAGE")
    min_signal_confidence: float = Field(default=0.65, validation_alias="MIN_SIGNAL_CONFIDENCE")
    llm_veto_threshold: float = Field(default=0.70, validation_alias="LLM_VETO_THRESHOLD")
    min_24h_volume: float = Field(default=0.0, validation_alias="MIN_24H_VOLUME")
    alert_webhook_url: Optional[str] = Field(default=None, validation_alias="ALERT_WEBHOOK_URL")

    # Modes
    trading_mode: str = Field(
        default="manual",
        pattern="^(manual|semi_automated|fully_automated)$",
        validation_alias="TRADING_MODE",
    )
    trading_environment: str = Field(
        default="paper",
        pattern="^(paper|live)$",
        validation_alias="TRADING_ENVIRONMENT",
    )

    # Kraken settings
    kraken_api_key: Optional[str] = Field(default=None, validation_alias="KRAKEN_API_KEY")
    kraken_api_secret: Optional[str] = Field(default=None, validation_alias="KRAKEN_API_SECRET")

    # News sources
    news_sources: List[str] = Field(
        default_factory=lambda: ["CoinDesk", "CoinNews", "CoinWeek"],
        validation_alias="NEWS_SOURCES",
    )

    # Universe settings
    fixed_markets: List[str] = Field(
        default_factory=lambda: ["BTC/EUR", "ETH/EUR"],
        validation_alias="FIXED_MARKETS",
    )
    dynamic_universe_source: str = Field(
        default="coinmarketcap", validation_alias="DYNAMIC_UNIVERSE_SOURCE"
    )
    max_eth_ecosystem_coins: int = Field(default=10, validation_alias="MAX_ETH_ECOSYSTEM_COINS")

    # Local LLM — Transformers (HuggingFace model loaded in-process)
    transformers_llm_model: str = Field(default="", validation_alias="TRANSFORMERS_LLM_MODEL")
    transformers_timeout: int = Field(default=60, validation_alias="TRANSFORMERS_TIMEOUT")

    # LLM-only strategy concurrency cap
    llm_only_max_concurrency: int = Field(default=3, validation_alias="LLM_ONLY_MAX_CONCURRENCY")

    # CORS
    cors_origins: List[str] = Field(
        default_factory=lambda: ["http://localhost:8000", "http://127.0.0.1:8000"],
        validation_alias="CORS_ORIGINS",
    )

    # Logging
    log_level: str = Field(default="INFO", validation_alias="LOG_LEVEL")
    log_file: str = Field(default="trading_bot.log", validation_alias="LOG_FILE")


# Global settings instance
settings = BotSettings()
