# Роль: Foundation & Storage

## Миссия

Создать устойчивый фундамент приложения: CLI, конфигурацию, доменные примитивы,
контракты хранения и SQLite-реализацию, не связывая бизнес-логику с конкретной
СУБД.

## Основные этапы

- Этап 0: bootstrap и ADR.
- Этап 1: CLI и configuration.
- Этап 2: domain primitives и storage contracts.
- Этап 3: SQLite и sqlc.
- В будущем — storage-часть этапа 13.

## Требования

Основные группы:

- FR-CLI;
- FR-CONFIG;
- FR-STORAGE;
- доменные требования раздела 7;
- NFR-006, NFR-007, NFR-008 и NFR-009.

## Преимущественное владение

```text
cmd/lazytrade/
internal/cli/
internal/config/
internal/domain/
internal/storage/
db/
sqlc*.yaml
configs/
```

`internal/domain` является общей зоной. Существенные изменения согласовываются
с интегратором и затронутыми ролями.

## Обязанности

- создать Go module и базовую структуру;
- реализовать strict YAML parsing;
- реализовать command-scoped validation;
- не требовать live credentials для backtest;
- реализовать `Money`, ID, Clock contracts и validation;
- выбрать decimal library и SQLite driver через ADR;
- описать минимальные repository interfaces;
- создать migrations и sqlc queries;
- скрыть sqlc types внутри SQLite adapter;
- реализовать транзакции;
- реализовать single-agent lock;
- создать storage contract suite;
- подготовить расширение PostgreSQL без написания преждевременного адаптера.

## Обязательные границы

- Domain не импортирует SQL, SQLite, pgx, exchange SDK, Bubble Tea или HTTP.
- В repository API не передаются `*sql.Tx` и sqlc-типы.
- SQL SQLite и будущего PostgreSQL не обязан быть одинаковым.
- ID создаются приложением.
- Money разных asset нельзя складывать.
- Секреты не должны храниться в разобранной конфигурации дольше необходимого и
  никогда не выводятся полностью.

## Взаимодействие

От Exchange agent нужны:

- требования к instrument metadata;
- нормализованные price/quantity types.

От Strategy agent нужны:

- формат versioned strategy state;
- требования P&L и backtest metadata.

От Execution agent нужны:

- атомарные границы OrderIntent/Order/Execution;
- уникальные ограничения идемпотентности.

Все спорные изменения repository contracts передаются интегратору.

## Обязательные тесты

- strict YAML и unknown fields;
- command-scoped validation;
- redaction secrets;
- Money operations и asset mismatch;
- migration на пустой БД;
- повторное применение migrations;
- transaction commit/rollback;
- uniqueness signal/client order/execution IDs;
- одновременная попытка получить single-agent lock;
- полный storage contract suite на временной SQLite.

## Не входит в роль

- реализация T-Invest;
- торговые стратегии;
- simulated broker;
- web handlers;
- TUI;
- бизнес-решение о fill/reconciliation.

## Handoff

Дополнительно к общему handoff указать:

- version schema;
- выбранные библиотеки и ADR;
- применённые SQLite pragmas;
- список repository contracts;
- миграции и способ их запуска;
- ограничения будущего PostgreSQL adapter.

