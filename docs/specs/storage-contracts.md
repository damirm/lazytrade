# Storage contracts: проект реализации

## 1. Назначение и статус

Документ уточняет этапы 2, 3 и storage-часть этапа 8A из
`implementation-plan.md`. Он является design handoff роли Foundation &
Storage. Код и окончательные доменные типы этим документом не утверждаются.

Цели:

- задать минимальные repository interfaces по application use case;
- определить атомарные границы торгового pipeline;
- обеспечить идемпотентность signal, order и execution;
- описать физическую SQLite-схему, не блокирующую будущий PostgreSQL adapter;
- определить единый contract test suite;
- сохранить metadata и artifacts backtest без помещения исторических свечей в
  основную БД.

Не цели:

- универсальный SQL для SQLite и PostgreSQL;
- один `Storage`-интерфейс со всеми операциями;
- распределённая координация нескольких агентов;
- хранение exchange SDK/sqlc типов за пределами adapter;
- перенос бизнес-логики P&L, reconciliation или state machine в SQL.

Документ покрывает FR-STORAGE-001..009, FR-ENGINE-002..005,
FR-RECON-001..003, FR-BACKTEST-010, FR-BACKTEST-013, FR-BACKTEST-016,
NFR-002, NFR-006, NFR-008 и NFR-009.

## 2. Основные принципы

1. Application service принимает только тот узкий repository/transaction port,
   который нужен его use case.
2. Не вводить публичный god-interface вида `Storage.Repositories()`.
3. Transaction boundary выражается отдельным интерфейсом конкретного use case,
   а не передачей `*sql.Tx`, `sqlc.Queries` или generic repository locator.
4. ID создаются приложением. База проверяет уникальность, но не определяет
   бизнес-ID.
5. sqlc-типы и nullable-типы драйвера остаются внутри
   `internal/storage/sqlite`.
6. Доменные enums, decimal и timestamps преобразуются явными mapper-функциями.
7. Executions, P&L events и audit events append-only. Исправление создаёт новое
   correction event.
8. Любой список из repository имеет документированный стабильный порядок.
9. `not found`, conflict/duplicate и optimistic concurrency являются
   типизированными storage errors; приложение не разбирает текст SQLite.
10. PostgreSQL portability проверяется одинаковым поведением repository
    contract tests, а не одинаковым DDL/SQL.

## 3. Размещение портов

Предпочтительная схема:

```text
internal/storage/
    errors.go
    tx.go
    strategy.go
    execution.go
    reconciliation.go
    control.go
    query.go
    backtest.go
    contracttest/
        suite.go

internal/storage/sqlite/
    store.go
    lock.go
    migrate.go
    mapper.go
    tx.go
    generated/

db/sqlite/
    migrations/
    queries/

db/postgres/
    README.md

sqlc.sqlite.yaml
```

Интерфейсы допустимо размещать ближе к consumer package, если это не создаёт
циклическую зависимость. В любом случае SQLite adapter реализует их
структурно; application package не импортирует `internal/storage/sqlite`.

## 4. Общие value objects и ошибки

Ниже имена типов ориентировочные:

```go
type Page struct {
    Limit  uint32
    Cursor string
}

type PageResult[T any] struct {
    Items      []T
    NextCursor string
}

var (
    ErrNotFound       = errors.New("storage: not found")
    ErrConflict       = errors.New("storage: conflict")
    ErrAlreadyExists  = errors.New("storage: already exists")
    ErrVersionConflict = errors.New("storage: version conflict")
    ErrLockHeld       = errors.New("storage: agent lock held")
)
```

Правила:

- `errors.Is` обязан работать;
- сообщения содержат entity type и безопасный ID, но не DSN и payload с
  секретами;
- insert с тем же idempotency key и тем же semantic payload может возвращать
  существующую сущность;
- тот же ключ с другим payload возвращает `ErrConflict`;
- pagination cursor является opaque для caller;
- отсутствие результата не кодируется пустой структурой;
- repository не получает `time.Now()`: timestamps передаёт application service
  через общий `Clock`.

## 5. Strategy runtime persistence

### 5.1. Регистрация instance

```go
type StrategyCatalog interface {
    UpsertDefinition(
        ctx context.Context,
        definition domain.StrategyDefinition,
    ) error

    GetDefinition(
        ctx context.Context,
        id domain.StrategyID,
    ) (domain.StrategyDefinition, error)

    ListDefinitions(ctx context.Context) ([]domain.StrategyDefinition, error)
}
```

`UpsertDefinition` разрешает обновить mutable metadata/config hash, но не
подменить identity `(exchange account, instrument, strategy type)` без явной
application migration. Список сортируется по `StrategyID`.

### 5.2. State и signals одного market event

Strategy runtime требует атомарно сохранить новое versioned state и
детерминированные signals:

```go
type StrategyEventCommit struct {
    StrategyID      domain.StrategyID
    ExpectedVersion uint64
    NewState        strategy.StateEnvelope
    RuntimeStatus   domain.StrategyStatus
    StatusReason    string
    EventCursor     domain.EventCursor
    Signals         []domain.Signal
    UpdatedAt       time.Time
}

type StrategyEventStore interface {
    LoadRuntime(
        ctx context.Context,
        strategyID domain.StrategyID,
    ) (domain.StrategyRuntime, error)

    CommitEvent(
        ctx context.Context,
        commit StrategyEventCommit,
    ) error
}
```

Инварианты `CommitEvent`:

- state и все signals сохраняются в одной транзакции;
- `ExpectedVersion` реализует optimistic concurrency;
- успешная запись увеличивает storage revision ровно на один;
- повторный exact commit после неопределённого локального outcome идемпотентен
  по `(strategy_id, event cursor/state checksum)`;
- существующий `signal_id` с другим payload даёт conflict;
- event cursor не может регрессировать;
- signals упорядочены по `ordinal`;
- неизвестный future state version не переписывается.

Если signals позже решено не хранить как отдельные сущности, сохранить как
минимум `signal_id`, canonical payload/hash и causative cursor: это необходимо
для FR-ENGINE-003 и аудита причин появления intent.

## 6. Risk decision и создание OrderIntent

Risk evaluation читает snapshot через application query services. Запись
решения должна сохранить сам факт решения. Для разрешённого решения intent и
audit создаются атомарно:

```go
type RecordRiskDecisionParams struct {
    Decision domain.RiskDecision
    Intent   *domain.OrderIntent // обязательно для allow
    Audit    domain.AuditEvent
}

type RiskDecisionWriter interface {
    RecordRiskDecision(
        ctx context.Context,
        params RecordRiskDecisionParams,
    ) error
}
```

Рекомендуемая транзакционная реализация:

```go
type RiskDecisionTx interface {
    InsertDecision(ctx context.Context, decision domain.RiskDecision) error
    InsertIntent(ctx context.Context, intent domain.OrderIntent) error
    AppendAudit(ctx context.Context, event domain.AuditEvent) error
}

type RiskDecisionTransactor interface {
    WithRiskDecisionTx(
        ctx context.Context,
        fn func(RiskDecisionTx) error,
    ) error
}
```

Интегратор должен выбрать один из двух вариантов. Не предоставлять одновременно
высокоуровневый `RecordRiskDecision` и unrestricted transaction callback без
потребности.

Граница:

```text
BEGIN
  insert risk_decision (allow/reject/pause)
  if allow: insert order_intent
  append audit_event
COMMIT
→ только после COMMIT разрешён Broker/Exchange.Submit
```

Уникальности:

- `risk_decisions.signal_id` — один итоговый decision на signal;
- `order_intents.signal_id` — не более одного intent на signal;
- `order_intents.client_order_id` — глобально уникален внутри БД;
- повтор того же signal/decision/intent возвращает существующий semantic
  результат;
- mismatch payload возвращает conflict.

Для `Pause` в этой же транзакции допускается обновить strategy status на
`risk_paused`, если state transition уже проверен application service.
Фактическая отмена внешних orders происходит после commit.

## 7. Order submission и unknown outcome

```go
type OrderLifecycleStore interface {
    GetIntentByClientOrderID(
        ctx context.Context,
        id domain.ClientOrderID,
    ) (domain.OrderIntent, error)

    ListUnresolvedIntents(
        ctx context.Context,
        accountID domain.ExchangeAccountID,
    ) ([]domain.OrderIntent, error)

    MarkSubmissionStarted(
        ctx context.Context,
        intentID domain.OrderIntentID,
        at time.Time,
    ) error

    RecordSubmissionResult(
        ctx context.Context,
        result domain.OrderSubmissionResult,
        audit domain.AuditEvent,
    ) error

    GetOrderByClientOrderID(
        ctx context.Context,
        id domain.ClientOrderID,
    ) (domain.Order, error)
}
```

`RecordSubmissionResult` выполняется одной локальной транзакцией:

- создаёт/обновляет order;
- переводит intent в `submitted`, `rejected` либо `unknown_outcome`;
- добавляет audit event.

`unknown_outcome` не удаляется и не превращается в новый intent. Повторный
network submit запрещён до resolution/reconciliation. Exchange order ID может
быть nullable до authoritative resolution.

Статусные переходы проверяются application/domain state machine; БД защищает от
потери revision и нарушения foreign keys.

## 8. Атомарное применение fill

Fill является наиболее важной write-транзакцией:

```go
type ApplyFillParams struct {
    Execution        domain.Execution
    ExpectedPosition *domain.PositionVersion
    PositionAfter    domain.Position
    PnLEvents        []domain.PnLEvent
    DailyStatistics  domain.DailyStatistics
    OrderAfter       domain.Order
    Audit            domain.AuditEvent
}

type FillStore interface {
    ApplyFill(ctx context.Context, params ApplyFillParams) (applied bool, err error)
}
```

Граница:

```text
BEGIN
  insert execution with dedupe key
  if duplicate exact execution: return applied=false
  update order filled quantity/status
  update position using expected revision
  append P&L events
  upsert daily statistics derived snapshot
  append audit event
COMMIT
```

Инварианты:

- duplicate execution не меняет order, position, P&L или counters повторно;
- duplicate key с другим payload даёт conflict и запускает reconciliation;
- position revision предотвращает lost update;
- сумма applied execution quantities не может превышать requested quantity без
  explicit correction/reconciliation event;
- P&L events имеют deterministic IDs, производные от execution + component;
- `daily_statistics` является производной проекцией; authoritative история —
  executions, equity snapshots и P&L events;
- ошибка любого шага откатывает всю транзакцию;
- `applied=false, nil` допустим только для exact duplicate.

После commit application service вызывает `RiskManager.ObservePnL`. Если
получен pause, отдельная атомарная risk/control транзакция меняет status и
создаёт audit, затем выполняются внешние cancel side effects.

Нельзя удерживать DB transaction во время сетевого вызова биржи.

## 9. Reconciliation ports

Reconciliation нужны read-модели и несколько узких mutation-команд:

```go
type ReconciliationReader interface {
    ListOpenOrders(
        ctx context.Context,
        accountID domain.ExchangeAccountID,
    ) ([]domain.Order, error)

    ListUnresolvedIntents(
        ctx context.Context,
        accountID domain.ExchangeAccountID,
    ) ([]domain.OrderIntent, error)

    ListPositions(
        ctx context.Context,
        accountID domain.ExchangeAccountID,
    ) ([]domain.Position, error)
}

type ReconciliationWriter interface {
    ResolveIntent(
        ctx context.Context,
        resolution domain.IntentResolution,
        audit domain.AuditEvent,
    ) error

    RecordRecoveredOrder(
        ctx context.Context,
        order domain.Order,
        audit domain.AuditEvent,
    ) error

    ApplyRecoveredFill(
        ctx context.Context,
        params ApplyFillParams,
    ) (bool, error)

    SetStrategyStatus(
        ctx context.Context,
        transition domain.StrategyStatusTransition,
        audit domain.AuditEvent,
    ) error
}
```

Результаты lists сортируются:

- orders: `(submitted_at, id)`;
- intents: `(created_at, id)`;
- positions: `(strategy_id, instrument_id)`.

Каждая mutation идемпотентна. Один огромный transaction на весь reconcile не
нужен: authoritative exchange reads выполняются вне DB transaction, после чего
малые локальные reconciliation steps применяются по одному. Торговля остаётся
закрытой control gate до полного успешного reconcile.

## 10. Control, audit и query ports

### 10.1. Control

```go
type ControlStore interface {
    GetEffectiveInputs(ctx context.Context) ([]domain.ControlState, error)

    ApplyTransition(
        ctx context.Context,
        transition domain.ControlTransition,
        audit domain.AuditEvent,
    ) error
}
```

`ApplyTransition` атомарно обновляет один scope и добавляет audit event.
Уникальный ключ control state: `(scope_type, scope_id)`. Global emergency stop
имеет зарезервированный scope ID. Effective state вычисляет application
service, не SQL.

### 10.2. Audit

```go
type AuditReader interface {
    ListAuditEvents(
        ctx context.Context,
        filter domain.AuditFilter,
        page Page,
    ) (PageResult[domain.AuditEvent], error)
}
```

Обычным application services не выдаётся самостоятельный `AuditWriter`, чтобы
не потерять атомарность с изменением состояния. Append audit входит в
соответствующий mutation use case.

### 10.3. Web/status read models

```go
type AgentQueryStore interface {
    GetStatusSnapshot(ctx context.Context) (domain.AgentStatusSnapshot, error)
    ListStrategyViews(ctx context.Context) ([]domain.StrategyView, error)
    ListOrderViews(ctx context.Context, filter domain.OrderFilter, page Page) (...)
    ListExecutionViews(ctx context.Context, filter domain.ExecutionFilter, page Page) (...)
    ListStatisticsViews(ctx context.Context, filter domain.StatisticsFilter) (...)
}
```

Конкретные DTO лучше разместить в application query package, не в `domain`, если
они являются web-oriented projection. Web handler не импортирует sqlc. Для
первого MVP допустимы несколько SQL joins внутри SQLite query adapter.

## 11. Equity, P&L и daily statistics

```go
type StatisticsStore interface {
    GetRiskSnapshot(
        ctx context.Context,
        strategyID domain.StrategyID,
        day domain.TradingDay,
        asset domain.Asset,
    ) (domain.StrategyRiskSnapshot, error)

    RecordEquitySnapshot(
        ctx context.Context,
        snapshot domain.EquitySnapshot,
    ) error

    GetDailyStatistics(
        ctx context.Context,
        strategyID domain.StrategyID,
        day domain.TradingDay,
        asset domain.Asset,
    ) (domain.DailyStatistics, error)
}
```

Start-of-day snapshot имеет unique key
`(strategy_id, trading_day, asset, snapshot_type)` и создаётся идемпотентно.
Дополнительные intraday snapshots имеют application-generated ID.

`trading_day` хранится как каноническая календарная дата `YYYY-MM-DD`,
рассчитанная application service по timezone/reset policy. SQLite не вычисляет
границы дня.

Ни один запрос не агрегирует разные asset. Asset всегда часть ключа/фильтра.
Completeness status хранится явно.

## 12. Backtest run и artifacts

Backtest metadata сохраняется через отдельный port; он не нужен agent runtime:

```go
type BacktestRunStore interface {
    StartRun(ctx context.Context, run domain.BacktestRun) error

    CompleteRun(
        ctx context.Context,
        completion domain.BacktestCompletion,
        artifacts []domain.BacktestArtifact,
    ) error

    FailRun(
        ctx context.Context,
        failure domain.BacktestFailure,
        artifacts []domain.BacktestArtifact,
    ) error

    CancelRun(
        ctx context.Context,
        cancellation domain.BacktestCancellation,
        artifacts []domain.BacktestArtifact,
    ) error

    GetRun(ctx context.Context, id domain.BacktestRunID) (domain.BacktestRun, error)
}
```

State machine:

```text
pending → running
running → completed
running → failed
running → cancelled
```

Для CLI можно сразу создавать `running`; незавершённый `running` после crash
при следующем startup/inspection помечается `interrupted` отдельной recovery
командой или application startup policy. Не маскировать его как failed.

`CompleteRun`/`FailRun`/`CancelRun` атомарно:

- проверяют ожидаемый current status/revision;
- обновляют terminal status и timestamps;
- сохраняют summary metrics, warnings/error;
- регистрируют все уже закрытые artifact-файлы.

Artifact protocol:

1. runner пишет файл во временный путь в целевом output directory;
2. flush/close;
3. вычисляет SHA-256 и размер;
4. атомарно переименовывает в final path, где это поддерживается;
5. передаёт metadata repository;
6. repository не пишет и не удаляет artifact-файл.

Если DB commit не удался после rename, файл остаётся orphan и может быть найден
cleanup/repair командой. Если файл не закрыт, его нельзя регистрировать.

`backtest_runs` хранит:

- run ID и configured run ID;
- strategy ID/type и normalized strategy config payload/hash;
- risk/execution config payload/hash;
- application version/commit;
- content dataset checksum и metadata checksum отдельно;
- dataset logical ID/path, размер и период;
- seed;
- report/fill/config schema versions;
- status/revision;
- started/finished wall-clock metadata;
- deterministic metrics/warnings/assumptions payload;
- safe error code/message.

`backtest_artifacts` хранит:

- artifact ID и run ID;
- type (`report_json`, `trades_csv`, `equity_series`, другое versioned value);
- relative или explicitly normalized path;
- content checksum;
- size;
- schema/media type;
- created timestamp.

Большие orders/executions/equity series потоково записываются в artifacts.
Основная БД хранит summary и manifest. Исторические OHLCV не дублируются.

Воспроизводимость не проверяется через unique constraint: одинаковые inputs
могут запускаться многократно и иметь разные run IDs. Для поиска можно
индексировать `(application_version, config_hash, dataset_checksum, seed)`.

## 13. Логические сущности и ключи

Минимальный набор:

| Сущность | Primary key | Обязательные unique keys |
|---|---|---|
| `strategy_instances` | application `strategy_id` | при MVP `(exchange_account_id, instrument_id)` для торгующих instances |
| `strategy_states` | `strategy_id` | `(strategy_id, revision)` через history либо current row revision |
| `strategy_lifecycle` | `strategy_id` | одна текущая lifecycle-строка на instance |
| `signals` | `signal_id` | `(strategy_id, causative cursor, ordinal)` |
| `risk_decisions` | application ID или `signal_id` | `signal_id` |
| `order_intents` | application `intent_id` | `signal_id`, `client_order_id` |
| `orders` | application `order_id` | `intent_id`; partial unique `(exchange_account_id, exchange_order_id)` when non-null |
| `order_executions` | application `execution_id` | exchange dedupe key when trustworthy |
| `positions` | application/current composite key | `(strategy_id, instrument_id)` |
| `equity_snapshots` | application ID | start-of-day composite key |
| `pnl_events` | application event ID | `(source_execution_id, component_type)` where applicable |
| `control_states` | composite | `(scope_type, scope_id)` |
| `daily_statistics` | composite | `(strategy_id, trading_day, asset)` |
| `audit_events` | application event ID | optional domain idempotency key |
| `backtest_runs` | application run ID | none on input identity |
| `backtest_artifacts` | application artifact ID | `(backtest_run_id, artifact_type, path)` |

### Execution dedupe

T-Invest `Operation.id` нельзя считать стабильным. Схема должна поддерживать:

- `source` (`order_state_stream`, `trades_stream`, `order_state_query`,
  `operations_cursor`, `simulated`);
- nullable raw exchange execution/trade ID;
- canonical `dedupe_key`, вычисленный adapter/application normalization;
- canonical payload checksum;
- unique `(exchange_account_id, source_family, dedupe_key)`.

`source_family` объединяет источники, для которых sandbox-тестом доказана общая
identity. До такой проверки нельзя молча дедуплицировать разные identifier
namespaces. Неизвестная корреляция приводит к reconciliation/blocked, а не к
эвристическому merge.

## 14. SQLite physical representation

Рекомендуемые переносимые правила:

- application IDs, enums, assets и decimal — `TEXT NOT NULL`;
- decimal хранится в canonical base-10 string, без exponent;
- timestamps — UTC Unix microseconds в `INTEGER`, с mapper в `time.Time`;
- trading day — ISO date `TEXT`;
- booleans — `INTEGER NOT NULL CHECK (value IN (0,1))`;
- version/revision/counters/size — `INTEGER`;
- JSON payload — canonical UTF-8 JSON в `TEXT`, перед записью валидируется;
- checksums — lowercase hex `TEXT`;
- nullable поля остаются SQL NULL, не sentinel empty string/zero;
- foreign keys указываются явно;
- enum значения защищаются `CHECK`, если миграционная цена расширения приемлема.

Почему decimal как `TEXT`: SQLite numeric affinity может преобразовать число в
binary floating representation. Все arithmetic/invariants выполняются в Go.
Будущий PostgreSQL adapter может использовать `NUMERIC`, сохраняя тот же
domain contract.

Почему Unix microseconds: однозначный UTC instant и стабильная сортировка.
Наносекундная точность не требуется текущими контрактами; если exchange
реально требует nanos для dedupe/order, решение нужно пересмотреть до миграции.

Индексы:

- все foreign keys;
- unresolved intents по `(exchange_account_id, status, created_at)`;
- open orders по `(exchange_account_id, status, submitted_at)`;
- executions по `(order_id, executed_at)`;
- P&L/statistics по `(strategy_id, trading_day, asset)`;
- audit по `(created_at, id)` и `(scope_type, scope_id, created_at)`;
- backtest input identity;
- никаких индексов «на всякий случай» без query/plan.

SQLite initialization на каждом connection:

```sql
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

`journal_mode=WAL` устанавливается/проверяется при open/migration. Write
transactions короткие; pool parameters задаются явно. Точный SQLite driver,
CGO policy и max open connections фиксируются ADR.

## 15. Single-agent lock

Lock защищает только от случайного запуска второго `lazytrade agent` на той же
БД. Он не применяется к offline `config validate`, read-only tooling и по
умолчанию к backtest, если backtest пишет metadata короткими транзакциями и не
использует live runtime state.

Требуемый port:

```go
type AgentLease interface {
    Acquire(ctx context.Context, owner domain.AgentInstanceID) error
    Release(ctx context.Context) error
}
```

Семантика:

- acquire fail-fast, без бесконечного ожидания;
- один object/store не может acquire дважды без idempotent exact-owner rule;
- lock удерживается всё время agent lifecycle;
- `Release` идемпотентен;
- потеря lock переводит agent в safe shutdown/blocked;
- lock нельзя реализовывать обычной singleton row, если DB transaction/connection
  не удерживает реальную эксклюзивность весь lifecycle;
- PID-файл без OS lock недостаточен из-за stale PID и PID reuse;
- crash обязан освобождать OS/session lock автоматически.

Рекомендация SQLite: OS advisory lock отдельного lock-файла, путь которого
канонически производен от DB path, с file descriptor на весь lifecycle.
Проверить macOS/Linux и поведение symlink/relative paths. Для in-memory DB в
tests использовать injectable lock implementation.

Будущий PostgreSQL: session-level advisory lock на выделенном connection.
Connection нельзя возвращать в pool до release. Это предохранитель, не leader
election.

## 16. Migration и sqlc layout

```text
db/sqlite/migrations/
    000001_initial.up.sql
    000002_....up.sql
db/sqlite/queries/
    strategy.sql
    execution.sql
    reconciliation.sql
    control.sql
    query.sql
    backtest.sql
sqlc.sqlite.yaml
internal/storage/sqlite/generated/
```

Правила:

- migrations являются единственным source of truth и одновременно schema input
  для `sqlc`; отдельный snapshot не поддерживается;
- sqlc queries группируются по use case, не только по таблицам;
- generated code не редактируется;
- migration runner хранит monotonically increasing version и dirty/error state;
- запуск на неизвестной более новой version завершается ошибкой;
- destructive automatic down migration не выполняется;
- migration выполняется до acquire runtime state и network connections;
- отдельная CLI `db migrate` не требует exchange credentials;
- migration integration test создаёт реальную временную SQLite DB.

Для будущего PostgreSQL:

```text
db/postgres/migrations/
db/postgres/queries/
db/postgres/schema.sql
sqlc.postgres.yaml
internal/storage/postgres/generated/
```

До этапа 13 создаётся только README с portability constraints; нельзя
генерировать пустой adapter или общий lowest-common-denominator SQL package.

## 17. Contract test suite

Suite принимает factory:

```go
type TestBackend interface {
    Open(t *testing.T) TestStore
    Reopen(t *testing.T, previous TestStore) TestStore
}
```

Тесты используют реальную временную SQLite DB, fixed Clock и deterministic ID
generator. SQL mocks запрещены для adapter contract.

### Матрица

| Группа | Обязательные сценарии |
|---|---|
| Migration | empty DB; sequential upgrade; repeat current version; reject newer/dirty; FK enabled |
| Mapping | decimal round-trip; negative Money where valid; UTC before/after epoch; NULL vs zero; canonical JSON; unknown enum error |
| Strategy | absent state; commit state+signals; rollback; version conflict; cursor regression; duplicate exact signal; duplicate conflicting payload; reopen/restore |
| Risk/intent | allow creates decision+intent+audit atomically; reject has no intent; pause status+audit; duplicate signal idempotent; conflicting client ID rejected |
| Submission | intent visible before submit; accepted/rejected/unknown outcome; exchange ID uniqueness scoped by account; reopen unresolved intent |
| Fill | partial fills; exact duplicate returns not applied; conflicting duplicate; order/position/P&L/statistics atomic rollback; revision conflict; no overfill |
| P&L | per strategy/day/asset isolation; no cross-asset aggregation; start-of-day uniqueness; completeness preserved; restart retains loss |
| Reconcile | deterministic lists; resolve unknown intent; recover order/fill idempotently; critical status transition+audit atomic |
| Control | scope uniqueness; valid transition+audit; invalid transition no write; resume cannot erase stricter scope in persistence inputs |
| Audit | append-only; deterministic pagination; correction event instead of update; payload round-trip |
| Backtest | start→complete/fail/cancel; invalid transition; metadata/hash round-trip; artifacts atomic with terminal status; repeated identical inputs allowed; interrupted running visible |
| Transaction | callback error rollback; context cancellation rollback; panic rollback/repanic policy documented; no network inside callback (code review) |
| Lock | first acquire; second process/handle fails fast; release then acquire; crash/process test where practical; path alias/symlink behavior |
| Portability | same semantic suite runnable for SQLite and future PostgreSQL; tests do not assert SQLite error text or rowid |

Дополнительные crash-point tests принадлежат engine integration suite, но
storage fixture должен позволить reopen после:

- intent commit до API call;
- unknown outcome commit;
- order response до local order commit;
- execution insert transaction rollback;
- completed artifact rename до metadata commit.

## 18. Необходимые проверки в implementation review

- application services не получают concrete SQLite store «для удобства»;
- transaction callbacks не совершают exchange/network calls;
- repository не пересчитывает бизнес-P&L;
- list queries имеют `ORDER BY`;
- pagination bounded; `Limit=0` получает безопасный default, превышение max
  отклоняется/ограничивается документированно;
- raw DSN не входит в errors/logs;
- busy/locked SQLite error получает безопасную storage classification;
- context cancellation передаётся во все queries;
- row scans не преобразуют decimal через `float64`;
- artifact path не позволяет path traversal при последующей выдаче через web;
- SQLite backup/copy не выполняется при активном writer без корректного API.

## 19. Спорные решения для интегратора

1. **Decimal library и canonical encoding.** До DDL/mapper tests выбрать
   библиотеку, precision и правило запрета exponent.
2. **ID format.** T-Invest client order ID требует UID ≤ 36; рекомендуется
   UUIDv7 для intent/client order ID. Signal ID может быть hash, но длина и
   representation должны быть закреплены.
3. **Strategy state history.** Хранить только current row с revision либо
   append-only history. MVP достаточно current row + audit, но history улучшает
   crash diagnostics.
4. **Хранение signals/risk decisions.** Основной план не перечисляет таблицы
   `signals`/`risk_decisions`, однако детерминированная дедупликация и аудит
   требуют либо этих таблиц, либо эквивалентного payload в intent/audit.
   Рекомендация: отдельные компактные таблицы.
5. **Risk transaction API.** Высокоуровневый atomic method безопаснее generic
   callback; callback гибче при развитии pipeline. Рекомендация: use-case
   method до появления второго caller.
6. **Fill transaction ownership.** `FillStore.ApplyFill` принимает уже
   вычисленные Position/P&L либо transaction загружает before-state и вызывает
   pure domain reducer. Рекомендация: application service вычисляет pure result
   из snapshot, store применяет его с expected revisions.
7. **Execution dedupe key T-Invest.** Нельзя утвердить до sandbox integration
   tests о стабильности identifiers между streams/query/operations.
8. **Timestamp precision.** Unix microseconds рекомендуются, но надо подтвердить
   достаточность для event cursor и fill dedupe.
9. **SQLite driver/CGO и lock implementation.** Требуются ADR и
   cross-platform test до этапа 3.
10. **Backtest lock policy.** Нужен ли exclusive agent lock для backtest,
    пишущего summary в ту же SQLite DB. Рекомендация: не нужен при корректном
    WAL/transactions, но `db migrate` и schema changes не должны идти
    одновременно с agent.
11. **Artifact paths.** Relative to configured artifact root предпочтительнее
    absolute paths для переносимости; root identity/relocation policy нужно
    закрепить.
12. **Position ownership constraint.** Unique
    `(exchange_account_id, instrument_id)` для trading strategy следует
    обеспечить после отделения read-only definitions; иначе конфликт ловится
    только config validation.
13. **Migration library.** Выбрать library/embed policy; sqlc сам migrations не
    применяет.
14. **Backtest terminal states.** Добавить `interrupted` как persisted status
    либо оставлять stale `running` до явной recovery. Рекомендация:
    `interrupted`.

## 20. Handoff

### Рекомендуемые интерфейсы

- `StrategyCatalog` и `StrategyEventStore`;
- atomic `RiskDecisionWriter` либо `RiskDecisionTransactor`;
- `OrderLifecycleStore`;
- atomic `FillStore`;
- `ReconciliationReader`/`ReconciliationWriter`;
- `ControlStore`, `AuditReader`, query projections;
- `StatisticsStore`;
- отдельный `BacktestRunStore`;
- lifecycle-scoped `AgentLease`.

### Атомарные границы

1. strategy state + signals;
2. risk decision + allowed OrderIntent + audit;
3. order submission result + intent status + audit;
4. execution + order + position + P&L events + daily statistics + audit;
5. control/status transition + audit;
6. terminal backtest status + artifact manifest.

Ни одна граница не включает сетевой exchange call или запись artifact-файла.

### Следующий шаг

Интегратору следует принять спорные решения 1–9 перед реализацией domain и
SQLite. После этого Foundation & Storage agent может:

1. создать узкие Go contracts и storage errors;
2. написать backend-neutral contract suite;
3. утвердить SQLite driver/lock ADR;
4. создать migrations/sqlc queries по фактическим use cases;
5. реализовать SQLite adapter и прогнать suite на временной БД.

### Известные ограничения

- PostgreSQL adapter не проектируется до этапа 13;
- execution dedupe между T-Invest sources остаётся открытым sandbox-вопросом;
- attribution общей exchange position нескольким стратегиям не поддерживается;
- artifact cleanup/repair является отдельной operational задачей;
- schema окончательно зависит от принятых domain enums и ID/decimal ADR.
