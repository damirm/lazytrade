# R9 Runtime Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Separate live runtime startup, market loop, signal processing, order submission and execution ingress while preserving observable trading and recovery behavior.

**Architecture:** `Run` remains the lifecycle owner. A private `startup` method returns a typed `startupResult` only after recovery, reconciliation, subscription and `running` persistence have succeeded. A market loop consumes that result. Existing package-private helpers move by responsibility without changing their contracts.

**Tech Stack:** Go 1.24, package `internal/agent`, existing exchange/storage/strategy ports and SQLite tests.

**Spec:** `docs/roadmap/refactoring.md` R9, `docs/architecture/trading-runtime.md` Startup sequence, `docs/specs/implementation-plan.md` FR-ENGINE.

**Status:** Completed on `main`; independent task and whole-change reviews passed. The live sandbox round trip remains open.

## Global Constraints

- Work on `main`; the user explicitly requested it. R8 is commit `44d9517`.
- Preserve user-owned `configs/example.yaml`; do not stage or edit it.
- `OrderIntent` must be durable before `PlaceOrder`; only `ready` may be submitted. `submitting/unknown` use lookup/reconciliation.
- Incoming executions are staged before projections; checkpoint advances only after complete history scan/apply.
- Lifecycle is `reconciling` before recovery and `running` only after post-subscription reconciliation and market subscription.
- Execution stream starts before history scan; market stream starts after ready-intent submission and second reconciliation.
- One strategy's worker/risk failure is isolated; execution/market stream failure blocks the account runtime. Cancellation keeps current behavior.
- No new adapter protocol, retry behavior, storage schema or public interface. Never use `GOCACHE`; no live API.

## File map

- `internal/agent/runtime.go`: public types, `NewRuntime`, small `Run`, component validation, general routing/logger helpers.
- `internal/agent/startup.go`: private `startupResult`, ordered startup/recovery orchestration, unresolved intent lookup.
- `internal/agent/market_loop.go`: market/execution channel select loop, stream fail-closed handling, observer gating and strategy isolation.
- `internal/agent/signal_pipeline.go`: event-to-worker, signal recovery, risk decision and intent creation.
- `internal/agent/order_submission.go`: intent transitions, PlaceOrder, result persistence, order/audit builders.
- `internal/agent/execution_ingress.go`: execution pump, history scan/staging, inbox drain and trading-day attribution.
- `internal/agent/runtime_startup_phase_test.go`: focused order and fail-closed regression.
- `docs/architecture/trading-runtime.md`, `docs/roadmap/refactoring.md`, `docs/roadmap/current.md`: actual boundary and status.

### Task 1: Explicit startup result and phase boundary

**Files:** Modify `internal/agent/runtime.go`; create `internal/agent/startup.go`; create `internal/agent/market_loop.go`; test `internal/agent/runtime_startup_phase_test.go` and existing `runtime_execution_history_test.go`, `runtime_lifecycle_test.go`, `runtime_multi_failclosed_test.go`.

**Interfaces:** Produce private:

```go
type startupResult struct {
    accountID domain.ExchangeAccountID
    activeWorkers map[domain.InstrumentID]*strategy.Worker
    risks map[domain.StrategyID]SignalRisk
    marketStream exchange.MarketStream
    executionNotifications <-chan struct{}
    executionPumpErrors <-chan error
    executionStoreMu *sync.Mutex
    pendingObservers map[domain.StrategyID]struct{}
    signalsRecovered bool
}
func (r Runtime) startup(ctx context.Context, workers map[domain.InstrumentID]*strategy.Worker, risks map[domain.StrategyID]SignalRisk, subscriptions []exchange.Subscription, lifecycleIDs []domain.StrategyID) (startupResult, error)
func (r Runtime) runMarketLoop(ctx context.Context, state startupResult, failedStrategies map[domain.StrategyID]struct{}) error
```

- [x] Step 1: Strengthen the existing startup order test with a trace of lookup, execution subscription, history, first reconciliation, ready-intent submission, second reconciliation, market subscription and `running` lifecycle. The test must assert exact relative order and that failure before `running` never emits `Ready`. Use existing fake/store/spies where possible; add one focused test rather than duplicating every recovery test.
- [x] Step 2: Run `go test -count=1 -timeout 60s ./internal/agent -run 'TestStartup|TestRuntimeLifecycle'` and record the expected red assertion or compilation error for the new phase test.
- [x] Step 3: Extract the existing startup block from `Run` into `startup`, ending after `setLifecycle(..., RuntimeStatusRunning, ...)` and `Ready` notification; return populated `startupResult` only on success. Preserve exact order of side effects, error wrappers and terminal lifecycle defer in `Run`.
- [x] Step 4: Extract the channel select loop from `Run` into `runMarketLoop`, consuming `startupResult`. Keep buffered terminal error preference on concurrent channel closure, cancellation semantics, observer gating, worker isolation and durable execution drain.
- [x] Step 5: Run focused agent tests plus `go test -race -count=1 -timeout 90s ./internal/agent` and inspect the diff for behavior changes. Do not modify production behavior to make a test easier.

### Task 2: Move helpers to owning files and update project knowledge

**Files:** Modify `internal/agent/runtime.go`, `startup.go`, `market_loop.go`, `runtime_startup_phase_test.go`; create `signal_pipeline.go`, `order_submission.go`, `execution_ingress.go`; modify `docs/architecture/trading-runtime.md`, `docs/roadmap/refactoring.md`, `docs/roadmap/current.md`.

**Interfaces:** Keep existing package-private helper names and signatures. No new public contracts. `startupResult` and `runMarketLoop` from Task 1 are the only new boundary.

- [x] Step 0: Add a deterministic lifecycle-failure case to `runtime_startup_phase_test.go`: make the lifecycle spy fail a `running` write after market subscription, assert no `Ready` send and no successful `startupResult`. This closes the Task 1 review's scheduler-sensitive test gap without changing runtime behavior.
- [x] Step 1: Move `startExecutionPump`, `recoverExecutionHistory`, `drainPendingExecutionsSynchronized`, `executionTradingDay`, `drainPendingExecutions` into `execution_ingress.go` verbatim. Keep the same mutex and checkpoint ordering.
- [x] Step 2: Move `processEventWith`, `recoverSignalsWith`, `processSignalWith`, `buildRiskDecision`, `buildIntent`, `deterministicClientOrderID` into `signal_pipeline.go` verbatim.
- [x] Step 3: Move `submitIntent`, `recordSubmitted`, `recordIntentWithoutOrder`, `intentEventTime`, `validateOrderForIntent`, `resolutionAudit`, `intentTransitionAudit`, `requestForIntent`, `orderStatus` into `order_submission.go` verbatim. Move `resolvePendingIntents` to `startup.go`. Place `riskForEvent` in `market_loop.go`; keep general configuration/routing helpers in `runtime.go`.
- [x] Step 4: Format touched Go files. Run `go test -count=1 -timeout 60s ./internal/agent`, then `go test -count=1 -timeout 60s ./...`, `go test -race -count=1 -timeout 90s ./internal/agent/... ./internal/exchange/... ./internal/storage/sqlite/... ./internal/cli/...`, `go vet ./...`, `go build ./...`, and `git diff --check`.
- [x] Step 5: Document the private startup-result boundary and preserved safety sequence in `docs/architecture/trading-runtime.md`; mark R9 status in `docs/roadmap/refactoring.md` and `docs/roadmap/current.md`. State that live sandbox round trip remains unconfirmed and identify the next fixed roadmap item R10.

## Review gates

Task 1 requires independent review of ordering, lifecycle and concurrent streams before Task 2. Task 2 requires review of move completeness, imports, no duplicate helpers, tests and docs. Final review compares the entire R9 diff against R8 base `44d9517`; `configs/example.yaml` is excluded.
