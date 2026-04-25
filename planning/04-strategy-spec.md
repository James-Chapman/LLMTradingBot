# Strategy Specification

## 1. Strategy Framework Goal

Design a multi-strategy framework that can evaluate market data and news signals across multiple horizons while keeping risk management centralized and mandatory.

## 2. Strategy Principles

- No strategy bypasses the risk engine.
- No strategy places live orders directly.
- News should inform but not dominate decision-making without confirmation.
- Strategies must emit rationale, not just buy/sell labels.
- A strategy may decline to trade when data quality or conditions are weak.

## 3. Strategy Categories

The system should support these categories, even if not all are implemented in the first coding pass:

### 3.1 Trend-Following

Use market structure and momentum to align with sustained price movement.

Inputs:

- candles
- moving averages or trend filters
- volatility measures
- news confirmation or contradiction

### 3.2 Mean Reversion

Look for temporary dislocations likely to revert.

Inputs:

- deviation from rolling mean
- volatility bands
- spread awareness
- event filter to avoid fading major news shocks

### 3.3 Event-Driven

React to detected news events affecting specific assets or the broader crypto market.

Inputs:

- headline sentiment
- summary signals
- classified events
- recency and source confidence

### 3.4 Regime Filter

Determine which strategies are allowed to act under current conditions.

Possible regime states:

- trending
- ranging
- volatile
- news-shocked
- low-liquidity

## 4. Multi-Horizon Support

The framework should be capable of supporting:

- intraday
- swing
- longer-horizon positioning

Recommended v1 design:

Build the framework to support multiple horizons, but limit the first implemented strategy set to a manageable subset until the execution and paper engine are proven stable.

## 5. Required Output Schema

Each strategy should emit a structured `TradeIdea` with at least:

- `strategy_id`
- `market`
- `time_horizon`
- `direction`
- `thesis`
- `supporting_signals`
- `confidence`
- `entry_plan`
- `exit_plan`
- `stop_or_invalidation`
- `position_sizing_proposal`
- `mode_eligibility`

## 6. News Signal Design

News processing should generate structured features rather than raw prose only.

Suggested fields:

- `asset_mentions`
- `headline_sentiment`
- `summary_sentiment`
- `event_type`
- `event_severity`
- `source_name`
- `published_at`
- `confidence`

Example event types:

- regulatory
- exchange
- protocol
- security
- macro
- partnership
- listing
- exploit
- network_outage

## 7. Position Sizing Rules

Sizing proposals must be constrained by:

- max `5%` loss of current equity per trade
- minimum viable order size
- estimated fees
- estimated slippage
- current spread

Strategies may propose a size, but the risk engine must finalize or reject it.

## 8. Entry and Exit Requirements

Every strategy must define:

- exact entry trigger
- exact invalidation logic
- take-profit or exit logic
- timeout or stale-signal expiry

No strategy should be allowed to generate "buy because it feels bullish" style outputs.

## 9. Strategy Promotion Path

Recommended lifecycle:

1. Backtest or replay evaluation
2. Paper trading only
3. Performance review
4. Selective live enablement by strategy and market
5. Ongoing monitoring

## 10. Open Questions

- Should all strategies be allowed on all markets?
- What minimum paper-trading performance threshold is required before live enablement?
- Should news-only trades be forbidden unless market structure confirms direction?
- How should short logic be represented if spot-only execution limits short availability?
