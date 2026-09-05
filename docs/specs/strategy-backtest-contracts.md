# Strategy, Risk & Backtest: предлагаемые контракты

## 1. Статус и область документа

Этот документ — handoff роли `Strategy, Risk & Backtest` для этапов 7, 8 и 8A.
Он уточняет FR-STRATEGY, FR-RISK, FR-STATS, FR-BACKTEST, NFR-001, NFR-009 и
NFR-010 из `implementation-plan.md`.

Контракты ниже предлагаемые. До их принятия интегратором они не должны
становиться причиной изменения общих domain/storage/exchange интерфейсов.
Имена пакетов могут быть адаптированы, но семантика, детерминизм и границы
ответственности должны сохраниться.

## 2. Основные инварианты

1. Одна реализация `Strategy` используется в live и backtest.
2. `Strategy` является детерминированным автоматом: вход — предыдущее состояние
   и одно событие; выход — новое состояние и ноль или больше сигналов.
3. Стратегия не получает `Broker`, `Storage`, exchange client, HTTP client,
   wall clock или генератор случайных чисел.
4. Все события одного strategy instance обрабатываются последовательно.
5. Каждый `Signal` обязательно проходит общий `RiskManager`.
6. Backtest отличается от live только реализациями портов:
   `MarketEventSource`, `Clock`, `Broker`, persistence/artifact sinks.
7. Денежные значения и количества используют decimal; `float32/float64`
   запрещены в доменных расчётах.
8. Значения разных asset нельзя складывать или сравнивать.
9. Signal, вычисленный по close свечи N, не может быть исполнен на свече N.
10. Порядок обработки не зависит от goroutine scheduling или iteration по map.

## 3. Базовые доменные типы

Ниже `decimal.Decimal` означает выбранный проектом decimal-тип.

```go
package domain

type StrategyID string
type SignalID string
type OrderID string
type ClientOrderID string
type ExecutionID string
type ExchangeAccountID string
type InstrumentID string

type Quantity struct {
    Value decimal.Decimal
}

type Price struct {
    Value decimal.Decimal
    Asset string
}

type Money struct {
    Amount decimal.Decimal
    Asset  string
}
```

`Asset` нормализуется в uppercase при построении типа. Конструкторы должны
отклонять пустой asset, отрицательное количество там, где оно недопустимо, и
невалидные decimal. Арифметика `Money` и сравнение `Price` разрешены только при
совпадающем asset.

## 4. Clock

```go
package clock

type Clock interface {
    Now() time.Time
}

type MutableClock interface {
    Clock

    // AdvanceTo разрешён только orchestration-коду backtest.
    // Перевод назад возвращает ошибку.
    AdvanceTo(t time.Time) error
}
```

Реализации:

- `SystemClock`: возвращает UTC wall clock, используется в agent mode;
- `VirtualClock`: начинается в явно заданной точке и изменяется runner;
- `FixedClock`: тестовая реализация.

Правила:

- `Now()` всегда возвращает время с UTC location;
- risk, P&L, trading-day boundary, broker и runner получают один и тот же
  `Clock`;
- стратегия времени не запрашивает: текущее логическое время содержится в
  событии;
- `VirtualClock` не запускает timers и не использует `time.Now`;
- timestamps запуска/завершения отчёта, относящиеся к wall-clock metadata,
  собираются вне детерминированного результата.

## 5. MarketEvent и источник событий

```go
package domain

type MarketEventKind uint8

const (
    MarketEventCandleOpen MarketEventKind = iota + 1
    MarketEventTrade
    MarketEventOrderBook
    MarketEventCandleClose
)

type EventCursor struct {
    Timestamp time.Time
    Priority  uint16
    Sequence  uint64
}

type MarketEvent struct {
    ExchangeAccountID ExchangeAccountID
    InstrumentID      InstrumentID
    Kind              MarketEventKind
    ExchangeTime      time.Time
    ReceivedTime      time.Time
    Sequence          uint64
    Candle            *Candle
    Trade             *MarketTrade
    OrderBook         *OrderBook
}

type Candle struct {
    Start     time.Time
    End       time.Time
    Interval  time.Duration
    Open      Price
    High      Price
    Low       Price
    Close     Price
    Volume    Quantity
    Complete  bool
}
```

Для candle-based backtest CSV-строка создаёт одно `CandleClose` event с
`ExchangeTime == Candle.End`, `Complete == true`. `Open` этой свечи доступен
broker для исполнения ранее принятых заявок, но стратегия получает полную
свечу только в момент `Candle.End`.

```go
package market

type MarketEventSource interface {
    Events(ctx context.Context) (<-chan domain.MarketEvent, <-chan error)
}
```

Контракт source:

- каждый канал имеет одного consumer;
- события выдаются уже в каноническом порядке;
- после последнего события оба канала закрываются;
- максимум одна terminal error; после неё канал событий закрывается;
- отмена context прекращает producer и закрывает каналы;
- source не должен создавать неограниченную очередь;
- CSV source работает потоково и не загружает весь dataset в память.

Для backtest предпочтительнее синхронный внутренний контракт, исключающий
зависимость результата от channel scheduling:

```go
type EventIterator interface {
    Next(ctx context.Context) (domain.MarketEvent, error)
}
```

`io.EOF` означает корректное завершение. Channel-based `MarketEventSource`
может быть adapter над iterator. Сам runner должен читать событие, полностью
обрабатывать его и только затем читать следующее.

## 6. Канонический порядок событий

`EventCursor` сравнивается лексикографически:

1. `Timestamp` по UTC instant;
2. `Priority` по фиксированной таблице;
3. `Sequence` по порядку записи в dataset.

Для candle-only MVP одна строка имеет один `CandleClose`, поэтому priority не
влияет на результат. Для расширенного источника предлагается порядок:

```text
10  broker boundary / candle open для заявок прошлых событий
20  external execution/order update
30  trade
40  order-book update
50  completed candle / strategy evaluation
60  risk/P&L snapshot
```

Внутренние события не нужно маскировать под `MarketEvent`; priority является
правилом orchestration runner. Если timestamps равны, заявка, созданная на
priority 50, не может быть обработана broker на priority 10 того же timestamp:
её `EligibleAfter` строго позже causative cursor.

Dataset validator обязан отклонить:

- невозрастающие candle timestamps;
- одинаковые timestamps;
- sequence regression;
- невалидный OHLC;
- interval mismatch;
- gaps при `gap_policy: fail`.

При `allow` gap не заполняется. При `mark` runner добавляет warning/marker, но
не синтетическую цену.

## 7. Strategy и versioned state

Вместо передачи opaque state и неявного изменения предлагается явный результат:

```go
package strategy

type StateEnvelope struct {
    StrategyType string
    Version      uint32
    Payload      json.RawMessage
}

type Input struct {
    StrategyID       domain.StrategyID
    ExchangeAccount  domain.ExchangeAccountID
    InstrumentID     domain.InstrumentID
    Event            domain.MarketEvent
}

type Result struct {
    State   StateEnvelope
    Signals []domain.SignalDraft
}

type Strategy interface {
    Type() string
    RequiredData() DataRequirements
    InitialState() (StateEnvelope, error)
    OnEvent(
        ctx context.Context,
        state StateEnvelope,
        input Input,
    ) (Result, error)
}
```

`SignalDraft` не содержит самостоятельно сгенерированный ID:

```go
package domain

type SignalAction uint8

const (
    SignalBuy SignalAction = iota + 1
    SignalSell
    SignalClose
)

type OrderType uint8

const (
    OrderTypeMarket OrderType = iota + 1
    OrderTypeLimit
)

type SignalDraft struct {
    Action     SignalAction
    OrderType  OrderType
    Quantity   Quantity
    LimitPrice *Price
    ReasonCode string
    Reason     string
}

type Signal struct {
    ID                 SignalID
    StrategyID         StrategyID
    ExchangeAccountID  ExchangeAccountID
    InstrumentID       InstrumentID
    Action             SignalAction
    OrderType          OrderType
    Quantity           Quantity
    LimitPrice         *Price
    ReasonCode         string
    Reason             string
    CreatedAt          time.Time
    CausativeCursor    EventCursor
    Ordinal            uint16
}
```

Worker валидирует result, атомарно сохраняет state и назначает Signal ID.
Предлагаемый детерминированный ID:

```text
SHA-256(
  "signal/v1" || strategy_id || normalized_event_cursor ||
  ordinal_within_result || canonical_signal_payload
)
```

ID кодируется lowercase hex или UUID-like представлением, выбранным domain.
`CreatedAt` равен логическому timestamp события, а не wall clock. `Ordinal`
равен индексу сигнала в возвращённом slice; стратегии запрещено строить slice
через неотсортированный map.

Правила state:

- `StrategyType` обязан совпадать с `Strategy.Type()`;
- `Version >= 1`;
- payload — canonical JSON без runtime timestamps и случайных ID;
- конкретная стратегия декодирует только поддерживаемые версии;
- миграция выполняется явной цепочкой `vN -> vN+1`;
- неизвестная будущая версия приводит к `blocked`, не к silent reset;
- `InitialState` используется только при доказанном отсутствии persisted state;
- повреждённый payload не заменяется initial state автоматически;
- state после каждого успешно обработанного события сохраняется вместе с
  signals в одной транзакционной границе live runtime;
- backtest хранит state в памяти, но может включить финальный envelope и его
  checksum в report.

`RequiredData` должен быть декларативным:

```go
type DataRequirements struct {
    CandleIntervals []time.Duration
    Trades          bool
    OrderBookDepth  int
    WarmupEvents    uint64
}
```

Registry:

```go
type Factory interface {
    Type() string
    Build(rawConfig json.RawMessage) (Strategy, error)
}
```

Factory валидирует и canonicalizes параметры. Runtime не меняет конфигурацию
стратегии после запуска.

## 8. Broker

Стратегия не видит broker. Execution pipeline после risk decision использует:

```go
package broker

type SubmitRequest struct {
    OrderID             domain.OrderID
    ClientOrderID       domain.ClientOrderID
    StrategyID          domain.StrategyID
    ExchangeAccountID   domain.ExchangeAccountID
    InstrumentID        domain.InstrumentID
    Side                domain.OrderSide
    Type                domain.OrderType
    Quantity            domain.Quantity
    LimitPrice          *domain.Price
    SubmittedAt         time.Time
    CausativeCursor     domain.EventCursor
}

type Broker interface {
    Submit(ctx context.Context, request SubmitRequest) (domain.Order, error)
    Cancel(ctx context.Context, orderID domain.OrderID) (domain.Order, error)
    OpenOrders(ctx context.Context, strategyID domain.StrategyID) ([]domain.Order, error)
}

type EventDrivenBroker interface {
    Broker
    OnMarketEvent(
        ctx context.Context,
        event domain.MarketEvent,
    ) ([]domain.Execution, error)
}
```

Live broker является adapter над exchange execution service. Simulated broker
является синхронным deterministic state machine:

```go
type SimulatedBroker interface {
    EventDrivenBroker
    Snapshot() PortfolioSnapshot
}
```

Общие требования:

- duplicate `ClientOrderID` с тем же payload возвращает существующий order;
- duplicate `ClientOrderID` с другим payload возвращает conflict;
- результаты `OpenOrders` отсортированы по `(SubmittedAt, OrderID)`;
- `OnMarketEvent` возвращает executions в `(ExecutedAt, OrderID, ExecutionID)`
  порядке;
- cancellation идемпотентен для уже cancelled order;
- partial fills отсутствуют только в candle MVP simulated broker, но доменная
  модель поддерживает их для live.

Live и simulated broker не обязаны иметь идентичный внутренний lifecycle.
Общими являются `SubmitRequest`, `Order`, `Execution` и потребляющий их
execution/P&L pipeline.

## 9. Simulated broker: fill model MVP

### 9.1. Eligibility

После обработки candle N стратегия создаёт signal. Если risk разрешил его,
order становится `accepted`, но:

```text
EligibleAfter = causative candle End
EarliestCandle = первая свеча с Start >= causative candle End
```

На gaps broker не выдумывает промежуточную свечу. Первой eligible является
следующая реально существующая свеча.

### 9.2. Market: `next_open`

Market order исполняется целиком на open первой eligible свечи:

```text
raw fill = next candle open
buy fill  = raw fill + adverse slippage
sell fill = raw fill - adverse slippage
```

Если следующей свечи нет, order остаётся open/unfilled; report содержит warning.

### 9.3. Limit: `touch`

Limit order рассматривается только на eligible candle:

- buy touched, если `Low <= limit`;
- sell touched, если `High >= limit`.

Предлагаемая price-improvement политика:

```text
buy:  raw fill = min(Open, Limit), если Low <= Limit
sell: raw fill = max(Open, Limit), если High >= Limit
```

Это моделирует gap через limit: buy не платит выше limit, sell не продаёт ниже
limit. После этого применяется adverse slippage, но итоговая цена limit order
должна быть capped:

```text
buy fill  = min(Limit, raw fill + slippage)
sell fill = max(Limit, raw fill - slippage)
```

Если принято более консервативное правило `fill exactly at limit`, его нужно
зафиксировать в config/report как отдельную модель; смешивать правила нельзя.

### 9.4. Ambiguous candle

В MVP нет stop orders и bracket/OCO, поэтому одна limit заявка имеет однозначный
touch. Если расширение создаёт несколько взаимоисключающих fills на одной
свече, применяется worst-case для стратегии:

1. сначала исполнения, уменьшающие equity;
2. затем risk pause/cancellation;
3. затем исполнения, увеличивающие equity;
4. tie-breaker `(SubmittedAt, OrderID)`.

Если worst-case нельзя вычислить однозначно без знания intrabar path, run
завершается validation/model error. Случайный OHLC path запрещён по умолчанию.

### 9.5. Fees and slippage

Процент комиссии в config измеряется в процентах:

```text
0.03 = 0.03% = 0.0003 fraction
commission = abs(fill price * quantity) * value / 100
```

Basis points:

```text
5 bp = 0.05% = 0.0005 fraction
slippage delta = raw price * value / 10000
```

Commission округляется по явно заданному currency precision/rounding mode.
Slippage применяется к цене до расчёта комиссии. Все assumptions и rounding
rules входят в report.

Volume по умолчанию не ограничивает fill: candle volume используется как
информационное поле. Это обязательное warning/assumption отчёта.

## 10. RiskManager и RiskDecision

```go
package risk

type DecisionKind uint8

const (
    DecisionAllow DecisionKind = iota + 1
    DecisionReject
    DecisionPause
)

type ReasonCode string

const (
    ReasonAssetMismatch       ReasonCode = "asset_mismatch"
    ReasonMaxPositionValue    ReasonCode = "max_position_value"
    ReasonMaxDailyLoss        ReasonCode = "max_daily_loss"
    ReasonStrategyNotRunning  ReasonCode = "strategy_not_running"
    ReasonInvalidSignal       ReasonCode = "invalid_signal"
    ReasonIncompletePnL       ReasonCode = "incomplete_pnl"
)

type Snapshot struct {
    StrategyID       domain.StrategyID
    Status           domain.StrategyStatus
    TradingDay       TradingDay
    Position         domain.Position
    PnL              domain.StrategyPnL
    OpenOrders       []domain.Order
    Instrument       domain.Instrument
}

type Decision struct {
    Kind             DecisionKind
    SignalID         domain.SignalID
    ReasonCode       ReasonCode
    Reason           string
    EvaluatedAt      time.Time
    EffectiveStatus  domain.StrategyStatus
    Order            *domain.NewOrder
    CancelOrderIDs   []domain.OrderID
    AuditPayload     json.RawMessage
}

type Manager interface {
    Evaluate(
        ctx context.Context,
        signal domain.Signal,
        snapshot Snapshot,
    ) (Decision, error)

    ObservePnL(
        ctx context.Context,
        snapshot Snapshot,
    ) (Decision, error)
}
```

Семантика:

- `error` означает, что решение безопасно вычислить невозможно; engine не
  создаёт order;
- `Reject` блокирует конкретный signal, не меняя статус;
- `Pause` переводит стратегию в `risk_paused`, блокирует signal и может вернуть
  список opening orders для отмены;
- `Allow` содержит полностью нормализованный `NewOrder`;
- решение является value object и сохраняется/audited до side effects;
- причины имеют стабильный machine-readable code;
- порядок `CancelOrderIDs` детерминирован;
- достижение лимита ровно на границе (`daily PnL <= -limit`) означает Pause;
- mismatch asset всегда error/reject согласно принятой общей error taxonomy,
  но никогда не конвертация;
- `risk_paused` не снимается при смене дня;
- manual resume находится в control service, не в `RiskManager`.

`ObservePnL` нужен для pause после изменения equity/fill даже при отсутствии
нового signal. И live, и backtest вызывают его после каждого P&L-changing
execution/valuation event.

Для `total` P&L при неполных данных требуется явная completeness policy.
Предлагаемый безопасный default: `DecisionPause` с
`ReasonIncompletePnL`, а не продолжение торговли с неполным total P&L.

## 11. Общий runtime pipeline

Один event обрабатывается до конца синхронно в следующем порядке:

```text
source.Next
→ virtual/system logical time
→ broker.OnMarketEvent (fills ранее принятых orders)
→ position/P&L apply executions
→ risk.ObservePnL
→ применить pause/cancellations
→ strategy.OnEvent
→ persist new strategy state
→ assign deterministic Signal IDs
→ risk.Evaluate каждого signal по ordinal
→ persist decision / allowed OrderIntent
→ broker.Submit
→ persist order
→ report/statistics sinks
→ следующее source event
```

В live runtime persistence steps должны соответствовать engine transaction
boundaries из основного ТЗ. В backtest те же domain services работают с
in-memory repositories/sinks; simulated broker не обходит risk.

Порядок `broker before strategy` на свече N обеспечивает next-open: orders,
созданные стратегией после close N, broker впервые увидит на N+1.

## 12. Граница live/backtest reuse

Обязательно общие:

- `Strategy`, registry и strategy config validation;
- `Signal`, deterministic Signal ID algorithm;
- `RiskManager`, limits и status transition rules;
- `Order`, `Execution`, `Position`, P&L domain services;
- trading-day calculation;
- decimal/rounding rules;
- metrics calculations, если применимы live statistics;
- execution pipeline orchestration на уровне domain/application service.

Различаются:

| Порт | Live | Backtest |
|---|---|---|
| Market events | exchange stream | validated dataset iterator |
| Clock | `SystemClock` | `VirtualClock` |
| Broker | exchange adapter facade | `SimulatedBroker` |
| State repository | SQLite | in-memory + optional final artifact |
| Event/order sink | SQLite/audit | report artifact + run metadata |
| IDs | persistent generator where needed | deterministic run-scoped generator |
| Runtime metadata | wall clock allowed | excluded from reproducibility compare |

Запрещено:

- `if backtest { ... }` внутри стратегии;
- отдельная backtest-стратегия;
- передача всей будущей series стратегии;
- exchange credentials/client в dependency graph backtest command;
- wall clock в risk/P&L/fill decisions;
- mock live `Exchange.PlaceOrder` как simulated broker;
- различающиеся формулы P&L для live и backtest без declared completeness
  differences.

## 13. Детерминизм и воспроизводимость

Для идентичного `(application version, normalized config hash, dataset checksum,
seed)` одинаковы:

- порядок signals, orders, executions и risk events;
- их semantic IDs;
- fill prices, fees, positions и P&L;
- metrics;
- warnings и assumptions;
- deterministic report payload.

Могут отличаться и исключаются из semantic comparison:

- backtest run ID;
- wall-clock `started_at`/`finished_at`;
- абсолютный output path;
- duration/throughput telemetry.

Правила реализации:

- canonical config сериализуется с фиксированным порядком полей;
- checksum dataset считается по исходным bytes плюс нормализованная metadata
  version либо эти checksum сохраняются раздельно;
- map перед сериализацией/обработкой преобразуется в sorted slice;
- seed обязателен в metadata, даже если текущая модель его не использует;
- RNG создаётся только через явный seeded port; candle MVP RNG не использует;
- goroutine не участвуют в обработке одного run;
- report schema и strategy state имеют независимые версии;
- decimal rounding mode фиксирован конфигурацией/версией модели.

## 14. Версии схем

Предлагаемые начальные версии:

```text
strategy state envelope: 1
backtest report schema:   1
fill model:               candle-next-open-touch/v1
signal ID algorithm:      signal/v1
config normalization:     config/v1
dataset metadata:         dataset/v1
```

Версии сохраняются в report. Изменение fill/rounding/ID semantics требует новой
версии модели и не должно менять воспроизводимость старых runs.

## 15. Минимальные golden fixtures

Нужны небольшие CSV datasets с ручным расчётом:

1. `market-next-open.csv`: сигнал на close N, fill строго на open N+1;
2. `limit-touch-gap.csv`: buy/sell touch, gap price improvement и no-touch;
3. `fees-slippage.csv`: точные commission/slippage и rounding;
4. `daily-loss-boundary.csv`: P&L ровно `-limit`, переход `risk_paused`;
5. `trading-day-timezone.csv`: граница дня в не-UTC timezone без auto resume;
6. `ambiguous-bar.csv`: worst-case policy или ожидаемая model error;
7. `gaps.csv`: `fail`, `allow`, `mark`;
8. `look-ahead.csv`: результат меняется только после доступности N+1;
9. `state-restore.json`: version 1 state и corrupted/future version cases.

Каждый fixture сопровождается expected JSON с decimal strings и пояснением
ручного расчёта. Golden output не содержит wall-clock metadata и случайный run
ID.

## 16. Ограничения модели MVP

- Только OHLCV candles; intrabar path неизвестен.
- Нет частичных fills и моделирования позиции в очереди.
- Candle volume не ограничивает fill.
- Нет latency, spread и market impact кроме configured adverse slippage.
- Нет stop/OCO/bracket orders до определения ambiguous-bar semantics.
- Нет portfolio backtest нескольких стратегий с общим капиталом.
- Один strategy instance — один instrument и один settlement asset.
- Corporate actions, dividends, funding и FX учитываются только если dataset
  содержит отдельные нормализованные события; молчаливые assumptions запрещены.
- Оставшийся open order/position в конце dataset не закрывается автоматически.
  Unrealized P&L оценивается по последнему доступному close и помечается как
  modeled valuation.

## 17. Спорные решения для интегратора

До реализации соответствующей части нужно принять:

1. **Форма Strategy API.** Принять чистый `Result{State, Signals}` или оставить
   state mutable/opaque. Рекомендация: чистый result для детерминизма.
2. **Signal ID ownership.** Рекомендация: ID назначает worker из cursor +
   ordinal + payload; стратегия возвращает `SignalDraft`.
3. **Синхронный source.** Рекомендация: backtest runner использует
   `EventIterator`; channel interface остаётся adapter boundary.
4. **Candle representation.** Нужны ли отдельные `CandleOpen`/`CandleClose`.
   Рекомендация MVP: одна completed candle стратегии, но broker обрабатывает её
   open до передачи completed candle стратегии.
5. **Limit gap improvement.** Выбрать `min/max(open, limit)` или always limit.
   Рекомендация: improvement с capped adverse slippage.
6. **Incomplete total P&L.** Рекомендация: fail-safe pause; возможна менее
   строгая явно настроенная policy.
7. **Risk persistence boundary.** Уточнить атомарность state + signals +
   decisions относительно существующих storage repositories.
8. **Execution ordering на одинаковом timestamp.** Принять priority table выше
   как общий engine contract.
9. **State encoding.** JSON проще для миграции/debug; protobuf компактнее.
   Рекомендация MVP: canonical JSON envelope.
10. **Dataset checksum.** Рекомендация: отдельно `content_sha256` исходного файла
    и `metadata_sha256`, чтобы перенос файла не менял identity.
11. **End-of-data policy.** Рекомендация: не force-close; mark-to-last-close и
    warning.
12. **Percentage commission semantics.** Подтвердить, что config `0.03`
    означает `0.03%`, а не fraction `0.03`.
13. **Attribution constraint.** До общего portfolio accounting запретить две
    торгующие стратегии на одном `(exchange account, instrument)`.

## 18. Handoff

Реализуемые этапы: 7, 8, 8A.

Ключевые контракты:

- pure/versioned `Strategy`;
- deterministic worker-owned `SignalID`;
- shared `Clock`;
- synchronous deterministic `EventIterator` для backtest;
- общий `Broker` domain boundary и отдельный `EventDrivenBroker`;
- fail-safe `RiskDecision`;
- shared Position/P&L pipeline.

Обязательные проверки перед merge реализации:

- deterministic signals и state restore;
- future state/corrupted payload переводит scope в safe state;
- next-open и no same-candle fill;
- touch/no-touch/gap limit cases;
- exact commission/slippage decimal calculations;
- risk limit ровно на границе;
- day boundary/timezone и отсутствие auto resume;
- asset mismatch;
- cancellation;
- no network dependency from backtest;
- identical semantic reports при повторном запуске;
- race test для live workers, хотя backtest loop должен быть однопоточным.

Известные ограничения и assumptions перечислены в разделах 9 и 16 и должны
попадать в report, а не только в документацию или logs.
