# R10 Composition Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove duplicated T-Invest connection and strategy-risk composition while preserving all existing sandbox and backtest scenarios.

**Architecture:** Keep the T-Invest opener private to `internal/cli`; command-specific validation and error wrapping stay at their existing call sites. Put the shared, pure strategy-risk mapping in a narrow `internal/composition` package consumed by live CLI and backtest app. Neither change creates a generic multi-exchange factory or changes trading rules.

**Tech Stack:** Go 1.24, Cobra CLI, existing T-Invest adapter, `internal/config`, `internal/risk`, `internal/domain`.

**Spec:** [`docs/roadmap/refactoring.md` R10](../../roadmap/refactoring.md#r10-устранение-дублирования-composition-кода); [`docs/architecture/overview.md`](../../architecture/overview.md).

**Status:** Completed on `main`; independent task and whole-change reviews passed. The live sandbox round trip remains open.

## Global Constraints

- Work on the existing `main` checkout as the user directed; do not create a branch. Base is `0b4b9eb`.
- Preserve the unrelated user-owned `configs/example.yaml` change. Stage no files while implementing.
- T-Invest remains sandbox-only; no production endpoint, generic exchange factory, real API calls, credentials reads or mutations in tests.
- Keep command-specific prerequisites, validation order and existing error wrappers where feasible. In particular, agent/preflight storage checks precede credential lookup; smoke-test confirmation and quantity checks precede connection; data interval/bounds validation precedes network connection.
- Preserve optional account ID for list/create/data, explicit pay-in account selection, and configured account ID for agent/preflight/history/smoke. Adapter `Name` is the configured exchange alias, not the broker account ID.
- Live and backtest continue to share the same risk rules but may retain mode-specific policy: live rejects `max_daily_loss.action != pause` before composition; backtest currently accepts validated `close_and_pause`. Do not alter that policy in R10.
- Live execution trading-day attribution must use the exact policy placed in its `risk.Config`; backtest still normalizes initial-cash asset before building risk. Never use `float64` for money.
- No schema, public configuration, exchange-port, retry, mutation or runtime behavior changes. Never set or use `GOCACHE`.

## File Map

- `internal/cli/tinvest_connection.go`: one private sandbox-only T-Invest config builder/opener, including alias, token env, optional supplied account ID, and CA resolution.
- `internal/cli/tinvest_connection_test.go`: pure config construction and policy tests; no network.
- Existing CLI commands: replace direct `tinvest.Open` at agent, preflight, history probe, account list/create/pay-in/smoke, and data download with the opener while retaining their surrounding validation and error wrappers.
- `internal/composition/risk.go`: one shared mapping from `config.StrategyConfig` and already-resolved settlement asset to `risk.Config`, including trading-day policy and limits.
- `internal/composition/risk_test.go`: parity and failure-path tests.
- `internal/cli/agent.go` and `internal/app/backtest.go`: use the shared risk mapping; live `TradingDayKey` uses `riskConfig.TradingDay`.
- `docs/architecture/overview.md`, `docs/roadmap/refactoring.md`, `docs/roadmap/current.md`, `docs/README.md`: describe the actual shared composition boundary and R10 status.

### Task 1: Centralize configured T-Invest opening

**Files:** Create `internal/cli/tinvest_connection.go`, `internal/cli/tinvest_connection_test.go`; modify `internal/cli/agent.go`, `agent_preflight.go`, `agent_history_probe.go`, `account.go`, `account_smoke.go`, `data.go`.

**Interfaces:** Produce private `configuredSandboxTInvestConfig(configPath, exchangeID string, exchangeConfig config.ExchangeConfig, accountID string) (tinvest.Config, error)` and `openConfiguredSandboxTInvest(ctx context.Context, configPath, exchangeID string, exchangeConfig config.ExchangeConfig, accountID string) (*tinvest.Adapter, error)`. The first is testable without network; the second is the only CLI call to `tinvest.Open`. Both enforce `Type == "tinvest" && Sandbox && !AllowLiveTrading`; callers keep their earlier, command-specific errors and guard order. `accountID == ""` means accountless adapter for list/create/data/pay-in.

- [x] Step 1: Add a table test of `configuredSandboxTInvestConfig`: sandbox config with `TokenEnv="R10_TEST_TOKEN"`, relative `CACertPath="certs/ca.pem"`, alias `sandbox-main`, and supplied `account-1` yields `Name`, token, account and config-relative CA exactly; empty account is permitted; non-T-Invest, non-sandbox and allow-live configs fail before reading token; missing/blank token fails without exposing its value. Run `go test -count=1 -timeout 60s ./internal/cli -run TestConfiguredSandboxTInvestConfig` and record the expected red result before implementing.
- [x] Step 2: Implement the two helpers. The opener calls `tinvest.Open` exactly once with the built config; never accepts an endpoint override. Keep `requiredEnvironment` and `resolveConfigPath` behavior. Use a named `sandboxTInvest` predicate in existing early guards if that removes duplicated policy expressions without changing command-specific messages.
- [x] Step 3: Replace all eight CLI `tinvest.Open` expressions with `openConfiguredSandboxTInvest`. Keep token/account lookups before later command-specific validation where they currently occur; if data needs an early token-presence check before parsing interval/bounds, retain only that check and let the shared builder construct the final adapter config. Keep existing `open T-Invest`, `preflight connect` and `history probe connect` wrappers, and raw errors for account/data commands.
- [x] Step 4: Run `go test -count=1 -timeout 60s ./internal/cli ./internal/config`, `rg -n 'tinvest\.Open' internal/cli`, `gofmt -l` on touched Go files, and inspect the diff for new access to live mode or changed prerequisite order. The `rg` result must show only the one opener.
- [x] Step 5: Self-review all CLI call sites. Do not call sandbox or read the user's environment token; tests use synthetic names and `t.Setenv` only.

### Task 2: Share live/backtest risk and trading-day composition

**Files:** Create `internal/composition/risk.go`, `internal/composition/risk_test.go`; modify `internal/cli/agent.go`, `internal/app/backtest.go`, `docs/architecture/overview.md`, `docs/roadmap/refactoring.md`, `docs/roadmap/current.md`, `docs/README.md`.

**Interfaces:** Produce `composition.BuildStrategyRisk(strategy config.StrategyConfig, settlementAsset string) (risk.Config, error)`. It creates exactly one `risk.TradingDayPolicy` and maps `MaxPositionValue`, `MaxDailyLoss.Limit` and `PnLMode`; it does not interpret `Action`, normalize the supplied settlement asset, or instantiate a risk evaluator. The returned `risk.Config.TradingDay` is also the live binding's execution-day policy. Use a small `errors.As`-compatible policy-phase error so live retains its distinct `strategy %q trading day: %w` diagnostic; limit-conversion errors retain `strategy %q risk: %w`.

- [x] Step 1: Add `TestBuildStrategyRiskLiveBacktestParity` using one strategy with `Europe/Moscow`, a non-midnight reset, both decimal limits and `PnLTotal`. Compare the built live config (resolved instrument asset) with the built backtest config (normalized initial-cash asset): strategy ID, settlement asset, amounts, modes, and trading-day key/start/end immediately before/at/after reset must match. Add failure tests for bad trading-day timezone and malformed limit amounts. Run `go test -count=1 -timeout 60s ./internal/composition` and record the expected red result.
- [x] Step 2: Implement `BuildStrategyRisk` by moving the common mapping once, preserving the order `NewTradingDayPolicy` then position limit then daily limit. Return a zero config on error; preserve wrapped causes. The helper must remain independent of CLI, backtest runner, T-Invest SDK, storage and clocks.
- [x] Step 3: In `runAgent`, replace local policy construction and `buildLiveRiskConfig` with the shared builder; use `riskConfig.TradingDay.At(at).Key` in `TradingDayKey`. Keep live-only action rejection and the persistent risk gate. In `buildManagedRisk`, retain backtest's initial-cash `NormalizeAsset` and evaluator construction; call the shared builder instead of duplicating limit mapping. Preserve the existing outer error contexts.
- [x] Step 4: Remove the obsolete `buildLiveRiskConfig` helper and now-unused imports. Run focused `go test -count=1 -timeout 60s ./internal/composition ./internal/cli ./internal/app ./internal/backtest ./internal/agent`; verify with `rg -n 'NewTradingDayPolicy|MaxPositionValue|MaxDailyLoss' internal/cli/agent.go internal/app/backtest.go internal/composition` that the mapping exists only in `internal/composition`.
- [x] Step 5: Update architecture and roadmap docs to distinguish the implemented R10 shared composition from the still-unverified live sandbox round trip, name R11 as the next supporting refactor, and keep the fixed product milestone unchanged. Link this plan from `docs/README.md`.
- [x] Step 6: Run `go test -count=1 -timeout 60s ./...`, `go test -race -count=1 -timeout 90s ./internal/cli/... ./internal/app/... ./internal/agent/...`, `go vet ./...`, `go build ./...`, `gofmt -l` on touched Go files, and `git diff --check`. Do not invoke live API.

## Review Gates

Task 1 gets a scoped spec-and-quality review before Task 2. Task 2 gets its own review. An independent final review checks the whole R10 diff against base `0b4b9eb`, including all eight T-Invest call sites, risk parity, docs and preservation of `configs/example.yaml`. Commit R10 only after an explicit user request.
