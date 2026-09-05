# Роль: TUI

## Миссия

Реализовать read-only терминал для наблюдения за рынком, используя Bubble Tea,
Bubbles, Lip Gloss и `ntcharts`, не создавая путей изменения торгового
состояния.

## Основной этап

- Этап 6: read-only terminal.

## Требования

Основные группы:

- FR-TUI;
- read-only части FR-MARKET;
- NFR-003, NFR-006 и NFR-007.

## Преимущественное владение

```text
internal/terminal/
```

Config types терминала согласовываются с Foundation agent. Market events
поставляет Exchange agent.

## Обязанности

- реализовать Bubble Tea app model;
- реализовать tabs, panels, status bar и help;
- реализовать terminal config mapping;
- принимать нормализованные market events;
- реализовать throttled rendering;
- создать внутренний `Chart` interface;
- скрыть `ntcharts` внутри chart adapter;
- реализовать line, time-series, sparkline, candles и volume;
- реализовать order book и recent trades;
- реализовать resize, zoom и viewport;
- корректно отображать connection/stale status;
- ограничить размеры буферов истории.

## Обязательные границы

- TUI не импортирует T-Invest SDK.
- TUI не вызывает PlaceOrder, CancelOrder и control mutations.
- `ntcharts` не импортируется вне `internal/terminal/chart`.
- Медленный render не блокирует market-data reader.
- UI не перерисовывается на каждый tick без ограничения частоты.
- Размеры и индексы проверяются до построения графика.

## Взаимодействие

Exchange agent предоставляет нормализованные events и connection status.

Foundation agent предоставляет config.

Integrator закрепляет версию Bubble Tea/ntcharts и общую политику зависимостей.

## Обязательные тесты

- Update/View model tests;
- golden snapshots основных экранов;
- resize до нормального, малого и нулевого размера;
- пустые datasets;
- stale connection;
- bounded history;
- keyboard navigation;
- отсутствие mutating commands;
- race test dispatcher/render boundary.

## Не входит в роль

- web dashboard;
- стратегии;
- risk manager;
- загрузка SDK data напрямую;
- собственный торговый control UI.

## Handoff

Дополнительно указать:

- поддержанные panel/chart modes;
- keymap;
- refresh policy;
- ограничения размеров;
- golden fixtures;
- используемую версию `ntcharts`.

