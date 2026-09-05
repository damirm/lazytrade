# Надёжность торгового runtime

## Главный инвариант

Нельзя создать повторную заявку только потому, что процесс или сеть не успели
подтвердить результат предыдущего API-вызова. Неизвестный исход — отдельное
состояние, которое разрешается чтением биржи и reconciliation, а не слепым
повтором mutation.

## Order intent lifecycle

```text
signal + allowed risk decision
  -> ready       API точно ещё не вызван
  -> submitting  durable transition записан перед PlaceOrder
  -> submitted   order подтверждён и сохранён
  -> rejected    биржа доказала, что заявка не применена
  -> unknown     заявка могла быть применена
```

Legacy `pending` был мигрирован в `unknown`, потому что из старого состояния
невозможно доказать, пересекал ли вызов границу API.

- Только `ready` разрешено отправлять.
- `submitting` и `unknown` при старте разрешаются через
  `GetOrderByClientID`.
- `NotFound` после неизвестного исхода не доказывает отсутствия заявки из-за
  eventual consistency; runtime блокируется вместо повторной отправки.
- Client order ID детерминирован и используется как idempotency key.

## Mutations и read retries

Автоматический bounded retry применяется только к read-only unary RPC:
accounts, instruments, portfolio, order state/list, market data и history.
По умолчанию — до трёх попыток с exponential backoff и jitter для transient
ошибок и rate limit. `NotFound` и permanent ошибки не повторяются.

`OpenSandboxAccount`, `SandboxPayIn`, `PostOrder` и `CancelOrder` никогда не
ретраятся внутри адаптера. `Unknown`, `Canceled`, `DeadlineExceeded`,
`AlreadyExists`, `Aborted`, `Internal`, `Unavailable`, `DataLoss`, неизвестный
status и malformed success response трактуются как `UnknownOutcome` и
`Retryable=false`.

## Execution durability

Execution stream не обновляет projections напрямую:

```text
exchange execution
  -> StageExecution(execution_inbox, pending)
  -> ApplyStagedExecution transaction
       order fill projection
       position
       P&L/statistics
       inbox status=applied
```

Дедупликация использует exchange account, source family и dedupe key.
Повторная доставка fill безопасна. При старте все pending inbox entries
применяются повторно.

Комиссия из cumulative order snapshot применяется только как положительная
дельта над уже сохранённым cumulative amount. Это предотвращает двойной учёт
при повторных snapshots.

## Startup sequence

Порядок важен и не должен упрощаться без доказательства безопасности:

1. Persist strategy lifecycle `reconciling`.
2. Разрешить persisted `ready/submitting/unknown` intents.
3. Открыть execution stream и durable pump.
4. Просканировать bounded history до фиксированной границы видимости.
5. Stage/apply recovered fills и commission deltas.
6. Drain pending execution inbox.
7. Сверить local positions и open orders с exchange.
8. Продвинуть history checkpoint только после полного успешного scan/apply.
9. Отправить только ранее найденные `ready` intents.
10. Выполнить post-subscription reconciliation.
11. Подписаться на market data и перевести стратегии в `running`.

Любая критическая неоднозначность переводит runtime в blocked state. Один
strategy worker failure должен останавливать только эту стратегию; общий
execution stream продолжает принимать исполнения ранее размещённых заявок.

## Reconciliation

Текущий reconciler сравнивает:

- суммарные local/remote quantities по instrument;
- presence и состояние открытых заявок по client order ID.

Он обнаруживает расхождения и блокирует startup, но не исправляет произвольно
exchange state. History recovery восстанавливает доступные fills; она пока не
обновляет локальный cancelled/rejected terminal status без fill.

## Cancellation

Основной runtime пока не инициирует отмены. Единственный рабочий caller —
sandbox smoke cleanup. Он вызывает `CancelOrder` один раз, а при unknown outcome
только наблюдает `GetOrder` до terminal state или bounded timeout.

Будущая отмена в runtime требует отдельной durable command state machine:

```text
ready -> cancelling -> confirmed | unknown | not_applied | superseded
```

Не кодировать uncertain cancellation как `OrderStatusUnknown`: состояние
ордера и состояние команды отмены — разные факты. Также нужно защитить
projection от регрессии `cancelled -> partially_filled` при поздней доставке
fill, исполненного до отмены.

