# Разработка и эксплуатация

## Базовые команды

```sh
make build
make test
make test-race
make sqlc-generate
make sqlc-check
make version
make config-validate
```

`Makefile` является короткой точкой входа в стандартные локальные проверки и
не скрывает специальных environment settings. Доступные цели показывает
`make help`.

Переменные можно переопределять при вызове:

```sh
make test PKG=./internal/agent
make config-validate CONFIG=configs/backtest.example.yaml
make build BINARY=/tmp/lazytrade
```

`make check` выполняет `go vet ./...` и `go test ./...`. `make fmt` изменяет
Go-файлы через `gofmt`. `make sqlc-generate` строит SQLite generated code
непосредственно по forward-миграциям, а `make sqlc-check` после генерации
завершается ошибкой при незакоммиченном diff generated files. Остальные
проверочные цели исходники не изменяют.
Makefile намеренно не содержит целей, размещающих sandbox orders, создающих или
пополняющих счета, а также включающих production trading.

Никогда не задавать и не использовать переменную `GOCACHE` в командах проекта.
Если sandbox не разрешает стандартный Go build cache, запросить разрешение на
обычный `go test`, а не перенаправлять cache.

Перед длительным прогоном сначала полезно запускать изменённый пакет, затем
`go test ./...`, затем релевантный `go test -race ...`.

## Работа с файлами и generated code

- Для поиска использовать `rg`/`rg --files`.
- Ручные изменения делать через patch; не перезаписывать пользовательские
  изменения и не выполнять destructive git commands.
- Единственный источник SQLite schema находится в `db/sqlite/migrations`, а
  SQL-запросы — в `db/sqlite/queries`; отдельный schema snapshot не ведётся.
- `internal/storage/sqlite/generated` — sqlc output; менять source query/schema,
  затем регенерировать, если изменение требует sqlc.
- Применённые migration files не редактировать; создавать новую migration.

## Накопление проектных знаний

Каталог `docs/` является долговременной базой знаний проекта. Его нужно
пополнять по мере разработки, чтобы следующий разработчик или агент мог
восстановить контекст без истории чата.

Документацию необходимо обновлять в том же изменении, если принято решение,
которое затрагивает хотя бы одну из областей:

- архитектурные границы или направление зависимостей;
- domain/storage/exchange API и состояния сущностей;
- торговую безопасность, idempotency, retry, recovery или reconciliation;
- schema/config/CLI и пользовательское поведение;
- поддержку бирж, стратегий, backtest или режимов запуска;
- текущие ограничения, известные риски или последовательность roadmap;
- результат реальной sandbox-проверки, меняющий наши предположения.

Запись решения должна отвечать минимум на четыре вопроса:

1. Что принято или обнаружено?
2. Почему выбран именно этот вариант?
3. Какие инварианты и ограничения нельзя нарушать?
4. Что остаётся отложенным и когда решение можно пересмотреть?

Правила ведения базы знаний:

- обновлять существующий тематический файл вместо создания дубликата;
- новый файл добавлять в индекс `docs/README.md`;
- отличать фактически реализованное от запланированного;
- при замене решения актуализировать старые упоминания и сохранять краткую
  причину изменения, если она важна;
- перед завершением задачи искать противоречащие упоминания через `rg`;
- не помещать в документацию секреты, реальные token values или чувствительные
  payloads;
- не копировать всё ТЗ: сохранять выводы, мотивировку, инварианты и полезный
  operational context.

Если изменение не создаёт нового знания и не меняет контракт — например,
локальный рефакторинг без изменения поведения — искусственное обновление
документации не требуется.

## Проверки по областям

- Strategy: package tests, worker/durable tests, backtest parity.
- Storage: чистая временная БД, migration idempotency, transaction rollback,
  duplicate execution и restart recovery.
- Runtime: intent phases, execution inbox/history, worker isolation,
  reconciliation и race detector.
- T-Invest: mapper/error/retry tests без credentials; real sandbox проверки
  только явно и с минимальными mutations.
- Config: strict unknown-field rejection, точный field path и отсутствие
  secret values в errors.

## Секреты

- `.env` читается при старте CLI и не должен попадать в git/output.
- Не печатать значения `TINVEST_SANDBOX_TOKEN` и `TINVEST_ACCOUNT_ID`.
- Не использовать shell trace (`set -x`) рядом с credentials.
- `source ~/.zshrc.private` нужен только если в текущей интерактивной shell
  session требуется проверить переменную, а не перед обычными тестами.

## Логи торгового agent

Agent использует `log/slog` и читает `logging.level`, `logging.format` и
`logging.output` из YAML. Допустимы уровни `debug`, `info`, `warn`, `error`,
форматы `json`, `text`, а output — `stderr`, `stdout` или путь к файлу. Путь
считается от каталога конфигурации.

В штатном `info` видны запуск, recovery, подписки, переход в running, сигналы,
risk decisions и заявки. Логи не заменяют durable audit/storage и не должны
содержать token, account ID или DSN.

## Безопасность торговых действий

- Tests и read-only diagnostics не дают разрешения на размещение ордеров.
- Реальный smoke-test выполняется только sandbox-конфигурацией, с `--confirm`,
  минимальным количеством и bounded timeout.
- Production endpoint отключён. Не включать его как побочный эффект другой
  задачи.
- Mutation RPC нельзя автоматически повторять после timeout/transport error.
- Если exchange state неоднозначен, fail closed и оставь понятный blocked
  reason; не угадывай позицию/исполнение по инструменту и времени.

## Definition of Done

Изменение завершено, когда:

- поведение соответствует текущему этапу и не расширяет scope молча;
- domain не зависит от infrastructure SDK;
- есть позитивные и негативные тесты;
- formatter, focused tests и полный набор тестов проходят;
- для concurrency-sensitive кода выполнен race detector;
- config/example/docs обновлены, если менялся публичный контракт;
- важные принятые решения и новые знания внесены в тематический файл `docs/`;
- известные ограничения и следующий пункт основного roadmap названы явно.
