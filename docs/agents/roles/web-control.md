# Роль: Web & Control

## Миссия

Предоставить безопасный встроенный web dashboard и единый control service для
наблюдения и управления scopes агента.

## Основной этап

- Этап 11: Web API и dashboard.

## Требования

Основные группы:

- FR-CONTROL;
- FR-WEB;
- FR-STATS presentation;
- FR-LOG audit requirements;
- NFR-004 и NFR-005.

## Преимущественное владение

```text
internal/control/
internal/web/
api/openapi.yaml
```

Control state repositories согласовываются с Foundation agent. Strategy state
transitions — со Strategy/Execution agents.

## Обязанности

- реализовать application query services;
- реализовать scope-aware pause/resume;
- реализовать emergency stop;
- вычислять effective control state;
- требовать explicit resume для `risk_paused`;
- сохранять audit event;
- реализовать health/live и health/ready;
- реализовать REST handlers;
- реализовать SSE hub;
- обеспечить snapshot после reconnect;
- использовать bounded client queues;
- встроить static assets через `go:embed`;
- реализовать bearer auth policy;
- отображать strategy status, P&L, limits, orders и executions;
- показывать asset у каждой суммы;
- не позволять ошибке web client влиять на engine.

## Обязательные границы

- Handlers не вызывают sqlc напрямую.
- UI не изменяет runtime state в обход control service.
- Resume стратегии не обходит pause exchange/instrument/emergency stop.
- Внешний bind без auth запрещён.
- Токен не попадает в HTML, URL или log.
- SSE events не являются authoritative storage.
- После потери events клиент получает актуальный snapshot.

## Взаимодействие

- Foundation: config и repositories.
- Strategy/Risk: state machine и P&L.
- Execution: reconcile/status services.
- Integrator: OpenAPI и security review.

## Обязательные тесты

- effective pause hierarchy;
- invalid state transition;
- explicit risk resume;
- audit fields;
- endpoint authorization;
- loopback defaults;
- external bind rejection without token;
- SSE reconnect/snapshot;
- slow/disconnected client;
- bounded queues;
- secret redaction;
- browser-level critical flow.

## Не входит в роль

- trading engine;
- direct exchange calls;
- TUI;
- backtest execution;
- изменение формулы P&L.

## Handoff

Дополнительно указать:

- OpenAPI version;
- endpoint list;
- auth policy;
- SSE event types и snapshot semantics;
- control precedence;
- browser test scenario.

