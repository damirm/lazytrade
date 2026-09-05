# ADR-0001: базовые domain и strategy contracts

- Статус: принято
- Дата: 2026-07-29
- Связанные этапы: 2, 7, 8, 8A

## Контекст

Live agent, sandbox, backtest, storage и UI должны использовать одинаковые
доменные значения. До параллельной реализации необходимо закрепить точность
денег, владение состоянием стратегии и детерминированность backtest.

## Решение

### Decimal

Использовать `github.com/shopspring/decimal` версии `v1.4.0`.

Доменные `Money`, `Price` и `Quantity` инкапсулируют decimal. Публичный API
lazytrade не принимает `float32/float64` для денег, цены и количества.

### Идентификаторы

Использовать отдельные строковые Go-типы для каждого вида ID. Генератор
конкретного формата будет передаваться как зависимость. Выбор UUIDv7/ULID
откладывается до реализации persistence, чтобы не связывать модели с
генератором.

### Strategy

Strategy является детерминированным преобразованием:

```text
previous versioned state + one MarketEvent
→ new versioned state + SignalDraft[]
```

Стратегия не получает Broker, Storage, Exchange, wall Clock, HTTP client и
генератор случайных чисел.

### Signal ID

Стратегия возвращает `SignalDraft`. Стабильный Signal ID назначает worker на
основе strategy instance, event cursor, ordinal и нормализованного payload.
Алгоритм имеет собственную версию.

### Источник событий backtest

Внутренний backtest runner использует синхронный `EventIterator`. Channel-based
источник допускается на границе live market data, но не определяет порядок
backtest.

### Состояние стратегии

Состояние хранится в JSON envelope с явной версией схемы. Неизвестная будущая
версия и повреждённый payload приводят к safe/blocked state, а не к молчаливому
сбросу.

### Время

Risk, P&L и runtime получают `Clock` как зависимость. Strategy использует
логическое время входного события и не вызывает wall clock.

## Последствия

- Live и backtest используют один strategy type.
- Результат backtest не зависит от channel scheduling.
- Decimal dependency появляется в domain layer.
- JSON state проще диагностировать и мигрировать, но требует явных validation
  и version upgrade.
- Формат генерируемых entity IDs остаётся отдельным решением storage-этапа.

## Отложенные решения

- UUIDv7 или ULID для новых entity IDs.
- Атомарная граница сохранения state, signals и risk decisions.
- Limit gap improvement.
- Политика incomplete total P&L.
- Полная priority table событий с одинаковым timestamp.

