# Архитектура

## Общая форма

Проект — modular monolith и один Go-бинарник с подкомандами. Основное правило
зависимостей: domain и application contracts не должны импортировать SDK биржи,
SQL или конкретный storage driver.

```text
cmd/lazytrade
  -> internal/cli             composition и команды
     -> internal/agent        live orchestration/recovery
     -> internal/app          backtest и data workflows
        -> internal/strategy  чистая логика стратегий и Worker
        -> internal/risk      per-strategy risk gates
        -> internal/exchange  нормализованный exchange port
        -> internal/storage   repository contracts

infrastructure adapters:
  internal/exchange/tinvest
  internal/exchange/fake
  internal/storage/sqlite
```

## Ключевые решения

### Деньги и количества

- Используется `shopspring/decimal`; `float64` для денег запрещён.
- Каждая денежная величина содержит asset.
- Суммы разных asset не агрегируются без явной conversion policy.
- Risk limits определяются для каждой стратегии и в её settlement asset.
- Глобального абсолютного `max_daily_loss` нет и добавлять его не нужно.

### Конфигурация и секреты

- Один strict YAML-файл содержит database, exchanges, agent, terminal и
  backtest sections.
- Секреты задаются именами environment variables, а не значениями в YAML.
- Корневой CLI загружает `.env` из текущего каталога и не перезаписывает уже
  заданное окружение.
- Значения token, account ID и других credentials нельзя логировать или
  помещать в fixtures.

### Exchange boundary

- `internal/exchange.Exchange` — нормализованный интерфейс.
- SDK T-Invest остаётся только внутри `internal/exchange/tinvest`.
- `Name()` адаптера в runtime используется как логический
  `ExchangeAccountID`; реальный broker account UUID хранится внутри адаптера и
  отправляется в API. Не подменять эти два идентификатора.
- Текущий agent поддерживает только T-Invest sandbox и один adapter/account на
  процесс. Целевая архитектура допускает будущий multi-exchange dispatcher.

### Стратегии

- `Strategy` — чистая state machine: `InitialState`, `RequiredData`, `OnEvent`.
- `Worker` сериализует события, проверяет cursor и атомарно сохраняет новое
  versioned state вместе с generated signals.
- Signal ID детерминирован из strategy ID, event cursor, ordinal и payload.
- Live и backtest строят built-in стратегии через общий
  `internal/strategy/builtin` composition point.
- Live runtime создаётся через `agent.NewRuntime(RuntimeConfig)`. Каждая
  стратегия задаётся одним `StrategyBinding`, который связывает strategy ID,
  instrument, worker, risk gate, subscription и trading-day policy. Отдельных
  single-strategy полей и параллельных routing maps нет.
- Текущий runtime маршрутизирует события по instrument ID, поэтому один
  инструмент может принадлежать только одной strategy instance.

### Storage

- Текущий driver — SQLite без CGO (`modernc.org/sqlite`).
- SQLite ограничивается одним одновременно работающим agent; store использует
  lock и одно открытое соединение там, где это требуется для корректности.
- Схема развивается append-only миграциями. Не редактировать применённую
  миграцию; добавлять следующую.
- PostgreSQL предусмотрен через те же storage contracts, но не реализован.
  Distributed/multi-process coordination пока не требуется.

### UI

- TUI должен быть read-only.
- Выбранный chart package — `ntcharts`, изолированный за внутренним adapter.
- Bubble Tea, Bubbles, Lip Gloss и `ntcharts` пока не добавлены в `go.mod`:
  решение принято, реализация этапа TUI не начата.
- Торговые mutations не должны появляться в terminal code path.

## Основные данные SQLite

- `strategy_instances`, `strategy_states`, `strategy_lifecycle`, `signals`;
- `risk_decisions`, `order_intents`, `orders`;
- `execution_inbox`, `order_executions`, `order_commissions`;
- `positions`, `equity_snapshots`, `pnl_events`, `daily_statistics`;
- `control_states`, `audit_events`;
- `backtest_runs`, `backtest_artifacts`.

Текущая schema version — 6. Execution inbox является durable ingress, а не
временной очередью в памяти. Cumulative commission хранится отдельно, чтобы
применять только положительную дельту и не задваивать комиссию.

Lifecycle стратегии хранится отдельно от event-state. Строка lifecycle
создаётся атомарно при регистрации instance, поэтому startup status наблюдаем
до первого market event. Детали: [ADR-0005](adr/0005-separate-strategy-lifecycle.md).

## Будущие изменения, требующие отдельного решения

- Multi-exchange: отдельный adapter/runtime scope, checkpoint и failure domain
  на каждый exchange account.
- Несколько стратегий на одном инструменте: маршрутизация к нескольким workers
  и однозначное владение общей exchange position/P&L.
- Полная DCA-корзина: calendar events, occurrence ledger, allocation items и
  budget-to-lot sizing.
- Durable cancellation command: отдельная сущность, а не новый `OrderStatus`.
