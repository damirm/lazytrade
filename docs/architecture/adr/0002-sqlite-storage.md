# ADR-0002: SQLite driver, migrations, sqlc и single-agent lock

- Статус: принято
- Дата: 2026-07-29
- Связанный этап: 3

## Решение

- Использовать pure-Go driver `modernc.org/sqlite` версии `v1.37.0`.
- Использовать `sqlc` версии `v1.29.0` с engine `sqlite`; generated-код
  находится только в `internal/storage/sqlite/generated`.
- Forward-only migrations хранятся в `db/sqlite/migrations` и встраиваются
  через `embed.FS`. Внешняя migration library не используется.
- На каждом connection DSN включает `foreign_keys=ON` и
  `busy_timeout=5000`; при open устанавливается и проверяется WAL.
- Pool первоначально планировался на четыре соединения. Текущая реализация
  ограничивает SQLite одним open connection (`SetMaxOpenConns(1)`), чтобы
  сериализовать доступ и исключить наблюдавшиеся `SQLITE_BUSY` в критическом
  execution/recovery pipeline. Write transactions должны оставаться короткими.
- Single-agent guard реализуется advisory `flock` отдельного файла
  `<canonical-db-path>.lock` через `golang.org/x/sys/unix`.

## Обоснование lock

Singleton-row и PID-файл не освобождаются надёжно после crash и допускают stale
state/PID reuse. `flock` удерживается file descriptor и автоматически
освобождается ядром при завершении процесса. Неблокирующий acquire обеспечивает
fail-fast семантику. Канонизация директории устраняет различия relative path и
symlink к директории.

MVP поддерживает macOS/Linux. Для Windows потребуется отдельная реализация
того же `storage.AgentLease`; это не меняет repository contracts.

## Последствия

- CGO не требуется.
- PostgreSQL получит собственные migrations/queries и session advisory lock.
- In-memory SQLite не предоставляет production agent lock; contract tests
  lock используют реальный временный файл.
- Forward-миграции являются runtime source of truth.

## Дополнение 2026-09-05

Ограничение пула до одного соединения является актуальным принятым поведением.
Возврат к нескольким connections допустим только после тестов конкурентного
execution inbox/recovery и доказательства отсутствия `SQLITE_BUSY`; исходное
число четыре больше не является целевым значением.

## Дополнение 2026-09-06

Отдельный `db/sqlite/schema.sql` удалён после обнаружения расхождений с
миграциями. `sqlc.sqlite.yaml` читает каталог `db/sqlite/migrations` напрямую,
поэтому runtime migration runner и генератор запросов используют одно описание
схемы. Новая forward-миграция автоматически входит в следующий запуск
`sqlc generate`; generated code проверяется через `make sqlc-check`.
