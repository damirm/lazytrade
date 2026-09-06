# ADR-0005: отдельное долговечное состояние lifecycle стратегии

- Статус: принято
- Дата: 2026-09-06
- Связанный этап: refactoring R5

## Контекст

Изначально `runtime_status` и `status_reason` хранились в `strategy_states`.
Строка этой таблицы появляется только после первого успешно обработанного
market event. Поэтому новый runtime не мог долговечно записать `reconciling`,
`running`, `blocked` или `stopped` до первого события; `ErrNotFound` при таком
обновлении подавлялся application-слоем.

Event snapshot и lifecycle имеют разный момент создания и разную частоту
обновления. Их совместное хранение делало наличие event-state неявным условием
наблюдаемости процесса.

## Решение

- Ввести таблицу `strategy_lifecycle`, содержащую `strategy_id`, status, reason
  и timestamp последнего обновления.
- Создавать начальную строку `stopped` атомарно вместе со
  `strategy_instances`.
- Хранить lifecycle независимо от `strategy_states`; отсутствие event-state у
  новой стратегии остаётся нормальным состоянием.
- `SetStrategyStatus` и startup recovery используют только
  `strategy_lifecycle`.
- Миграция 6 переносит существующий status из `strategy_states`, а для instance
  без snapshot создаёт `stopped`.
- Ошибка отсутствующей lifecycle-строки больше не подавляется runtime.

## Legacy-колонки

SQLite migration 1 содержит NOT NULL колонки `runtime_status` и
`status_reason` в `strategy_states`. Они остаются физически существовать, чтобы
не выполнять destructive table rebuild. Новый код не читает их как lifecycle и
не записывает туда реальные переходы; при создании event-state используется
техническое значение `state`.

Эти колонки можно физически удалить позднее отдельной миграцией только после
проверки SQLite rebuild, foreign keys, индексов и rollback/recovery сценариев.

## Последствия

- Lifecycle виден до первого market event и после crash во время startup.
- State revision/cursor не изменяются при lifecycle transition.
- Регистрация instance и начального lifecycle атомарна.
- Появляется отдельный read contract `LoadStrategyLifecycle`; event-state
  `LoadRuntime` больше не смешивает две сущности.
