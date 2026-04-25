# Project Brief

## Project Name

Kraken News-Aware Trading Bot

## Purpose

Build a local desktop trading bot for personal use that analyzes Kraken market data and crypto news to generate trading decisions for spot markets quoted in EUR.

The system must support three operating modes within one application:

- Manual signals
- Semi-automated trading with approval
- Fully automated trading

The first release should prioritize paper trading, with a controlled path to live trading later via per-strategy and per-market toggles.

## Primary Objective

Attempt profitable trading with strict risk controls.

## Non-Goals

- Guaranteed profit
- High-frequency trading
- Margin or leveraged trading in v1
- Mobile-first experience
- Multi-exchange support in v1
- Paid APIs or paid third-party services in v1

## Intended User

Single personal operator in the UK.

## Trading Scope

- Exchange: Kraken
- Quote currency: EUR
- Core markets: `BTC/EUR`, `ETH/EUR`
- Additional markets: ETH itself plus the top 10 Ethereum ecosystem coins by market cap, resolved dynamically at runtime
- Position types: Spot only
- Directionality: Long and short are requested, but short capability is exchange- and instrument-dependent and must not be assumed available for every in-scope market in v1

## Decision Inputs

- Live market data from Kraken
- News feeds from `CoinDesk`, `CoinNews`, and `CoinWeek`
- Headline sentiment
- Article summaries
- Event detection

## Required Modes

### Manual Signals

The bot produces market analysis, trade ideas, and operator-readable rationale, but does not place orders.

### Semi-Automated

The bot prepares candidate trades and requires explicit human approval before order placement. Approvals must be possible through both:

- In-app buttons
- CLI prompts

### Fully Automated

The bot may place trades automatically, but only where both the strategy and the market have been explicitly enabled for live execution.

## Risk Constraints

- Starting capital assumption for planning: `EUR 100`
- Max loss per trade: `5%` of current account equity
- Max daily loss: `5%` of current account equity
- Paper trading must simulate fees, slippage, and latency

## Platform Scope

- Windows
- Linux

## Delivery Intent

Produce a maintainable local desktop application and supporting planning artifacts suitable for LLM-guided implementation.

## Known Ambiguities

- The exact source used to resolve the "top 10 Ethereum ecosystem coins by market cap" is not yet selected.
- "Short" support may require a separate design track because spot-only trading does not always imply native short capability.
- The feasibility and legality of using the named news sources via scraping or feeds is not yet confirmed.
