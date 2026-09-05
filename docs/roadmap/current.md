# Текущий статус и roadmap

## Цель проекта

`lazytrade` — один Go-бинарник для автоматической торговли, исторического
тестирования и наблюдения за рынком. Первый поддерживаемый торговый контур —
T-Invest sandbox. Production trading намеренно выключен.

## Что реализовано

- Cobra CLI и строгая YAML-конфигурация версии 1.
- Загрузка `.env` при старте процесса без перезаписи уже заданных переменных.
- Decimal domain model: деньги разных asset нельзя неявно агрегировать.
- SQLite на `modernc.org/sqlite`, встроенные миграции 1–5 и single-agent lock.
- Exchange port, fake exchange и T-Invest adapter на
  `opensource.tbank.ru/invest/invest-go`.
- T-Invest instruments, portfolio, candles, prices, order book, trades,
  trading status, orders и execution stream.
- Несколько стратегий в одном процессе на одном exchange account, при условии
  что каждой принадлежит отдельный инструмент.
- Стратегии `moving_average_cross` и ограниченная DCA
  `periodic_investment`.
- Per-strategy `max_daily_loss` и `max_position_value` с явным asset.
- CSV OHLCV download/validation, deterministic backtest, simulated broker,
  комиссии, slippage, metrics, JSON report и trades CSV.
- Durable signal state, risk decisions, order intents, execution inbox,
  positions, P&L, cumulative commissions и audit events.
- Startup recovery: unresolved intents, history scan, execution inbox drain и
  reconciliation до перехода стратегий в `running`.
- Read-only API retries и conservative mutation `UnknownOutcome` handling.
- Диагностические команды sandbox: account list/create/pay-in/smoke-test,
  agent preflight и history probe.

## Что ещё не реализовано

- `terminal`: read-only TUI и `ntcharts` ещё не подключены; команда является
  заглушкой. Не использовать устаревшее упоминание `asciigraph` из
  `docs/archive/initial-product-vision.md` как архитектурное решение.
- Web API/dashboard, pause/resume UI и emergency-stop controls.
- Команда `db migrate` является заглушкой; миграции применяются при открытии
  SQLite store.
- Production T-Invest endpoint и любой live trading.
- Multi-exchange runtime: текущий agent требует один exchange для всех
  стратегий.
- Две стратегии на одном exchange/instrument: runtime хранит отображение
  `instrument -> worker`, а БД имеет соответствующую uniqueness-границу.
- Durable lifecycle отмены заявок в основном runtime и automatic cancellation
  при risk pause.
- PostgreSQL adapter. Есть только `db/postgres/README.md`; несколько процессов
  одновременно не являются требованием даже для будущего PostgreSQL.
- Полная DCA: basket, денежный бюджет, веса, daily/weekly schedule, независимый
  calendar scheduler, trading calendar и rebalance.

## Текущий milestone

Главный незакрытый milestone — доказать полный end-to-end round trip в реальном
T-Invest sandbox во время открытого рынка:

```text
market event -> strategy signal -> risk -> durable intent
-> sandbox order -> execution -> position/P&L -> restart recovery
```

Последняя фактическая попытка 2 августа 2026 года пришлась на воскресенье:
GAZP вернул `30079 Instrument is not available for trading`; ордер создан не
был. Поэтому наличие большого набора unit/integration tests не считается
подтверждением реального round trip.

## Зафиксированный порядок дальнейшей работы

1. Поддерживать зелёную локальную baseline: build, config validation, SQLite,
   `go test ./...` и релевантный race detector.
2. Выполнить read-only sandbox preflight: account, portfolio, instrument,
   trading status, market/execution streams и bounded history probe.
3. Во время торговой сессии выполнить один минимальный buy/sell smoke-test и
   проверить position, commissions, open orders и history bridge.
4. Запустить настоящий agent сначала с одной стратегией, затем с несколькими
   стратегиями на разных инструментах одного exchange.
5. Проверить crash/restart recovery на fake exchange и безопасных sandbox
   сценариях.
6. Закрыть оставшиеся пункты Order Execution/Reconciliation, включая durable
   cancellation lifecycle и terminal status projection.
7. Реализовать read-only TUI на Bubble Tea/Bubbles/Lip Gloss и `ntcharts` за
   внутренним chart adapter.
8. Реализовать Web API/dashboard и затем sandbox hardening/soak test.

PostgreSQL, [multi-exchange](multi-exchange.md) и каталог дополнительных
стратегий — этапы после sandbox MVP. Новая найденная идея не должна подменять
следующий пункт этого маршрута, если она не блокирует milestone.
