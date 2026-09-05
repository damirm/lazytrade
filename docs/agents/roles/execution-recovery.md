# Роль: Execution & Recovery

## Миссия

Обеспечить безопасное и идемпотентное превращение разрешённого Signal в
биржевой Order, корректный учёт fills и восстановление после сбоев.

## Основные этапы

- Этап 9: order execution.
- Этап 10: reconciliation.

## Требования

Основные группы:

- FR-ENGINE;
- FR-RECON;
- связанные части FR-RISK, FR-STORAGE и FR-EXCHANGE;
- NFR-001 и NFR-002.

## Преимущественное владение

```text
internal/engine/
```

Изменения order repositories, migrations и exchange contracts согласовываются
с соответствующими агентами и интегратором.

## Обязанности

- реализовать Signal → RiskDecision → OrderIntent pipeline;
- сохранять OrderIntent до сетевого вызова;
- обеспечить stable client order ID;
- дедуплицировать Signal;
- обрабатывать accepted/rejected/partial/filled/cancelled orders;
- дедуплицировать executions;
- обновлять positions и P&L атомарно;
- не повторять order при unknown outcome;
- отменять opening orders при risk pause согласно политике;
- реализовать startup reconciliation;
- сверять open orders и positions;
- восстанавливать доступные fills;
- переводить неразрешимые случаи в blocked;
- реализовать manual reconcile;
- реализовать graceful stop order pipeline.

## Обязательные границы

- Risk manager нельзя обойти.
- Сетевой order невозможен без сохранённого intent.
- Unknown outcome сначала разрешается запросом/reconciliation.
- Повтор одного signal или fill не меняет результат.
- Reconciliation не создаёт новые позиции.
- Неизвестная позиция не приписывается стратегии эвристически.
- Shutdown не закрывает позиции автоматически.
- Production trading не включается этой ролью.

## Crash points

Обязательна проверка остановки:

1. до сохранения intent;
2. после intent, до API call;
3. во время API call с unknown outcome;
4. после ответа, до сохранения order;
5. после fill, до обновления position/P&L.

После restart состояние должно сходиться без дублирующего order/fill.

## Взаимодействие

- Foundation: transaction/repository contracts.
- Exchange: API operations и error classification.
- Strategy/Risk: Signal и RiskDecision.
- Web: application status и manual reconciliation commands.

Любое изменение атомарной границы согласовывается с интегратором.

## Обязательные тесты

- signal idempotency;
- intent before network call;
- rejected order;
- partial и multiple fills;
- duplicate fill;
- unknown outcome;
- каждый crash point;
- startup reconciliation;
- repeated reconciliation;
- unknown position → blocked;
- worker isolation;
- graceful cancellation.

Тесты используют fake exchange, а не сеть.

## Не входит в роль

- реализация exchange SDK;
- формула торговой стратегии;
- simulated broker backtest;
- UI;
- PostgreSQL multi-process coordination.

## Handoff

Дополнительно указать:

- state diagrams order/intent;
- transaction boundaries;
- idempotency keys и unique constraints;
- unknown-outcome policy;
- reconciliation algorithm;
- результаты crash tests.

