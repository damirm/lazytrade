# R12 Legacy Hard-Cut Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** удалить неиспользуемые и вводящие в заблуждение абстракции, убрать
повторную валидацию конфигурации и перестать рекламировать неработающие CLI
команды, не меняя реализованные торговые, storage и backtest сценарии.

**Architecture:** R12 выполняется как hard cut текущего состояния: production
код содержит только используемые контракты, а будущие `Capabilities`, generic
lease port, `terminal` и `db migrate` возвращаются только вместе с реальным
consumer/implementation. Механические cleanup, изменения внутренних контрактов
и observable CLI help проверяются независимыми задачами и review gates.

**Tech Stack:** Go, Cobra, YAML v3, SQLite, стандартный `testing`, Markdown.

**Spec:** `docs/roadmap/refactoring.md` (R12),
`docs/specs/implementation-plan.md`, `docs/specs/storage-contracts.md`.

**Status:** completed 23 September 2026. Tasks 1–4 passed independent spec and
code-quality reviews; Task 5 completed the full local regression gate. No R13
was created: the next product work remains the documented read-only sandbox
preflight, followed by an explicitly authorized minimal buy/sell round trip
during an open session.

## Global Constraints

- Работать непосредственно в `main`; ветку и worktree не создавать.
- Не устанавливать и не использовать `GOCACHE`.
- Не читать sandbox token, не выполнять сетевые или реальные T-Invest вызовы.
- Не изменять пользовательский `configs/example.yaml`.
- Не добавлять compatibility aliases, fallback paths или dual behavior.
- Коммиты и staging выполняются только после отдельной просьбы пользователя.
- Target requirements могут описывать будущую функциональность, но current-state
  документация не должна утверждать, что отсутствующая реализация уже доступна.
- Существующие SQLite lock/migrations, terminal config validation и
  `config validate --for terminal` должны остаться рабочими.

---

### Task 1: Удалить aliases и test-only production helper

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/app/data_validate.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/backtest/runner.go`
- Modify: `internal/backtest/backtest_test.go`
- Modify: `internal/exchange/tinvest/marketdata.go`
- Modify: `internal/exchange/tinvest/marketdata_mapping_test.go`

**Interfaces:**

- Consumes: canonical `config.StrategyParams`, `exchange.CandleQuery`,
  `backtest.RiskEvaluator`.
- Produces: один тип параметров стратегии, один тип candle query и test-only
  unexported `allowAllRisk`.

- [x] **Step 1: Зафиксировать отсутствие внешних consumers**

Run:

```sh
rg -n '\bMovingAverageCrossParams\b|\bAllowAllRisk\b|\bCandleQuery\b' internal
```

Expected: compatibility alias имеет два consumer, `AllowAllRisk` используется
только в `_test.go`, а `tinvest.CandleQuery` — только внутри T-Invest package.

- [x] **Step 2: Сначала перевести consumers на canonical types**

В `data_validate.go` и `load_test.go` использовать
`config.StrategyParams`. В `marketdata.go` объявить:

```go
func (a *Adapter) Candles(ctx context.Context, query exchange.CandleQuery) ([]domain.Candle, error)
```

В `marketdata_mapping_test.go` импортировать `internal/exchange` и использовать
`exchange.CandleQuery` во всех literals.

- [x] **Step 3: Перенести permissive risk helper в тесты**

Удалить exported `AllowAllRisk` и его `Evaluate` из `runner.go`. В
`backtest_test.go` добавить:

```go
type allowAllRisk struct{}

func (allowAllRisk) Evaluate(context.Context, domain.Signal, PortfolioSnapshot) (RiskDecision, error) {
	return RiskDecision{Allowed: true}, nil
}
```

Заменить три test usages на `allowAllRisk{}`.

- [x] **Step 4: Удалить aliases**

Удалить `MovingAverageCrossParams` из `config.go` и `CandleQuery` alias из
`marketdata.go`.

- [x] **Step 5: Проверить задачу**

Run:

```sh
gofmt -w internal/config/config.go internal/app/data_validate.go internal/config/load_test.go internal/backtest/runner.go internal/backtest/backtest_test.go internal/exchange/tinvest/marketdata.go internal/exchange/tinvest/marketdata_mapping_test.go
go test -count=1 -timeout 60s ./internal/config ./internal/app ./internal/backtest ./internal/exchange/tinvest
rg -n '\bMovingAverageCrossParams\b|\bAllowAllRisk\b|type CandleQuery =' internal
```

Expected: tests pass; final `rg` returns no matches.

---

### Task 2: Убрать повторную config validation и мёртвую decimal ветку

**Files:**

- Modify: `internal/config/load.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/load_test.go`
- Create: `internal/config/validate_test.go`

**Interfaces:**

- Consumes: public `Config.Validate()` and `Config.ValidateFor(Command, lookup)`.
- Produces: private command-only validation helper used after a configuration
  has already passed static validation; public method behavior and error order
  remain unchanged.

- [x] **Step 1: Добавить characterization test порядка ошибок**

Добавить test, где config одновременно статически невалиден и не содержит
command requirements. Вызов `ValidateFor` должен вернуть static error до
command/credential error, а supplied lookup не должен вызываться.

- [x] **Step 2: Добавить decimal boundary table**

Покрыть `isDecimalZero`/его callers значениями `0`, `+0.0`, `-0.0`, `-1`,
`1` и положительным числом больше `uint64`. Ожидания должны различать
`must be positive`, `must not be negative` и успешную валидацию.

- [x] **Step 3: Запустить новые tests до изменения**

Run:

```sh
go test -count=1 -timeout 60s ./internal/config
```

Expected: characterization tests pass на старой реализации; они защищают
семантику рефакторинга, а не искусственно создают failure.

- [x] **Step 4: Выделить command-only validation**

Сохранить public contract:

```go
func (cfg Config) ValidateFor(command Command, lookup func(string) (string, bool)) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return cfg.validateCommandRequirements(command, lookup)
}
```

Перенести текущую command/credential часть без изменения порядка ошибок в
`validateCommandRequirements`. После `LoadFile`, который уже вызывает
`Validate`, `LoadFileFor` должен вызывать private helper напрямую.

- [x] **Step 5: Упростить decimal zero check**

Удалить `strconv.ParseUint` и import `strconv`; после удаления знака и точки
вернуть только:

```go
return strings.Trim(value, "0") == ""
```

- [x] **Step 6: Проверить задачу**

Run:

```sh
gofmt -w internal/config/load.go internal/config/validate.go internal/config/load_test.go internal/config/validate_test.go
go test -count=1 -timeout 60s ./internal/config ./internal/cli ./internal/app
```

Expected: все tests pass; static и command error precedence не изменились.

---

### Task 3: Удалить speculative exchange/storage contracts

**Files:**

- Delete: `internal/exchange/capabilities.go`
- Modify: `internal/exchange/exchange.go`
- Modify: `internal/exchange/fake/fake.go`
- Modify: `internal/exchange/fake/fake_test.go`
- Modify: `internal/exchange/tinvest/client.go`
- Modify: `internal/cli/account_smoke_test.go`
- Modify: все `internal/agent/*_test.go`, вызывающие `fake.New`
- Delete: `internal/storage/lease.go`
- Modify: `docs/architecture/adr/0002-sqlite-storage.md`
- Modify: `docs/architecture/overview.md`
- Modify: `docs/specs/storage-contracts.md`
- Modify: `docs/specs/implementation-plan.md`
- Modify: `docs/specs/tinvest-adapter-design.md`
- Modify: `docs/agents/roles/exchange-market-data.md`

**Interfaces:**

- Consumes: фактически используемый `exchange.Exchange` и concrete
  `sqlite.Store.Acquire/Release` lifecycle.
- Produces: `fake.New(name string) *fake.Exchange`; exchange interface без
  неиспользуемого feature flag snapshot; текущая документация без заявления о
  существующем generic lease port.

- [x] **Step 1: Добавить compile-time regression boundary**

Существующие agent, CLI и adapter tests являются contract tests: после удаления
methods каждый production adapter и wrapper должен продолжить компилироваться.
Отдельную compatibility shim не добавлять.

- [x] **Step 2: Удалить `Capabilities` одним hard cut**

Удалить type file, interface method, adapter/fake fields и methods. Изменить
constructor:

```go
func New(name string) *Exchange
```

Механически заменить все test calls `fake.New("fake", exchange.Capabilities{...})`
на `fake.New("fake")`; удалить ставшие неиспользуемыми imports и round-trip
capabilities assertion.

- [x] **Step 3: Удалить неиспользуемый `AgentLease` interface**

Удалить только `internal/storage/lease.go`. Не менять `sqlite.Store.Acquire`,
`Release`, automatic close cleanup, CLI lock lifecycle и их tests.

- [x] **Step 4: Синхронизировать документацию**

В accepted ADR и current architecture явно записать: текущий single-driver
runtime использует concrete SQLite lifecycle; consumer-owned lease interface
появится вместе со вторым driver/реальным generic consumer. В target specs
сохранить будущую потребность в capability negotiation и PostgreSQL locking,
но убрать формулировки, будто текущие неиспользуемые Go types являются
реализованным обязательным handoff. Не проектировать будущую форму API в R12.

- [x] **Step 5: Проверить задачу**

Run:

```sh
gofmt -w internal/exchange/exchange.go internal/exchange/fake/fake.go internal/exchange/fake/fake_test.go internal/exchange/tinvest/client.go internal/cli/account_smoke_test.go internal/agent/*.go
go test -count=1 -timeout 60s ./internal/exchange/... ./internal/agent ./internal/cli ./internal/storage/...
go test -race -count=1 -timeout 90s ./internal/exchange/... ./internal/agent ./internal/cli ./internal/storage/sqlite
rg -n 'Capabilities\(|exchange\.Capabilities|type AgentLease' internal
```

Expected: tests and race tests pass; final `rg` returns no matches.

---

### Task 4: Удалить CLI stubs и зафиксировать observable help

**Files:**

- Delete: `internal/cli/terminal.go`
- Delete: `internal/cli/db.go`
- Delete: `internal/cli/not_implemented.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`
- Create: `internal/cli/testdata/root_help.golden`
- Modify: `docs/roadmap/current.md`
- Modify: `docs/roadmap/refactoring.md`
- Modify: `docs/architecture/overview.md`

**Interfaces:**

- Consumes: Cobra root registration and existing `config validate --for
  terminal` target validation.
- Produces: root help, содержащий только исполнимые commands; unknown-command
  error для `terminal` и `db migrate`.

- [x] **Step 1: Добавить failing CLI contract tests**

Добавить golden test полного root help и table test, который ожидает ошибку
`unknown command` для `terminal` и `db migrate`. Обновить required-command test,
чтобы он назывался `TestRootContainsImplementedCommands` и перечислял только
реально работающие paths.

- [x] **Step 2: Запустить tests и подтвердить failure**

Run:

```sh
go test -count=1 -timeout 60s ./internal/cli
```

Expected: new absence/help expectations fail, потому что stubs ещё
зарегистрированы.

- [x] **Step 3: Удалить registrations и stub files**

Удалить `newTerminalCommand()` и `newDBCommand()` из root registration, удалить
три ставших ненужными файла и заменить root `Short` на:

```text
Automated trading, market data, and strategy backtesting
```

Не удалять `TerminalConfig`, `CommandTerminal`, terminal static validation или
`config validate --for terminal`.

- [x] **Step 4: Обновить current-state docs и R12 status**

Записать, что `terminal` и `db migrate` сейчас не экспонируются и вернутся
атомарно вместе с реализацией. Target CLI requirements не отменять. В R12
перечислить удалённые элементы и явно отметить, что текущие SQLite migrations
по-прежнему применяются при `sqlite.Open`.

- [x] **Step 5: Проверить задачу**

Run:

```sh
gofmt -w internal/cli/root.go internal/cli/root_test.go
go test -count=1 -timeout 60s ./internal/cli ./internal/config ./internal/storage/sqlite
go run ./cmd/lazytrade --help
```

Expected: tests pass; root help не содержит `terminal` или `db`; terminal
configuration validation всё ещё компилируется и покрывается tests.

---

### Task 5: Полный regression gate и knowledge-base handoff

**Files:**

- Modify: `docs/roadmap/refactoring.md`
- Modify: `docs/roadmap/current.md`
- Modify: `docs/superpowers/plans/2026-09-23-r12-legacy-hard-cut.md`

**Interfaces:**

- Consumes: результаты Tasks 1–4.
- Produces: завершённый R12 без нового product milestone; следующий шаг снова
  read-only sandbox preflight и реальный buy/sell round trip в открытую сессию.

- [x] **Step 1: Проверить отсутствие legacy symbols и stub copy**

Run:

```sh
rg -n 'MovingAverageCrossParams|AllowAllRisk|type CandleQuery =|Capabilities\(|exchange\.Capabilities|type AgentLease|command runtime is not implemented' --glob '*.go' .
```

Expected: no matches.

- [x] **Step 2: Выполнить полный gate**

Run:

```sh
make fmt
make test
go vet ./...
go build ./...
go test -race -count=1 -timeout 90s ./internal/agent ./internal/app ./internal/backtest ./internal/cli ./internal/config ./internal/exchange/... ./internal/storage/sqlite
```

Expected: all commands succeed.

- [x] **Step 3: Проверить scope diff**

Run:

```sh
git status --short
git diff --check
git diff --stat
git diff -- configs/example.yaml
```

Expected: `git diff --check` succeeds; pre-existing user diff in
`configs/example.yaml` не изменён R12; нет generated/cache artifacts.

- [x] **Step 4: Независимые reviews**

Отдельные reviewers проверяют соответствие этому плану и затем code quality.
Каждое замечание исправляется и повторно проверяется до финального отчёта.

- [x] **Step 5: Обновить статус плана**

Отметить R12 завершённым в roadmap и записать фактические verification commands.
Не создавать R13: после supporting refactor проект возвращается к зафиксированному
sandbox milestone.
