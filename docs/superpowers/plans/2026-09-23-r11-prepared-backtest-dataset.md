# R11 Prepared Backtest Dataset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every backtest execution, persisted lifecycle row, and generated report refer to one immutable prepared dataset while bounding terminal persistence after cancellation.

**Architecture:** Add a private per-run preparation boundary in `internal/app`. Preparation resolves the config, dataset, optional manifest, and output paths once; copies the source CSV into a private temporary snapshot while calculating SHA-256; and resolves metadata against those prepared paths. The runner reads only the snapshot, while persistence and report generation reuse the snapshot checksum. Terminal persistence keeps context values but receives a fresh finite timeout even when the execution context is already cancelled.

**Tech Stack:** Go 1.24, existing CSV backtest iterator/runner, `internal/storage.BacktestStore`, SQLite-backed integration tests.

**Spec:** [`docs/roadmap/refactoring.md` R11](../../roadmap/refactoring.md#r11-единая-подготовка-backtest-dataset); [`docs/strategies/overview.md`](../../strategies/overview.md); [`docs/architecture/overview.md`](../../architecture/overview.md).

**Status:** Completed on `main`; task and final whole-change reviews passed. Base commit is `778bbf5`. R11 remains a supporting refactor and does not replace the open live sandbox round-trip milestone.

## Global Constraints

- Work on the existing `main` checkout; do not create a branch and do not commit R11 without a separate explicit user request.
- Preserve the unrelated user-owned `configs/example.yaml` change and never stage it.
- Never set or use `GOCACHE`.
- Do not call T-Invest, read credentials, or mutate a real sandbox account.
- Preserve backtest configuration, strict CSV validation, manifest validation, gap policy, deterministic artifacts, risk composition, simulated broker behavior, and the sequential execution of configured runs.
- Preserve the configured, normally relative `dataset_path` written to the report. Absolute prepared paths are an internal execution detail and must not make artifacts machine-dependent.
- Keep `hashFile` for completed output artifacts; remove only the independent pre-execution hash of the source dataset.
- A private snapshot is authoritative after preparation. Dataset producers are still responsible for publishing a coherent source file, preferably by atomic rename; R11 does not attempt to coordinate with an in-place concurrent writer while the initial copy is occurring.

## File Map

- `internal/app/backtest_prepare.go`: private `preparedBacktestRun`, path resolution, temporary snapshot creation/checksum, and idempotent cleanup.
- `internal/app/backtest.go`: prepare each selected run before persistence; persist, execute, report, and clean up the same snapshot; bound terminal persistence.
- `internal/app/dataset_manifest.go`: accept the already-resolved optional manifest path rather than resolving it independently.
- `internal/app/backtest_test.go`: source-mutation, checksum identity, cancellation/finalization deadline, and cleanup regressions using a narrow fake store.
- `docs/architecture/overview.md`, `docs/strategies/overview.md`, `docs/roadmap/refactoring.md`, `docs/roadmap/current.md`, `docs/README.md`: record the prepared-dataset boundary, R11 status, and unchanged primary milestone.

### Task 1: Prepare and execute one immutable dataset snapshot

**Files:** Create `internal/app/backtest_prepare.go`; modify `internal/app/backtest.go`, `internal/app/dataset_manifest.go`, `internal/app/backtest_test.go`.

**Interfaces:** Introduce only private application types/functions. `preparedBacktestRun` owns the selected run and strategy, the once-resolved absolute source/manifest/output paths, resolved dataset metadata, an open read-only temporary snapshot, its path, and its SHA-256. Its cleanup closes and removes the snapshot and is safe on every return path. Public config and storage contracts do not change.

- [x] Step 1: Add a deterministic regression test with a fake `BacktestStore`. In `StartBacktestRun`, replace the original CSV after preparation but before execution. Assert that the persisted start checksum, report `dataset_sha256`, metrics/trades, and completed status all describe the original prepared bytes; also assert the replacement has a different checksum. Run the focused test and record the expected red result before implementation.
- [x] Step 2: Implement preparation. Resolve `filepath.Abs(filepath.Dir(options.ConfigPath))` once and return its error; derive absolute dataset, optional manifest, and output paths once; load metadata from the prepared manifest path; copy the source CSV to a private temporary file while feeding SHA-256; close the source and writable snapshot; reopen the snapshot read-only at offset zero. On any failure, close/remove partial resources and wrap the operation and run ID clearly.
- [x] Step 3: Remove `mustAbsoluteDir` and the source-dataset `hashFile` call from `RunBacktests`. Prepare before `StartBacktestRun`, persist `prepared.datasetChecksum`, pass the same prepared object to execution, and use only its snapshot as the iterator input. Keep `hashFile` for artifact manifests.
- [x] Step 4: After successful iteration, require the iterator checksum to equal the prepared checksum before publishing success. Use the prepared checksum for the report artifact. Keep manifest expected-checksum validation in the iterator so its existing failure lifecycle remains intact. Keep the configured relative dataset path in JSON.
- [x] Step 5: Ensure per-run cleanup happens before moving to the next run and on preparation, start, execution, artifact, final-persistence, and cancellation failures. Do not put a loop-scoped `defer` in `RunBacktests` that retains all snapshots until the command exits.
- [x] Step 6: Add focused tests for missing source cleanup/error wrapping and for path-resolution failure without silent fallback. A subprocess may use a removed current directory to make `filepath.Abs` fail without changing the parent test process. Run `go test -count=1 -timeout 60s ./internal/app ./internal/backtest` and `gofmt -l` on touched Go files.

### Task 2: Bound terminal persistence and document the boundary

**Files:** Modify `internal/app/backtest.go`, `internal/app/backtest_test.go`, `docs/architecture/overview.md`, `docs/strategies/overview.md`, `docs/roadmap/refactoring.md`, `docs/roadmap/current.md`, `docs/README.md`.

**Interfaces:** Add a private named terminal-persistence timeout and helper. The helper calls `context.WithTimeout(context.WithoutCancel(executionContext), timeout)`, so context values survive, prior cancellation does not prevent the terminal write from starting, and the store cannot block indefinitely. Preserve both execution and finish errors with `errors.Join`.

- [x] Step 1: Add a fake-store test that cancels the execution context after the run has started. Assert that `FinishBacktestRun` receives a live context with a finite deadline, the terminal status/error code are `cancelled`, and the caller still receives `context.Canceled` when final persistence succeeds. Run it and record the expected red result.
- [x] Step 2: Add a focused helper-level timeout test with a short injected duration. The fake finish method blocks until `ctx.Done()` and returns `ctx.Err()`. Assert bounded completion and `context.DeadlineExceeded`. Add a joined-error test proving a cancelled execution plus finish timeout remains identifiable with both `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)`.
- [x] Step 3: Implement the bounded finalization helper and use it for every terminal status, not only cancellation. Keep the normal completed/failed/cancelled classification and atomic store contract unchanged. Document that a timed-out terminal update may leave the durable row in `running`, but never partially commits terminal metadata/artifacts.
- [x] Step 4: Update architecture and strategy documentation with the prepared snapshot lifecycle and checksum ownership. Mark R11 implemented in the refactoring roadmap, keep the first real T-Invest sandbox buy/sell round trip as the primary milestone, and name R12 as the next supporting refactor.
- [x] Step 5: Run `go test -count=1 -timeout 60s ./internal/app ./internal/backtest ./internal/storage/...`, then `go test -count=1 -timeout 60s ./...`, `go test -race -count=1 -timeout 90s ./internal/app/... ./internal/backtest/... ./internal/storage/...`, `go vet ./...`, `go build ./...`, `gofmt -l` on touched Go files, and `git diff --check`. Do not invoke any live API.

## Review Gates

Task 1 receives a scoped spec and quality review before Task 2. Task 2 receives its own review. A fresh independent final review checks the complete R11 diff against `778bbf5`, including snapshot cleanup, dataset/checksum identity, manifest behavior, cancellation semantics, documentation, and preservation of `configs/example.yaml`. R11 remains uncommitted until the user explicitly asks for a commit.
