# AGENTS.md

## Перед началом

Прочитай в таком порядке:

1. `docs/roadmap/current.md` — фактический статус и зафиксированный маршрут.
2. `docs/architecture/overview.md` — границы модулей и архитектурные решения.
3. Документ по области задачи из `docs/`.
4. `docs/specs/implementation-plan.md` — полное целевое ТЗ; не считай все его
   пункты уже реализованными.

Код и тесты — источник истины о текущем поведении. `docs/archive/initial-product-vision.md`
исторический и местами устарел: TUI должен использовать `ntcharts`, не
`asciigraph`; multi-exchange ещё не реализован.

## Текущий приоритет

Главный milestone — первый подтверждённый end-to-end buy/sell round trip через
T-Invest sandbox, затем запуск agent и crash/restart recovery. Не подменяй его
новой архитектурной задачей, если она не является блокером. Очерёдность записана
в `docs/roadmap/current.md`.

## Неподвижные решения

- Go 1.24, modular monolith, один бинарник/Cobra CLI.
- Деньги и quantities — decimal; `float64` для денег запрещён.
- Любая сумма содержит asset; разные asset не агрегируются неявно.
- Daily loss — только per strategy, без глобального абсолютного лимита.
- Сейчас SQLite (`modernc.org/sqlite`) и один agent process. PostgreSQL позже,
  без требования нескольких одновременно работающих ботов.
- Exchange SDK не проникает в domain/application contracts.
- T-Invest SDK: `opensource.tbank.ru/invest/invest-go`.
- Только T-Invest sandbox; production/live endpoint запрещён.
- Все стратегии текущего agent используют один exchange. Каждой стратегии нужен
  отдельный instrument; future multi-exchange зафиксирован в
  `docs/roadmap/multi-exchange.md`, но не реализован.
- TUI read-only; charts — `ntcharts` за adapter, но сам TUI ещё не реализован.

## Надёжность

- Persist order intent до API call.
- Фазы: `ready -> submitting -> submitted|rejected|unknown`.
- Только `ready` можно отправлять. `submitting/unknown` разрешаются lookup, а не
  повторным PlaceOrder.
- Read-only transient calls можно bounded-retry. Mutation calls
  (`PostOrder`, `CancelOrder`, account create/pay-in) автоматически не ретраить.
- Unknown/malformed mutation outcome означает fail closed + reconciliation.
- Incoming executions сначала сохраняются в durable inbox; projections и
  `applied` обновляются атомарно. Дубликаты должны быть безопасны.
- History checkpoint продвигается только после полного успешного scan/apply.
- Startup reconciliation завершается до `running` и новых сигналов.

Подробности: `docs/architecture/trading-runtime.md` и `docs/operations/tinvest-sandbox.md`.

## Стратегии

- Используй общий `Strategy`/`Worker`/risk/intent pipeline для live и backtest.
- Built-ins создаются через `internal/strategy/builtin`.
- Реализованы `moving_average_cross` и single-instrument monthly
  `periodic_investment` v1.
- DCA v1 candle-driven, fixed quantity, day 1–28, один occurrence `YYYY-MM`.
  Не выдавай её за basket/budget scheduler; ограничения перечислены в
  `docs/strategies/overview.md`.

## Работа с репозиторием

- Сначала проверяй существующий код и тесты; используй `rg`.
- `docs/` — накапливаемая база знаний проекта, а не одноразовый отчёт. Любое
  принятое решение, которое влияет на архитектуру, публичные контракты,
  безопасность торговли, хранение данных, recovery, конфигурацию, ограничения
  или roadmap, фиксируй в соответствующем документе `docs/` в том же изменении.
- Не оставляй важное решение только в чате, описании задачи, commit message или
  локальном TODO. Для существенно отвергнутого подхода запиши причину отказа и
  условия, при которых решение можно пересмотреть.
- Сначала обновляй существующий тематический документ. Новый `.md` создавай,
  только если появилась самостоятельная область знаний, и добавляй его в
  `docs/README.md`.
- При изменении решения найди старые упоминания через `rg`, обнови их и явно
  раздели фактически реализованное, запланированное и deprecated.
- Сохраняй чужие изменения и не выполняй destructive git operations.
- Не редактируй старую SQLite migration; добавляй следующую.
- Не правь sqlc generated files вручную без необходимости.
- Не раскрывай credentials в stdout/stderr, logs, fixtures или errors.
- Для runtime logging используй внедрённый `log/slog`, структурированные поля и
  стабильный `event`; не добавляй разрозненные `fmt.Printf` или глобальный logger.
- `.env` загружается программой. `source ~/.zshrc.private` нужен только для
  ручной проверки sandbox env в текущей shell, не перед тестами.
- НИКОГДА не задавай и не используй `GOCACHE`. При sandbox-проблеме запроси
  разрешение на обычную Go-команду.

## Проверка изменений

Минимум:

```sh
make fmt
make test PKG=<изменённые пакеты>
make test
```

Эквивалентные прямые Go-команды допустимы. Makefile не задаёт `GOCACHE` и не
содержит sandbox mutation targets.

Для runtime, storage, streams и concurrency также запускай релевантный
`go test -race ...`. Реальные sandbox mutations не являются частью обычных
тестов и требуют явного подтверждённого сценария.

В handoff всегда укажи: текущий этап/milestone, что изменено, проверки,
ограничения, следующий пункт зафиксированного roadmap и отдельно — отложенные
находки. Также укажи, какой файл `docs/` обновлён для нового решения, либо явно
скажи, что изменение не создало нового проектного знания. Не превращай
отложенную находку в новый «следующий шаг» без причины.
