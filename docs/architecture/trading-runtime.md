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

## Stream supervision: sandbox MVP

Market и execution streams одноразовые: `Subscribe…` открывает один RPC;
market subscription requests отправляются до возврата из `SubscribeMarketData`.
Ошибки подготовки/open/send возвращаются вызывающему коду напрямую. После
успешного открытия consumer получает только data channel и `Errors`.

- Transport/mapping error и неожиданный EOF публикуют одну terminal error,
  после чего оба канала закрываются. Автоматического reconnect/backoff нет.
- Runtime трактует ошибку или неожиданное закрытие любого канала как fail
  closed: account runtime завершается, активные стратегии получают `blocked`.
- Отмена родительского context — штатное завершение без terminal error;
  дочерний RPC context отменяется при любом выходе receive loop, в том числе
  при ошибке mapper и блокировке consumer.
- `StreamState`, `StreamEvent`, поколения соединения и восстановление списка
  подписок удалены. Новый запуск проходит обычный startup recovery до торговли.
- Read-only unary retries (включая metadata и `GetOrderState`) не изменены.

Открытие потока не означает получение broker ACK или первой котировки. Ранее
`StreamHealthy` также отправлялся до первого `Recv`. `agent preflight` проверяет
открытие потоков и уже доступные ошибки, но не ждёт market event, поэтому может
работать при закрытом рынке. Он не доказывает полноценную доставку данных;
полная проверка market subscription ACK остаётся отдельным непокрытым пунктом
адаптера, не скрытым за названием `healthy`.

Reconnect отложен: его нельзя вернуть только внутри адаптера. Для этого нужны
состояние `degraded`, запрет новых сигналов во время разрыва, согласованный
recovery/resubscribe протокол и soak tests. Решение R7 убирает недостижимый путь,
а не ослабляет реакцию runtime на потерю связи.

## Execution durability

Execution stream не обновляет projections напрямую:

```text
exchange.Execution (без strategy ID)
  -> FindExecutionOwner(account, order ID, optional client order ID)
  -> domain.Execution (с владельцем из БД)
  -> StageExecution(execution_inbox, pending)
  -> ApplyStagedExecution transaction
       order fill projection
       position
       P&L/statistics
       inbox status=applied
```

Владение исполнением определяется в application/storage, не в T-Invest
adapter. Адаптер отдаёт биржевой order ID, client order ID (если доступен),
instrument и side. Runtime ищет intent/order только внутри текущего логического
exchange account и проверяет совпадение instrument/side. Два переданных ID
обязаны указывать на один intent; совпадения только одного недостаточно, если
другой противоречит сохранённому order. `OrderContextRegistrar` и in-memory
карты атрибуции удалены.

После рестарта сохранённого exchange order ID достаточно для атрибуции, даже
до reconciliation. Во время `PlaceOrder` fill можно привязать по client ID из
уже сохранённого intent и записать в inbox до сохранения ответа на заявку.
Применение проекций всё ещё требует локальный order: до его recovery запись
остаётся pending. Стратегия с ошибкой также остаётся владельцем своих поздних
fills; список работающих workers не является источником владения.

Неизвестные или противоречивые идентификаторы приводят к fail closed, а не к
угадыванию стратегии по instrument или игнорированию исполнения. Такой fill не
проходит staging: дальнейшая диагностика/recovery обязательна. Отсутствие client
ID допустимо только если exchange order уже известен локально. T-Invest по-прежнему
читает `GetOrderState` для client ID и комиссии; недоступность/некорректность
этого read также останавливает ingress. Это не offline-гарантия обработки fill.

Предварительная регистрация всех contexts до открытия stream отвергнута в R6:
она сохранила бы второй источник владения в памяти и зависимость от порядка
startup. Durable lookup заменил этот механизм напрямую, без переходного пути.

Дедупликация использует exchange account, source family и dedupe key.
Повторная доставка fill безопасна. При старте все pending inbox entries
применяются повторно.

Комиссия из cumulative order snapshot применяется только как положительная
дельта над уже сохранённым cumulative amount. Это предотвращает двойной учёт
при повторных snapshots.

## Startup sequence

До startup CLI собирает один `StrategyBinding` на каждую strategy instance.
`NewRuntime` отклоняет пустые и дублирующиеся strategy/instrument IDs, nil
worker/risk gate, невалидную subscription и несовпадение instrument между
binding и subscription. Routing maps выводятся из этого списка и не являются
отдельной конфигурацией.

Lifecycle хранится в отдельной строке `strategy_lifecycle`, создаваемой вместе
со strategy instance. Он не зависит от наличия event snapshot в
`strategy_states`, поэтому `reconciling`, `running` и terminal status должны
сохраняться даже до первого market event. `ErrNotFound` при lifecycle update
считается нарушением storage-инварианта и не подавляется.

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

В текущем коде `Runtime.Run` после проверки конфигурации, repair failed
signals и записи `reconciling` вызывает приватную фазу `startup`. Она возвращает
`startupResult` с account ID, активными workers/risk gates, market stream,
каналами execution pump, mutex для storage и состоянием отложенного signal
recovery. Результат доступен только после завершения перечисленных шагов,
записи всех `running` lifecycle и, если канал настроен, отправки `Ready`;
при ошибке возвращается нулевой результат без `Ready`. Затем `runMarketLoop`
потребляет этот результат.
Порядок intent recovery, stream/history ingress, обеих reconciliation,
checkpoint, отправки только `ready` intents и перехода lifecycle не изменён.

Любая критическая неоднозначность переводит runtime в blocked state. Один
strategy worker failure должен останавливать только эту стратегию; общий
execution stream продолжает принимать исполнения ранее размещённых заявок.

Открытые находки по безопасности текущей реализации, обнаруженные при R9
(поведение существовало до разделения файлов): execution pump использует
родительский context и может продолжить работу после ошибки `startup`, пока
вызывающий не отменит context; накопленная в канале ошибка pump не проверяется
перед отправкой ранее сохранённых `ready` intents; terminal stream error может
соревноваться с уже готовым market event в `select` основного цикла. R9 не
меняет эти сценарии; для них нужны отдельные воспроизводящие тесты и решение,
не подменяющее незакрытый sandbox round trip.

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
