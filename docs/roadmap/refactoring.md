# План упрощения и укрепления кодовой базы

## Назначение

Этот план фиксирует результаты аудита кодовой базы от 6 сентября 2026 года.
Его цель — удалить параллельные и устаревшие пути выполнения, сделать
архитектурные инварианты явными и упростить дальнейшее развитие без изменения
уже реализованных пользовательских сценариев.

Рефакторинг является поддерживающим потоком работ. Он не заменяет основной
sandbox milestone из [`current.md`](current.md): задачи берутся из этого плана,
когда они уменьшают риск ближайшего end-to-end запуска или могут быть выполнены
без задержки проверки во время открытого рынка.

## Неподвижные условия

- Все существующие реализованные сценарии должны продолжать работать.
- Торговые гарантии из `AGENTS.md` и
  [`trading-runtime.md`](../architecture/trading-runtime.md) не ослабляются.
- Миграции SQLite остаются forward-only; применённые миграции не редактируются.
- Для заменяемого механизма остаётся один канонический путь. Compatibility и
  fallback-код не сохраняются без подтверждённого внешнего требования.
- Каждый этап выполняется отдельным небольшим, проверяемым изменением.
- Перед изменением сложного поведения добавляются characterization/regression
  tests. Тесты не переписываются так, чтобы скрыть регрессию.
- После каждого этапа выполняются тесты затронутых пакетов и полный `go test`;
  для runtime, storage, streams и concurrency дополнительно запускается race
  detector.
- Существенные принятые решения обновляются в `docs/` в том же изменении.

## Подтверждённая исходная точка

Перед составлением плана успешно выполнены:

```sh
go vet ./...
go test -timeout 60s ./...
go test -race -timeout 90s \
  ./internal/agent \
  ./internal/storage/sqlite \
  ./internal/exchange/tinvest
```

## Этапы

### R1. Единственный источник схемы SQLite

Статус: реализовано 6 сентября 2026 года.

Проблема: `db/sqlite/schema.sql`, используемый `sqlc`, расходится с итоговой
схемой, создаваемой миграциями. В частности, snapshot пропускает часть таблиц,
индексов и ограничений, но содержит объекты из более поздних миграций.

Работы:

1. Настроить `sqlc` на чтение каталога forward-миграций в порядке имён.
2. Удалить отдельный schema snapshot.
3. Убедиться, что `sqlc generate` не изменяет сгенерированный Go-код.
4. Добавить воспроизводимую проверку генерации в development workflow.

Критерии готовности:

- миграции являются единственным описанием схемы;
- `sqlc generate` завершается успешно;
- generated code не имеет неожиданного diff;
- storage и полный набор тестов зелёные.

### R2. Один долговечный путь записи execution

Статус: реализовано 6 сентября 2026 года.

Проблема: наряду с обязательным `StageExecution -> ApplyStagedExecution`
сохранился прямой `RecordExecution`, позволяющий обойти durable inbox.

Работы:

1. Перевести тестовые fixture на stage/apply путь.
2. Удалить `Runtime.recordExecution`, `Store.RecordExecution` и
   `ExecutionStore`.
3. Проверить, что live stream и history recovery используют один ingress.

Регрессионные проверки:

- duplicate execution остаётся идемпотентным;
- checksum conflict отклоняется;
- inbox и projections меняются атомарно;
- crash между stage/apply безопасно восстанавливается.

### R3. Один атомарный путь создания разрешённого intent

Статус: реализовано 6 сентября 2026 года.

Проблема: прямые `CreateOrderIntent` и `RecordIntentAndAudit` сосуществуют с
каноническим `RecordAllowedDecisionIntent`.

Работы:

1. Перевести тестовые данные на канонический API или package-local helpers.
2. Удалить `IntentAuditStore` и прямой `RecordIntentAndAudit`.
3. Удалить `CreateOrderIntent`, если после миграции тестов нет потребителей.
4. Оставить узкие read-интерфейсы для reconciliation.

Критерий: production-код не может создать разрешённый order intent отдельно от
соответствующего risk decision и audit trail.

### R4. Явная multi-strategy композиция runtime

Статус: реализовано 6 сентября 2026 года.

Проблема: runtime одновременно принимает одиночные и map-based поля (`Worker` /
`Workers`, `Risk` / `Risks`, `Subscription` / `Subscriptions`, trading-day
callbacks), а затем неявно нормализует старый формат.

Целевой контракт:

```go
type StrategyBinding struct {
    ID            domain.StrategyID
    InstrumentID  domain.InstrumentID
    Worker        *strategy.Worker
    Risk          SignalRisk
    Subscription  exchange.Subscription
    TradingDayKey func(time.Time) string
}

type RuntimeConfig struct {
    Exchange   TradingExchange
    Store      RuntimeStore
    Strategies []StrategyBinding
}
```

Работы:

1. Добавить конструктор, валидирующий полную композицию до startup.
2. Перевести CLI и тесты на `StrategyBinding`.
3. Удалить одиночные поля, empty strategy ID и compatibility normalization.

Критерий: невозможно создать частично сконфигурированный runtime или смешать
старую и новую формы.

### R5. Долговечный lifecycle до первого market event

Статус: реализовано 6 сентября 2026 года.

Проблема: обновление lifecycle игнорирует `storage.ErrNotFound`, пока для новой
стратегии не появилась первая строка event-state. Падение до первого события
может не оставить наблюдаемого `reconciling`, `blocked` или `stopped`.

Работы:

1. Добавить regression test запуска новой стратегии без market events.
2. Отделить lifecycle от event-state новой таблицей и новой миграцией либо
   создать равнозначный явно инициализируемый persistence contract.
3. Удалить подавление `ErrNotFound`.

Критерий: каждый startup оставляет долговечное и однозначное lifecycle state
независимо от наличия рыночных событий.

### R6. Безопасная атрибуция execution после рестарта

Проблема: T-Invest adapter требует in-memory order context, но execution stream
может получить fill до регистрации восстановленных ордеров.

Работы:

1. Добавить тест, где fill приходит сразу после рестарта.
2. Краткосрочно регистрировать все локальные order contexts до открытия stream.
3. Затем перенести атрибуцию strategy ID в application/storage слой: adapter
   отдаёт raw exchange identity, а локальный долговечный state определяет
   стратегию.
4. После переноса удалить `OrderContextRegistrar`.

Критерий: ранний fill не теряется и не завершает execution ingress.

### R7. Один контракт stream supervision

Проблема: T-Invest adapter реализует reconnect, но runtime завершает работу при
первой stream error, поэтому reconnect фактически недостижим.

Текущее решение для sandbox MVP: одноразовый fail-closed stream. Adapter
возвращает terminal error, runtime блокируется и требует recovery/restart.

Работы:

1. Зафиксировать текущее fail-closed поведение тестами.
2. Удалить внутренний reconnect/backoff и поколения соединения.
3. Упростить stream state model до событий и terminal error.

Полноценный reconnect можно вернуть отдельным решением только вместе с
`degraded` state, запретом новых сигналов во время разрыва и soak tests.

### R8. Строгий mapping внешних данных T-Invest

Работы:

1. Заменить enum default branches на `(value, error)` mapping.
2. Не превращать неизвестные order direction/type в валидные торговые значения.
3. Преобразовывать OHLC явно в фиксированном порядке без pointer-key map.
4. Сделать порядок ошибок `NewOrder.Validate` детерминированным.
5. Добавить тесты на unknown enum, nil quotation и nil timestamp.

Критерий: неизвестные или неполные внешние данные обрабатываются fail closed.

### R9. Разделение обязанностей runtime

Этот этап выполняется после R4–R8, когда поведение уже закреплено контрактами.

Целевая структура:

```text
runtime.go
startup.go
market_loop.go
signal_pipeline.go
order_submission.go
execution_ingress.go
```

Разделение должно вводить явный результат startup/recovery фазы, а не только
переносить функции между файлами. Порядок recovery, reconciliation и перехода в
`running` остаётся неизменным и проверяется тестом.

### R10. Устранение дублирования composition-кода

Работы:

1. В CLI централизовать открытие настроенного T-Invest adapter: token, account,
   CA, sandbox-only policy и обработку exchange alias.
2. Пока не создавать универсальную multi-exchange factory: реализован только
   один adapter.
3. Объединить построение live/backtest `risk.Config` и trading-day policy.
4. Добавить parity test для одинаковой strategy/risk конфигурации.

### R11. Единая подготовка backtest dataset

Проблема: dataset хешируется и читается дважды, что допускает расхождение между
metadata и реально обработанными данными.

Работы:

1. Ввести `preparedBacktestRun` с один раз разрешёнными абсолютными путями.
2. Обрабатывать неизменяемый snapshot dataset или проверять checksum после
   итерации до фиксации результата.
3. Возвращать ошибки разрешения путей вместо silent fallback.
4. Ограничить timeout финальной persistence операции после cancellation.
5. Добавить regression test изменения файла между preparation и execution.

### R12. Удаление оставшегося speculative и legacy кода

Кандидаты удаляются только после повторной проверки usages:

- неиспользуемый `Exchange.Capabilities`;
- alias `MovingAverageCrossParams`;
- test-only `AllowAllRisk` из production files;
- pass-through alias `tinvest.CandleQuery`;
- неиспользуемый `AgentLease`;
- двойная config validation;
- лишние ветви `isDecimalZero`;
- незавершённые CLI-команды `db migrate` и `terminal`.

Удаление CLI-заглушек выполняется отдельно, поскольку меняет observable
`--help`, хотя не удаляет работающий пользовательский сценарий.

## Проверка каждого этапа

Минимальный gate:

```sh
make fmt
make test PKG=<затронутые пакеты>
make test
```

Для storage/runtime/exchange/concurrency:

```sh
go test -race -timeout 90s <затронутые пакеты>
```

Для изменений схемы и запросов:

```sh
make sqlc-check
```

Перед завершением всего плана:

```sh
go vet ./...
go test -timeout 60s ./...
go test -race -timeout 90s \
  ./internal/agent \
  ./internal/storage/sqlite \
  ./internal/exchange/tinvest
```

## Не входящие в рефакторинг наблюдения

В рабочем каталоге во время аудита находился неотслеживаемый файл `1` с
JSON-логами, где записи были продублированы. Файл не следует автоматически
удалять или игнорировать: сначала нужно проверить способ запуска, число agent
processes и состав logging handlers. Это отдельная диагностическая задача, а не
основание менять logger без воспроизводящего теста.
