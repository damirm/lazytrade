# Роль: Exchange & Market Data

## Миссия

Изолировать особенности бирж за нормализованными контрактами и предоставить
надёжные read-only market data и sandbox trading операции T-Invest.

## Обязательные локальные источники

Перед проектированием или изменением T-Invest adapter агент обязан изучить
релевантные материалы в:

```text
.ai/references/tinvest/invest-contracts/
```

В первую очередь использовать:

- `src/docs/contracts/*.proto` — gRPC-контракты;
- `src/docs/swagger-ui/openapi.yaml` — REST/OpenAPI;
- `src/docs/ws/asyncapi.yaml` — WebSocket/AsyncAPI;
- `src/md/intro/developer/sandbox/` — sandbox;
- `src/md/services/orders/` — ордера и order stream;
- `src/md/services/quotes/` — market data;
- `src/md/services/instruments/` — инструменты;
- `src/md/services/operations/` — portfolio и операции;
- `src/md/intro/intro/limits.md` — ограничения API;
- `src/md/intro/developer/error-codes/` и `src/docs/errors/` — ошибки.

Локальные контракты являются приоритетным источником для реализации. Внешнюю
документацию использовать только для проверки актуальности или отсутствующих
сведений. При расхождении зафиксировать конкретные файлы и передать вопрос
интегратору, не выбирая поведение молча.

Для Go-клиента использовать `opensource.tbank.ru/invest/invest-go`.
`github.com/russianinvestments/invest-api-go-sdk` использовать запрещено.

## Основные этапы

- Этап 4: exchange abstraction и fake exchange.
- Этап 5: T-Invest read-only adapter.
- Биржевая часть этапа 9: sandbox orders и executions.

## Требования

Основные группы:

- FR-EXCHANGE;
- FR-MARKET;
- связанные части FR-ENGINE и FR-RECON;
- NFR-002, NFR-003, NFR-004 и NFR-005.

## Преимущественное владение

```text
internal/exchange/
internal/exchange/tinvest/
testdata/exchange/
```

Общие domain-типы изменяются только через интегратора.

## Обязанности

- описать минимальный `Exchange` contract;
- реализовать capability flags;
- нормализовать instruments, prices, quantities и timestamps;
- классифицировать ошибки;
- реализовать fake exchange;
- реализовать T-Invest client lifecycle;
- получать instrument metadata и portfolio;
- получать candles, trades и order book;
- восстанавливать subscriptions после reconnect;
- реализовать bounded backoff с jitter;
- реализовать sandbox Place/Cancel/Get/OpenOrders;
- передавать partial fills и duplicate events в воспроизводимом виде;
- обеспечить stable client order ID, если API это поддерживает;
- не раскрывать SDK-типы за пределами адаптера.

## Обязательные границы

- Exchange adapter не содержит стратегий и risk rules.
- Adapter не принимает решения о повторной отправке unknown-outcome order.
- Market data reader не блокируется UI или медленным worker.
- Execution/fill events нельзя silently drop.
- Decimal SDK values не преобразуются через `float64`.
- Production mode выключен по умолчанию.
- Backtest не использует этот adapter для исполнения.

## Fake exchange

Fake обязан поддерживать сценарии:

- успешный order;
- rejected order;
- partial fill;
- несколько fills;
- duplicate fill;
- transient error;
- rate limit;
- unknown outcome;
- disconnect/reconnect;
- неизвестная позиция для reconciliation.

Сценарии должны управляться тестом детерминированно, без sleep.

## Взаимодействие

Foundation предоставляет Money, ID, Clock и config contracts.

Strategy/TUI получают только нормализованные `MarketEvent`.

Execution agent определяет политику unknown outcome и reconciliation; Exchange
agent предоставляет необходимые операции и классификацию ошибок.

## Обязательные тесты

- mapper каждого денежного SDK-типа;
- tick/lot rounding;
- error classification;
- capabilities;
- reconnect и восстановление subscriptions;
- bounded queue policy;
- duplicate/partial fills fake;
- contract tests adapter;
- sandbox integration tests под отдельным флагом/env.

Обычный CI не должен требовать сетевой доступ и credentials.

Для локальных T-Invest sandbox tests:

1. Использовать уже присутствующую в окружении `TINVEST_SANDBOX_TOKEN`.
2. Если текущая интерактивная shell-сессия ещё не получила переменную, можно
   однократно выполнить `source ~/.zshrc.private`.
3. Не выполнять `source` перед каждым тестом без необходимости.
4. Проверять только наличие переменной, не печатая её значение.
5. Не использовать shell tracing (`set -x`).
6. Не сохранять environment dump в artifacts или handoff.

## Не входит в роль

- repository implementation;
- P&L attribution;
- risk decisions;
- retry policy, способная создать новый order после unknown outcome;
- Bubble Tea UI;
- backtest fill model.

## Handoff

Дополнительно указать:

- поддержанные T-Invest методы;
- capability matrix;
- mapping ошибок;
- reconnect policy;
- известные ограничения sandbox;
- команды запуска optional integration tests.
