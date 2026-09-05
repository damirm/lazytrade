# T-Invest adapter: проект реализации

## 1. Назначение и границы

Документ уточняет реализацию exchange adapter для T-Invest в рамках этапов 4,
5 и биржевой части этапа 9 из `implementation-plan.md`. Он не меняет общие
доменные контракты.

Адаптер обязан:

- скрывать transport/protobuf-типы внутри `internal/exchange/tinvest`;
- поддерживать read-only market data и состояние sandbox-счёта;
- поддерживать sandbox market/limit orders;
- возвращать нормализованные события, точные decimal-значения и
  классифицированные ошибки;
- не принимать risk-решения и не повторять order при неопределённом исходе;
- не использоваться simulated broker в backtest.

## 2. Рекомендуемый протокол и клиент

### 2.1. Основной транспорт

Использовать gRPC поверх TLS:

- sandbox: `sandbox-invest-public-api.tbank.ru:443`;
- production endpoint не включать в MVP-конфигурацию и не выбирать
  автоматически;
- токен передавать metadata `Authorization: Bearer <token>`;
- дополнительные доверенные CA загружать из `exchange.ca_cert_path`; путь может
  указывать на PEM/DER-файл либо каталог, а сертификаты дополняют системный
  trust store;
- добавить непустой `x-app-name`, если это допускает выбранный клиент.

Причины выбора gRPC:

- proto-файлы являются наиболее точным локальным контрактом;
- market data и order state представлены нативными streaming RPC;
- unary и streaming методы используют одинаковые типы;
- gRPC status позволяет классифицировать transport/API ошибки без разбора
  текста.

REST/OpenAPI оставить диагностическим и потенциальным transport adapter на
будущее. WebSocket/AsyncAPI не нужен для MVP при наличии gRPC stream.

### 2.2. SDK

Использовать официальный Go-модуль
`opensource.tbank.ru/invest/invest-go` и тонкий собственный wrapper. Конкретная
версия фиксируется ADR после проверки соответствия локальным контрактам. Не
следует строить доменную модель на высокоуровневых типах SDK.

Модуль `github.com/russianinvestments/invest-api-go-sdk` использовать
запрещено.

Если официальный Go SDK существенно отстаёт от локальных proto-контрактов,
генерировать stubs из локальных `.proto` воспроизводимой командой. В репозитории
должны быть зафиксированы версия `protoc`, плагины и команда генерации.

Источники:

- `.ai/references/tinvest/invest-contracts/src/docs/contracts/*.proto`;
- `.ai/references/tinvest/invest-contracts/src/docs/swagger-ui/openapi.yaml`;
- `.ai/references/tinvest/invest-contracts/src/docs/ws/asyncapi.yaml`;
- `.ai/references/tinvest/invest-contracts/src/md/intro/developer/sandbox/url_difference.md`.

## 3. Сервисы и методы MVP

### 3.1. InstrumentsService

Обязательные:

- `GetInstrumentBy` — разрешение инструмента и загрузка metadata по UID/FIGI;
- `FindInstrument` — конфигурационная/CLI-помощь, не hot path;
- `TradingSchedules` — проверка расписания и отображение сессии.

Для списков инструментов использовать типовые `Shares`, `Bonds`, `Etfs`,
`Currencies`, `Futures`, `Options` только если UI действительно требует
каталог. Не загружать все справочники при старте обычного worker.

Нормализовать:

- `uid` как основной устойчивый T-Invest instrument ID;
- FIGI, ticker, class code как aliases;
- asset/currency;
- lot;
- `min_price_increment`;
- доступность API, market и limit orders;
- торговую площадку и тип инструмента.

Источники:

- `.ai/references/tinvest/invest-contracts/src/docs/contracts/instruments.proto`;
- `.ai/references/tinvest/invest-contracts/src/md/services/instruments/methods.mdx`;
- `.ai/references/tinvest/invest-contracts/src/md/services/instruments/more-instrument.md`.

### 3.2. MarketDataService и MarketDataStreamService

Unary MVP:

- `GetCandles` — начальная история для TUI;
- `GetLastPrices` — initial snapshot/fallback;
- `GetOrderBook` — initial snapshot/fallback;
- `GetTradingStatus` или `GetTradingStatuses`;
- `GetLastTrades` — ограниченная начальная история.

Streaming MVP через один управляемый `MarketDataStream` на connection:

- candles;
- order book;
- public trades;
- last price;
- instrument trading status (`Info`).

Server-side stream можно оставить вне первой реализации: bidirectional stream
нужен для динамических подписок вкладок TUI и workers.

Каждый subscription response нужно проверять поэлементно. Получение gRPC stream
без ошибки не означает успешную подписку. Неизвестный protobuf variant или
subscription status должен стать диагностическим событием/ошибкой, а не
игнорироваться.

Источники:

- `.ai/references/tinvest/invest-contracts/src/docs/contracts/marketdata.proto`;
- `.ai/references/tinvest/invest-contracts/src/md/services/quotes/marketdata.mdx`;
- `.ai/references/tinvest/invest-contracts/src/md/services/quotes/faq_marketdata.md`;
- `.ai/references/tinvest/invest-contracts/src/md/services/quotes/get_history.md`.

### 3.3. SandboxService

Обязательные account/state:

- `GetSandboxAccounts`;
- `GetSandboxPortfolio`;
- `GetSandboxPositions`;
- `GetSandboxOperationsByCursor`;
- `GetSandboxWithdrawLimits`.

Обязательные orders:

- `PostSandboxOrder`;
- `CancelSandboxOrder`;
- `GetSandboxOrderState`;
- `GetSandboxOrders`;
- `GetSandboxMaxLots` (preflight/diagnostics, не замена risk manager);
- опционально `GetSandboxOrderPrice`.

Provisioning-команды, но не автоматический startup:

- `OpenSandboxAccount`;
- `SandboxPayIn`;
- `CloseSandboxAccount`.

Не включать в первый order pipeline:

- async order;
- replace order;
- stop orders.

Причина: синхронный `PostSandboxOrder` даёт более простой initial lifecycle;
async/replace/stop расширяют state machine и должны добавляться отдельным
решением.

Источники:

- `.ai/references/tinvest/invest-contracts/src/docs/contracts/sandbox.proto`;
- `.ai/references/tinvest/invest-contracts/src/md/intro/developer/sandbox/index.md`;
- `.ai/references/tinvest/invest-contracts/src/md/intro/developer/sandbox/methods.md`.

### 3.4. Order и operations streams

Для исполнения предпочесть `OrderStateStream` как основной realtime источник
переходов order state и partial fills. `TradesStream` допустим как
дополнительный источник исполнения; события должны дедуплицироваться.

При reconnect/reconciliation authoritative fallback:

1. `GetSandboxOrders` для активных заявок;
2. `GetSandboxOrderState` для известных order/client IDs;
3. `GetSandboxPositions` и `GetSandboxPortfolio`;
4. `GetSandboxOperationsByCursor` для восстановления операций/комиссий.

Не использовать `Operation.id` как стабильный первичный ключ: документация
говорит, что ID может изменяться. `trade_id` operations также может отличаться
от order service. Для исполнения приоритетны trade/order identifiers сервиса
заявок, для комиссий — operation semantics и `parent_operation_id`.

Источники:

- `.ai/references/tinvest/invest-contracts/src/docs/contracts/orders.proto`;
- `.ai/references/tinvest/invest-contracts/src/docs/contracts/operations.proto`;
- `.ai/references/tinvest/invest-contracts/src/md/services/orders/orders_state_stream.md`;
- `.ai/references/tinvest/invest-contracts/src/md/services/operations/operations_stream.md`;
- `.ai/references/tinvest/invest-contracts/src/md/services/operations/operations_problems.md`.

## 4. Особенности sandbox

- Sandbox endpoint поддерживает обычные сервисы и специальные
  `SandboxService` методы.
- Виртуальные счета могут быть удалены; срок хранения — три месяца от
  последнего использования. Отсутствующий configured account должен блокировать
  запуск и предлагать явную provisioning-команду, но не создавать новый счёт
  молча.
- Часть portfolio indicators, risk rates, guarantee coverage и liquidity не
  рассчитывается либо упрощена.
- Плечо моделируется упрощённо как 2; для futures списывается/начисляется полная
  стоимость, variation margin не рассчитывается.
- Нет купонов, дивидендов и налогов.
- Комиссия sandbox — 0,05% объёма сделки.
- Market order исполняется по last price без моделирования глубины.
- Пересекающий стакан limit order исполняется полностью при наличии хотя бы
  одного встречного лота; ожидающий limit order не влияет на стакан.
- Ожидающая заявка исполняется по указанной limit price при подходящей последней
  сделке.
- Неисполненные заявки отменяются после окончания торговой сессии.
- `GetSandboxOrderState` хранит sandbox orders семь дней.
- Broker report и foreign-dividend report в sandbox недоступны/пусты.

Эти свойства не должны переноситься в simulated backtest как «реалистичная»
модель биржи и не должны использоваться для вывода о production execution.

## 5. Деньги, цены и количества

Proto:

```text
MoneyValue { currency, units, nanos }
Quotation  { units, nanos }
```

Правила mapper:

- decimal = `units + nanos / 1_000_000_000` без промежуточного `float64`;
- модуль `nanos` меньше `1_000_000_000`;
- `units` и `nanos` должны иметь согласованный знак согласно API-правилам;
- `MoneyValue.currency` нормализуется в uppercase;
- `Quotation` получает asset только из контекста инструмента/поля, не
  «угадывает» его;
- цена API указана за одну единицу инструмента; стоимость lot =
  `price * lot`;
- `PostOrder.quantity` выражена в целых lot (`int64`);
- доменное количество единиц обязано делиться на lot без остатка до вызова API;
- limit price округляется/валидируется по `min_price_increment`;
- отсутствующее/нулевое значение не подменяется нулём, если поле семантически
  optional/unknown.

Для bonds/futures/options проверить специальные правила отображения цены до
реализации торговли ими. Без отдельного mapper/test их capability
`PlaceOrder` должен быть выключен.

## 6. Client order ID и идемпотентность

`PostOrderRequest.order_id` — обязательный ключ идемпотентности в формате UID,
максимальная длина 36. Использовать UUID, созданный приложением до сохранения
`OrderIntent`, и повторно передавать тот же ID.

Ключ обязан:

- сохраняться до network call;
- оставаться стабильным после restart;
- быть отдельным от биржевого `order_id`;
- передаваться в формате UID, иначе `OrderStateStream.order_request_id` может
  быть заменён API-сгенерированным значением и correlation потеряется.

После timeout/connection loss во время `PostSandboxOrder` адаптер возвращает
`UnknownOutcome` и никогда самостоятельно не отправляет новый order. Execution
service выполняет lookup/reconciliation тем же client ID. Если lookup по client
ID конкретным unary методом не подтверждён контрактом, использовать stream,
список orders и операции; это открытый sandbox integration test.

Источники:

- `.ai/references/tinvest/invest-contracts/src/docs/contracts/orders.proto`
  (`PostOrderRequest.order_id`);
- `.ai/references/tinvest/invest-contracts/src/md/services/orders/orders_state_stream.md`;
- `.ai/references/tinvest/invest-contracts/src/md/services/orders/faq_orders.mdx`.

## 7. Stream lifecycle и reconnect

Adapter хранит desired subscription set отдельно от конкретного gRPC stream.

Алгоритм:

1. открыть connection/stream с контекстом lifecycle;
2. отправить desired subscriptions батчами;
3. дождаться и проверить subscription acknowledgements;
4. публиковать нормализованные data и health events;
5. на terminal transport error закрыть stream;
6. выполнить exponential backoff с full jitter, например 250 ms → 500 ms →
   1 s → 2 s → 5 s, cap 30 s;
7. открыть новый stream и заново отправить актуальный desired set;
8. после acknowledgement отметить stream healthy.

Backoff сбрасывать только после устойчивого healthy периода, чтобы избежать
tight reconnect loop. Shutdown context прекращает reconnect немедленно.

Требования к доставке:

- market snapshots могут coalesce/drop только по явно заданной политике
  (`latest wins`) и с метрикой потерь;
- order/execution events нельзя silently drop: bounded queue overflow переводит
  соответствующий scope в degraded/blocked и запускает reconciliation;
- consumer не должен блокировать gRPC receive loop;
- subscription mutations сериализуются одним writer;
- timestamps сохраняются в UTC;
- после reconnect порядок между старым и новым stream не предполагается:
  dedup/reconciliation обязательны.

API ограничивает частоту отдельных market updates: order book/candles — не чаще
одного сообщения на подписку за 100 ms, trades/last price/info — без такого
интервала. Это не гарантия отсутствия burst.

## 8. Error mapping

Нормализованная ошибка должна содержать:

- operation;
- gRPC status;
- T-Invest numeric code из details, если доступен;
- category;
- retry hint;
- outcome (`known_not_applied`, `unknown`);
- safe message без token/metadata.

Категории:

| Источник | Категория | Поведение |
|---|---|---|
| `INVALID_ARGUMENT` / 300xx | InvalidRequest | permanent, исправить config/input |
| `UNAUTHENTICATED` / 40003 | Authentication | permanent до смены token |
| `PERMISSION_DENIED` / 40002, 40004 | Permission | permanent/block scope |
| `NOT_FOUND` / 50002, 50004, 50005 | NotFound | permanent либо reconciliation signal |
| `RESOURCE_EXHAUSTED` / 80001..80006 | RateLimited/Capacity | retry с backoff; учесть reset metadata |
| `UNAVAILABLE` | Unavailable | transient для read-only; order outcome зависит от фазы |
| `DEADLINE_EXCEEDED` | Timeout | transient для read-only; `UnknownOutcome` для order |
| `INTERNAL` / 70001..70003 | ExchangeInternal | transient; order может быть unknown |
| `FAILED_PRECONDITION` / 900xx | Rejected/Precondition | known rejection, не retry автоматически |
| context canceled | Canceled | не retry при shutdown |

Текст ошибки не использовать как основной classifier. API error catalog JSON
можно генерировать в lookup table/test fixture, но неизвестный code должен
сохраняться как `UnknownAPIError`.

Критическая грань: `PostOrder` можно автоматически retry только если
достоверно доказано, что запрос не ушёл. Любой timeout, EOF или unavailable
после возможной отправки возвращается как `UnknownOutcome`.

Источники:

- `.ai/references/tinvest/invest-contracts/src/md/intro/developer/error-codes/errors.md`;
- `.ai/references/tinvest/invest-contracts/src/md/intro/developer/error-codes/http_errors.md`;
- `.ai/references/tinvest/invest-contracts/src/md/intro/developer/error-codes/errors-ws.md`;
- `.ai/references/tinvest/invest-contracts/src/docs/errors/api_errors.json`.

## 9. Rate limits и deadlines

Документированные базовые лимиты:

| Scope | Лимит |
|---|---:|
| рекомендованный общий IP rate | ≤ 50 req/s |
| Instruments unary | 200/min |
| Operations unary | 200/min |
| Market data unary | 600/min |
| Sandbox service unary | 200/min |
| Orders unary | 100/min |
| `GetOrders` | 200/min |
| `PostOrder` | 15/s, 900/min |
| `CancelOrder` | 100/min |
| market streams | 32 |
| order streams | 16 на тип |
| operations streams | 11 на тип |
| candles+books+trades subscriptions на stream | 300 суммарно |
| subscription mutation requests | 100/min |

Лимиты динамические и общие по счетам пользователя. Нужен общий process-level
rate limiter на token/user, плюс service buckets; не limiter на каждый worker.
Не использовать опубликованные пределы полностью: оставить запас для
reconciliation и reconnect.

Рекомендуемые начальные deadlines (configurable, не API-гарантии):

- metadata/status unary: 5 s;
- candles/operations pages: 10–15 s;
- place/cancel/get order: 5 s;
- stream connect: 10 s;
- stream lifetime ограничивается lifecycle, не unary deadline.

При `RESOURCE_EXHAUSTED` учитывать server reset metadata, если она присутствует;
иначе exponential backoff с jitter. Не retry permanent errors.

Источник:

- `.ai/references/tinvest/invest-contracts/src/md/intro/intro/limits.md`.

## 10. Capability matrix MVP

| Capability | Sandbox MVP | Примечание |
|---|---:|---|
| Instrument lookup/metadata | Да | UID primary, FIGI alias |
| Historical candles | Да | интервалы/максимальный период валидировать |
| Streaming candles | Да | subscription ack обязателен |
| Streaming order book | Да | depth только 1/10/20/30/40/50 |
| Streaming public trades | Да | |
| Last price | Да | unary + stream |
| Trading status | Да | unary + Info stream |
| Portfolio/positions | Да | часть sandbox metrics отсутствует |
| Operations history | Да | cursor API предпочтителен |
| Market orders | Да, для проверенных типов | instrument flags + session |
| Limit orders | Да, для проверенных типов | tick validation |
| Cancel order | Да | |
| Open orders / order state | Да | |
| Partial fill events | Да | OrderStateStream + reconciliation |
| Stable client order ID | Да | UID ≤ 36 |
| Replace order | Нет | после MVP |
| Async post order | Нет | после MVP |
| Stop orders | Нет | после MVP |
| Production trading | Нет | запрещено конфигурацией MVP |
| Margin/risk figures authoritative | Нет | sandbox упрощён |

Runtime capability дополнительно пересекается с instrument flags
`api_trade_available_flag`, `market_order_available_flag`,
`limit_order_available_flag` и trading status. Статическая capability биржи не
означает, что конкретный инструмент сейчас доступен.

## 11. Неизвестные вопросы и обязательные проверки

До этапа 9 нужны sandbox integration tests:

1. Принимает ли `GetSandboxOrderState` client UID напрямую и с каким
   `OrderIdType`; можно ли этим надёжно разрешить unknown outcome.
2. Всегда ли `OrderStateStream` на sandbox возвращает исходный
   `order_request_id` для UID.
3. Какие trade identifiers стабильны между `OrderStateStream`,
   `TradesStream`, `GetSandboxOrderState` и operations.
4. Возвращает ли повторный `PostSandboxOrder` с тем же UID исходный order,
   duplicate error или иное состояние.
5. Доступны ли order/portfolio streams на sandbox endpoint с обычными service
   RPC либо требуют особой маршрутизации.
6. Какие execution/commission events реально приходят и с какой задержкой.
7. Есть ли reset metadata в gRPC errors выбранного Go client.
8. Какие instrument classes безопасно включить в MVP. Рекомендация: начать с
   liquid shares в RUB; bonds/futures/options выключить до отдельных mapper и
   tests.
9. Требует ли выбранная версия API `instrument_id` UID либо допускает FIGI во
   всех MVP методах.
10. Какая версия официального Go SDK точно соответствует локальным proto.

Эти проверки используют уже доступную `TINVEST_SANDBOX_TOKEN`. Если переменная
не загружена в текущую интерактивную shell, `source ~/.zshrc.private` выполняется
однократно, не перед каждым тестом. Значение token не выводится.

## 12. План тестов адаптера

- table tests `MoneyValue`/`Quotation`, включая отрицательные nanos и invalid;
- lot/tick validation без binary floating point;
- instrument mapper для каждого включённого типа;
- exhaustive enum mapping с safe unknown;
- error classifier по gRPC status и API catalog;
- deterministic fake stream: disconnect, ack rejection, reconnect/resubscribe;
- bounded queue overflow отдельно для market snapshot и execution;
- duplicate/partial fill normalization;
- transport contract tests без сети;
- optional sandbox tests с build tag и явным account provisioning;
- тест на отсутствие token в error/log.

## 13. Handoff

### Поддерживаемые методы

Спроектированы Instruments, MarketData unary/stream, Sandbox account/state/order
методы и OrderState/operations reconciliation path. Реализация кода не начата.

### Ключевые решения

- основной transport: gRPC/TLS;
- production endpoint выключен;
- UID является основным instrument ID;
- UUID client order ID сохраняется до вызова и коррелирует stream;
- unknown order outcome не retry внутри adapter;
- desired subscriptions восстанавливаются после reconnect;
- операции не считаются стабильным источником trade ID;
- decimal mapping выполняется непосредственно из units/nanos.

### Следующий шаг

Интегратору следует утвердить domain contracts (`Instrument`, `MarketEvent`,
`ExchangeError`, `Capabilities`, order IDs) и ADR по protobuf/SDK. После этого
Exchange agent может реализовать fake adapter и mapper tests, затем read-only
T-Invest, и только после стабилизации execution contract — sandbox orders.
