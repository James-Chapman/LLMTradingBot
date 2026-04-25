# Risk Register

## Scoring

- Probability: `Low`, `Medium`, `High`
- Impact: `Low`, `Medium`, `High`

## Risks

| ID | Risk | Probability | Impact | Mitigation |
|---|---|---|---|---|
| R1 | News source access may be blocked, unstable, or disallowed by terms of use. | High | High | Validate each source's allowed ingestion path before implementation. Design pluggable news adapters. |
| R2 | The dynamic Ethereum ecosystem list may be inconsistent across providers. | High | Medium | Require a single canonical source and persist universe snapshots with timestamps and provenance. |
| R3 | Kraken may not support all dynamically selected ETH ecosystem assets in EUR pairs. | High | High | Add market mapping and eligibility checks. Restrict trading to Kraken-supported pairs only. |
| R4 | The request for short exposure may not fit a strict spot-only v1 scope. | High | High | Add a capability matrix. If shorting is unsupported, disable it explicitly rather than emulating unsafe behavior. |
| R5 | News-driven signals may be noisy, late, or contradictory. | High | Medium | Keep news as one input among several. Require confidence thresholds and source attribution. |
| R6 | Paper-trading results may diverge materially from live execution. | High | High | Simulate fees, slippage, and latency. Track paper-vs-live drift once live mode is introduced. |
| R7 | A bug in mode or toggle handling could route a paper-only strategy into live execution. | Medium | High | Fail closed. Require explicit live enablement by strategy and market. Add visible mode banners and audit logs. |
| R8 | Risk limits based on current equity may be calculated inconsistently across components. | Medium | High | Centralize risk calculations in one engine and persist equity snapshots used for each decision. |
| R9 | Small starting capital may make fees and spread dominate performance. | High | Medium | Include minimum trade size checks, fee-aware sizing, and "do not trade" conditions. |
| R10 | Multi-strategy operation may lead to overexposure or correlated positions. | Medium | High | Add aggregate exposure limits and cross-strategy coordination in the risk layer. |
| R11 | Local desktop operation increases operational risk if the app is closed or the machine sleeps. | Medium | Medium | Surface health status clearly and document operational limitations. |
| R12 | Operator approval workflows in both UI and CLI could diverge. | Medium | Medium | Use one approval service and shared validation rules beneath both interfaces. |
| R13 | LLM-generated summaries or sentiment labels may hallucinate or overstate confidence. | Medium | High | Keep raw source text references, require bounded output schemas, and separate extraction from trading decisions. |
| R14 | Regulatory or tax obligations in the UK may affect usable workflows, records, or reporting. | Medium | High | Treat legal/tax handling as a separate compliance workstream before live rollout. |
| R15 | Secrets stored locally could be exposed through bad configuration or logs. | Medium | High | Use local secret storage, redact logs, and isolate live credentials from paper mode. |

## Priority Risks To Resolve Early

- `R1` News source ingestion feasibility
- `R3` Kraken market mapping for the dynamic universe
- `R4` Short support under spot-only constraints
- `R7` Paper/live toggle safety
- `R9` Fee impact at low capital
- `R14` UK compliance and record-keeping expectations
