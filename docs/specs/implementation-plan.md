# Lazytrade: техническое задание и план реализации

## 1. Назначение документа

Этот документ является основной технической спецификацией для реализации
`lazytrade`. Он предназначен для разработчиков и автономных агентов, которые
будут выполнять отдельные этапы работы.

Перед началом задачи исполнитель обязан:

1. Прочитать этот документ полностью.
2. Проверить текущее состояние репозитория и уже реализованные этапы.
3. Не менять принятые здесь архитектурные решения без явного согласования.
4. Не смешивать реализацию нескольких крупных этапов в одном изменении, если
   между ними нет обязательной технической зависимости.
5. Для каждого этапа добавлять тесты и обновлять документацию конфигурации.

Во всех задачах запрещено задавать или переопределять environment variable
`GOCACHE`. Go toolchain должен использовать обычную конфигурацию окружения
пользователя; проблемы доступа к cache не обходятся сменой его пути.

Исходное продуктовое описание находится в `docs/archive/initial-product-vision.md`.

## 2. Цель продукта

`lazytrade` — единый исполняемый Go-файл с тремя основными подкомандами:

- `lazytrade agent` — автоматическая торговля по настроенным стратегиям;
- `lazytrade terminal` — read-only TUI для наблюдения за рынком;
- `lazytrade backtest` — детерминированный прогон стратегии на исторических
  данных без обращения к торговому API.

Первая поддерживаемая биржа — T-Invest. Архитектура должна позволять позднее
добавлять Bybit, Kraken, OKX, HTX, KuCoin, BingX и Bitfinex через независимые
адаптеры без изменения торгового ядра.

Локальная копия документации и контрактов T-Invest находится в
`.ai/references/tinvest/invest-contracts/`. При реализации адаптера сначала
используются локальные proto/OpenAPI/AsyncAPI и Markdown-материалы. Внешняя
документация нужна только для проверки актуальности или восполнения отсутствующих
сведений.

Go-адаптер T-Invest обязан использовать официальный модуль
`opensource.tbank.ru/invest/invest-go`. Использование
`github.com/russianinvestments/invest-api-go-sdk` запрещено.

Агент должен:

- одновременно исполнять несколько экземпляров стратегий;
- работать с несколькими биржами и инструментами;
- выставлять и отслеживать ордера;
- сохранять состояние в SQLite;
- восстанавливаться после перезапуска;
- рассчитывать статистику по каждой стратегии;
- применять дневной лимит убытка отдельно к каждой стратегии;
- публиковать состояние через встроенный web-интерфейс;
- позволять приостанавливать торговлю по бирже, инструменту и стратегии.

Терминал должен:

- выполнять только read-only операции;
- получать рыночные данные в реальном времени;
- отображать настраиваемые вкладки и панели;
- строить line, time-series, sparkline и OHLC/candlestick-графики;
- использовать Bubble Tea, Bubbles, Lip Gloss и `ntcharts`.

Backtesting должен:

- запускать ту же реализацию стратегии, которая используется в agent mode;
- читать исторические свечи из воспроизводимого набора данных;
- эмулировать исполнение ордеров, комиссии и проскальзывание;
- применять те же risk rules, включая дневной лимит стратегии;
- исключать доступ стратегии к будущим данным;
- формировать машиночитаемый и человекочитаемый отчёт;
- сохранять параметры, версию данных и результаты запуска.

## 3. Зафиксированные архитектурные решения

Следующие решения считаются принятыми.

### 3.1. Форма приложения

- Приложение реализуется на Go.
- Поставляется одним бинарным файлом.
- Используется CLI с подкомандами.
- На первом этапе приложение является модульным монолитом.
- `agent`, web-сервер и workers стратегий работают в одном процессе.
- Несколько экземпляров агента с одной базой одновременно не поддерживаются.

### 3.2. UI

- TUI строится на Bubble Tea, Bubbles и Lip Gloss.
- Для графиков используется `github.com/NimbleMarkets/ntcharts`.
- Код продукта не должен напрямую зависеть от деталей `ntcharts` за пределами
  пакета визуализации. Требуется внутренний интерфейс графика.
- Web UI встраивается в бинарный файл через `go:embed`.
- Для MVP обновления web UI передаются через Server-Sent Events.
- Управляющие команды выполняются через REST.

### 3.3. Конфигурация

- Формат конфигурации — YAML.
- Один файл содержит отдельные секции `agent` и `terminal`.
- Секреты не хранятся в YAML. Конфигурация содержит только имена переменных
  окружения, из которых читаются токены, account ID и DSN с паролями.
- Конфигурация валидируется целиком до запуска сетевых соединений и workers.

### 3.4. Стратегии и risk-management

- Стратегия не обращается к бирже и не выставляет ордер напрямую.
- Стратегия получает нормализованные market events и возвращает `Signal`.
- Risk manager преобразует разрешённый сигнал в намерение создать ордер.
- В первой версии один экземпляр стратегии работает с одним инструментом.
- Дневной лимит убытка задаётся отдельно для каждой стратегии.
- Глобального абсолютного дневного лимита нет.
- Каждый денежный лимит содержит `amount` и `asset`.
- Денежные значения нельзя представлять через `float32` или `float64`.
- При достижении дневного лимита стратегия получает состояние `risk_paused`.
- По умолчанию существующая позиция при этом не закрывается автоматически.
- Автоматическое возобновление стратегии на следующий день запрещено:
  требуется ручной `resume`.

### 3.5. Хранилище

- В первой версии используется SQLite и sqlc.
- В будущем допускается PostgreSQL как альтернативное хранилище.
- Одновременная работа нескольких агентов с PostgreSQL не планируется.
- Торговое ядро зависит от доменных repository-интерфейсов, а не от SQLite,
  `database/sql`, `pgx` или сгенерированных sqlc-структур.
- Не требуется универсальный SQL для SQLite и PostgreSQL.
- Для каждого SQL-диалекта должны быть собственные schema, migrations, queries
  и sqlc output.
- PostgreSQL-адаптер не входит в первый MVP, но SQLite-реализация не должна
  препятствовать его добавлению.

### 3.6. Надёжность исполнения

- Намерение создать ордер сохраняется до сетевого вызова биржи.
- Для ордеров применяется стабильный client order ID.
- Повтор запроса после неопределённой сетевой ошибки не должен создавать
  дублирующий ордер.
- После запуска агент выполняет reconciliation локального состояния с биржей.
- При критическом расхождении новые ордера блокируются.

### 3.7. Backtesting

- Backtest использует тот же `Strategy`, доменные `Signal`, risk manager и
  расчёт P&L, что и agent mode.
- Сетевой exchange adapter в backtest не используется. Исполнение выполняет
  отдельный детерминированный simulated broker.
- Стратегия видит только события, timestamp которых не больше текущего
  виртуального времени.
- Исторические события обрабатываются в стабильном порядке.
- При одинаковом config, dataset, seed и версии приложения результат должен
  совпадать.
- Базовый источник MVP — валидируемый CSV-файл со свечами OHLCV.
- Загрузка истории из exchange API допускается отдельной командой, но полученный
  dataset должен быть сохранён локально до запуска теста.
- Не допускается silently подставлять отсутствующие свечи, цены, комиссии или
  объёмы. Все assumptions входят в отчёт.
- Backtest не использует live credentials и ни при каких условиях не вызывает
  `PlaceOrder` реальной биржи.

## 4. Область реализации

### 4.1. Входит в MVP

- единый CLI-бинарник;
- загрузка и строгая валидация YAML;
- T-Invest adapter;
- работа с T-Invest sandbox;
- получение инструментов, свечей, сделок и стакана в доступном API объёме;
- read-only terminal;
- одна референсная стратегия;
- backtesting референсной и любых зарегистрированных стратегий на OHLCV;
- simulated broker с комиссиями и настраиваемым проскальзыванием;
- отчёт backtest с P&L, drawdown, сделками и risk events;
- исполнение market и/или limit orders в зависимости от возможностей sandbox;
- risk manager;
- дневной P&L и дневной лимит убытка на стратегию;
- SQLite, migrations и sqlc;
- восстановление состояния после перезапуска;
- reconciliation;
- structured logs;
- web dashboard;
- pause/resume и emergency stop;
- graceful shutdown;
- unit, integration и минимальные end-to-end тесты.

### 4.2. Не входит в MVP

- реальные денежные операции без отдельного явного разрешения;
- несколько процессов `lazytrade agent` с одной БД;
- распределённые блокировки и leader election;
- PostgreSQL adapter;
- внешняя очередь сообщений;
- пользовательский язык программирования стратегий;
- загрузка непроверенных Go plugins;
- автоматическая конвертация глобального P&L между валютами;
- high-frequency trading;
- гарантии работы с задержками биржевого уровня;
- мобильный web UI;
- автоматическое закрытие всех позиций при остановке процесса;
- tick-level backtesting по полному потоку заявок и сделок;
- моделирование собственной позиции в очереди биржевого стакана;
- portfolio backtesting нескольких стратегий с общим капиталом;
- автоматическая оптимизация параметров и перебор сетки;
- walk-forward analysis и Monte Carlo simulation.

## 5. Терминология

- **Exchange adapter** — реализация нормализованного интерфейса конкретной
  биржи.
- **Exchange account** — настроенное подключение к бирже и торговому аккаунту.
- **Instrument** — торгуемый актив или контракт в нормализованном формате.
- **Strategy type** — Go-реализация алгоритма, например moving average cross.
- **Strategy instance** — настроенный запуск strategy type для одного exchange
  account и одного instrument.
- **Worker** — runtime-процесс внутри агента, обслуживающий один strategy
  instance.
- **Signal** — торговое намерение стратегии, ещё не являющееся ордером.
- **Order intent** — прошедшее risk-проверки персистентное намерение обратиться
  к бирже.
- **Execution/fill** — полное или частичное исполнение ордера.
- **Trading day** — период расчёта дневного P&L согласно настройке стратегии.
- **Reconciliation** — сверка локального состояния с authoritative state биржи.
- **Asset** — код валюты или расчётного актива: `RUB`, `USD`, `USDT`, `BTC`.
- **Dataset** — неизменяемый набор исторических market events с metadata и
  контрольной суммой.
- **Backtest run** — один запуск strategy instance на конкретном dataset и с
  конкретной execution model.
- **Simulated broker** — детерминированная модель принятия и исполнения ордеров
  в backtest.

## 6. Предлагаемая структура репозитория

```text
cmd/lazytrade/
    main.go

internal/
    app/
        agent.go
        terminal.go
        lifecycle.go

    cli/
        root.go
        agent.go
        terminal.go
        config.go
        db.go

    config/
        config.go
        load.go
        validate.go

    domain/
        account.go
        instrument.go
        market.go
        money.go
        order.go
        position.go
        pnl.go
        signal.go
        strategy.go
        trade.go

    exchange/
        exchange.go
        registry.go
        capabilities.go
        tinvest/
            client.go
            mapper.go
            instruments.go
            marketdata.go
            orders.go
            portfolio.go

    strategy/
        strategy.go
        registry.go
        movingaverage/

    engine/
        engine.go
        worker.go
        execution.go
        reconciliation.go

    risk/
        manager.go
        daily_loss.go
        limits.go

    storage/
        storage.go
        repositories.go
        sqlite/
            store.go
            mapper.go
            generated/

    statistics/
        service.go
        pnl.go

    backtest/
        runner.go
        clock.go
        dataset.go
        report.go
        broker/
            broker.go
            fill_model.go
            fees.go
            slippage.go
        datasource/
            datasource.go
            csv.go

    control/
        service.go

    web/
        server.go
        handlers.go
        events.go
        static/

    terminal/
        model.go
        messages.go
        tabs.go
        components/
        chart/
            chart.go
            ntcharts.go

    telemetry/
        logger.go

db/
    sqlite/
        migrations/
        queries/
        schema.sql
    postgres/
        README.md

api/
    openapi.yaml

configs/
    example.yaml
    backtest.example.yaml

testdata/
    backtest/

sqlc.sqlite.yaml
go.mod
```

Допустимы небольшие изменения названий пакетов, но запрещено смешивать domain,
exchange SDK, SQL и UI в одном пакете.

## 7. Доменные требования

### 7.1. Идентификаторы

- ID сущностей создаются приложением, а не автоинкрементом БД.
- Рекомендуемый формат — UUIDv7 или ULID.
- ID представлены отдельными Go-типами либо валидируемыми строками.
- `strategy_id` стабилен между перезапусками и берётся из YAML.
- Для каждого order intent генерируется уникальный стабильный
  `client_order_id`.

### 7.2. Деньги и количество

Требуется тип:

```go
type Money struct {
    Amount decimal.Decimal
    Asset  string
}
```

Требования:

- `Amount` не использует binary floating point;
- `Asset` нормализован в uppercase;
- пустой `Asset` запрещён;
- сравнение и сложение допустимы только для одинакового `Asset`;
- несовпадение asset возвращает ошибку, а не выполняет скрытую конвертацию;
- строки сумм в YAML разбираются строго;
- количество инструмента также использует decimal или биржевой целочисленный
  формат;
- округление выполняется по шагу цены и количеству лотов конкретной биржи.

### 7.3. Время

- В домене используется `time.Time`.
- Внутри приложения время нормализуется в UTC.
- Исходная timezone хранится только там, где нужна граница торгового дня или
  отображение.
- Для тестируемости компоненты, зависящие от текущего времени, получают `Clock`.
- Нельзя вызывать `time.Now()` в расчётах risk/P&L напрямую.

## 8. Функциональные требования

### FR-CLI: командная строка

#### FR-CLI-001

Приложение должно предоставлять команды:

```text
lazytrade agent --config <path>
lazytrade terminal --config <path>
lazytrade backtest --config <path> [--output <path>]
lazytrade backtest validate-data --input <path>
lazytrade config validate --config <path>
lazytrade db migrate --config <path>
lazytrade version
```

#### FR-CLI-002

Любая ошибка конфигурации должна приводить к ненулевому exit code и сообщению с
точным YAML-путём проблемного поля.

#### FR-CLI-003

`agent` и `terminal` должны обрабатывать `SIGINT` и `SIGTERM`.

#### FR-CLI-004

CLI не должен выводить секреты или полный DSN в лог либо сообщение об ошибке.

### FR-CONFIG: конфигурация

#### FR-CONFIG-001

Конфигурация должна иметь версию схемы:

```yaml
version: 1
```

#### FR-CONFIG-002

Должны поддерживаться секции:

```yaml
database:
logging:
exchanges:
agent:
terminal:
backtest:
```

#### FR-CONFIG-003

Секция exchange account должна включать:

- уникальный ID;
- type адаптера;
- ссылки на environment variables с credentials;
- sandbox/production mode;
- специфичные для адаптера параметры.

#### FR-CONFIG-004

Каждый strategy instance должен включать:

- уникальный `id`;
- exchange account ID;
- один instrument ID;
- strategy type;
- параметры стратегии;
- execution policy;
- risk policy;
- trading-day policy.

#### FR-CONFIG-005

Валидация должна обнаруживать:

- неизвестные поля YAML;
- дублирующиеся ID;
- ссылки на отсутствующий exchange account;
- неизвестный strategy type;
- отсутствующие environment variables;
- неверные decimal-значения;
- неположительные лимиты;
- несовместимые execution options;
- пустой asset;
- конфликт валюты лимита с расчётным asset инструмента после загрузки
  metadata;
- неподдерживаемые terminal panel type и chart mode.

Статическую валидацию выполнить до сети. Проверки, требующие metadata биржи,
выполнить до запуска workers.

#### FR-CONFIG-006

Валидация должна учитывать запускаемую команду:

- `agent` проверяет credentials, exchange account и live/sandbox metadata;
- `terminal` проверяет только используемые им exchange accounts;
- `backtest` не требует exchange credentials и не создаёт exchange clients;
- `backtest` берёт instrument metadata из dataset/config и проверяет
  согласованность с описанием стратегии;
- `config validate` по умолчанию валидирует всю статическую схему, а
  command-specific проверки выполняет с флагом `--for agent|terminal|backtest`.

Один отсутствующий секрет для неиспользуемой команды не должен блокировать
offline backtest.

#### FR-CONFIG-007

Текущий runtime MVP запускает несколько strategy instances через один
настроенный exchange account. Это временное ограничение реализации, а не
ограничение конфигурационной модели: каждый strategy instance уже ссылается на
свой `exchange account ID`.

После стабилизации однобиржевого sandbox-контура один процесс `agent` должен
поддерживать несколько exchange accounts и adapters одновременно. Разные
strategy instances могут быть направлены на разные биржи и счета. Стратегия не
может обращаться к adapter, account state, history cursor или execution stream
другого `exchange account ID`.

### FR-EXCHANGE: интерфейс биржи

#### FR-EXCHANGE-001

Ядро должно использовать нормализованный интерфейс:

```go
type Exchange interface {
    Name() string
    Capabilities() Capabilities
    Instruments(ctx context.Context) ([]Instrument, error)
    Portfolio(ctx context.Context, accountID string) (Portfolio, error)
    SubscribeMarketData(
        ctx context.Context,
        subscriptions []Subscription,
    ) (<-chan MarketEvent, <-chan error)
    PlaceOrder(ctx context.Context, order NewOrder) (Order, error)
    CancelOrder(ctx context.Context, orderID string) error
    GetOrder(ctx context.Context, orderID string) (Order, error)
    OpenOrders(ctx context.Context, accountID string) ([]Order, error)
}
```

Конкретная сигнатура может уточняться, но функции должны оставаться
разделёнными по назначению и не раскрывать SDK-типы.

#### FR-EXCHANGE-002

Адаптер должен преобразовывать:

- instrument metadata;
- prices и quantities;
- candles;
- trades;
- order book snapshots/updates;
- order states;
- fills;
- portfolio/positions;
- ошибки и rate-limit события.

#### FR-EXCHANGE-003

Адаптер должен предоставлять capability flags как минимум для:

- market orders;
- limit orders;
- stop orders;
- order book;
- streaming candles;
- sandbox.

#### FR-EXCHANGE-004

T-Invest adapter должен поддерживать sandbox первым. Production mode должен
быть заблокирован по умолчанию и требовать отдельного явного параметра,
например:

```yaml
allow_live_trading: false
```

#### FR-EXCHANGE-005

Переподключение market-data stream должно использовать bounded exponential
backoff с jitter. После переподключения подписки восстанавливаются.

#### FR-EXCHANGE-006

Ошибки биржи классифицируются как минимум на:

- invalid request;
- authentication;
- permission;
- insufficient funds;
- rate limited;
- transient/network;
- unknown outcome;
- permanent.

### FR-MARKET: рыночные данные

#### FR-MARKET-001

Все события должны иметь:

- exchange account ID;
- instrument ID;
- exchange timestamp, если доступен;
- local received timestamp;
- sequence/revision, если доступна;
- тип события.

#### FR-MARKET-002

Медленный TUI, web client или стратегия не должны бесконечно блокировать
market-data reader. Очереди должны быть bounded, а политика переполнения —
явной и измеримой.

#### FR-MARKET-003

Для snapshot-подобных данных допускается coalescing последних обновлений.
Execution/fill и control events терять запрещено.

### FR-STRATEGY: стратегии

#### FR-STRATEGY-001

Стратегия реализует интерфейс следующего смысла:

```go
type Strategy interface {
    Type() string
    RequiredData() DataRequirements
    OnEvent(
        ctx context.Context,
        state StrategyState,
        event MarketEvent,
    ) ([]Signal, error)
}
```

#### FR-STRATEGY-002

Стратегия не может:

- вызывать exchange adapter;
- записывать напрямую в БД;
- отправлять HTTP-запросы;
- обходить risk manager;
- самостоятельно менять runtime status.

#### FR-STRATEGY-003

Первая референсная стратегия — moving average cross либо эквивалентная простая
детерминированная стратегия. Она нужна для проверки всей вертикали, а не как
рекомендация для реальной торговли.

#### FR-STRATEGY-004

Состояние стратегии должно сериализоваться и восстанавливаться после
перезапуска. Формат состояния должен иметь версию.

#### FR-STRATEGY-005

Один strategy worker обрабатывает события последовательно. Это исключает гонки
в состоянии конкретной стратегии. Разные workers работают параллельно.

#### FR-STRATEGY-006

Signal должен содержать:

- strategy ID;
- exchange account ID;
- instrument ID;
- action;
- order type;
- quantity или policy её вычисления;
- optional limit price;
- reason;
- timestamp;
- уникальный signal ID.

### FR-RISK: risk-management

#### FR-RISK-001

Дневной лимит убытка задаётся внутри каждого strategy instance:

```yaml
risk:
  max_daily_loss:
    amount: "1000"
    asset: RUB
    pnl: total
    action: pause
```

#### FR-RISK-002

Глобальный абсолютный `max_daily_loss` отсутствует.

#### FR-RISK-003

В первой версии asset лимита должен совпадать с расчётным asset инструмента.
Скрытая валютная конвертация запрещена.

#### FR-RISK-004

Поддерживаются режимы P&L:

- `realized`;
- `total`.

`total` означает realized + unrealized с учётом комиссий и funding, если
соответствующие значения предоставляет биржа.

#### FR-RISK-005

Рекомендуемый режим по умолчанию — `total`. Дневной P&L по возможности
определяется через изменение equity:

```text
current equity
- start-of-day equity
- external deposits
+ external withdrawals
```

Если биржа не предоставляет достаточно данных, адаптер обязан явно объявить
ограниченный режим расчёта. Нельзя молча показывать неполный P&L как полный.

#### FR-RISK-006

Trading-day policy задаёт timezone и границу дня:

```yaml
trading_day:
  timezone: Europe/Moscow
  reset_at: "00:00"
```

В будущем допускается `type: exchange_session`.

#### FR-RISK-007

При достижении лимита:

1. статус меняется на `risk_paused`;
2. новые signals и order intents блокируются;
3. активные ордера на открытие позиции отменяются, если это безопасно и
   поддерживается биржей;
4. открытая позиция по умолчанию сохраняется;
5. событие фиксируется в audit log;
6. web UI и TUI status получают обновление;
7. стратегия не возобновляется автоматически на следующий день.

#### FR-RISK-008

Поддерживаются состояния:

```text
running
user_paused
risk_paused
stopped
failed
reconciling
blocked
```

Переходы должны быть описаны и протестированы как конечный автомат.

#### FR-RISK-009

Кроме дневного лимита архитектура должна допускать limits:

- max order value;
- max position value;
- max open orders;
- cooldown between orders;
- allowed trading schedule.

Для MVP обязательны max daily loss и max position value.

### FR-ENGINE: торговый движок

#### FR-ENGINE-001

Обязательный pipeline:

```text
MarketEvent
→ Strategy
→ Signal
→ RiskManager
→ persisted OrderIntent
→ Exchange.PlaceOrder
→ persisted Order
→ Execution updates
→ Position/PnL/Statistics
```

#### FR-ENGINE-002

`OrderIntent` сохраняется до вызова `PlaceOrder`.

#### FR-ENGINE-003

Повторная обработка одного signal ID должна быть идемпотентной.

#### FR-ENGINE-004

Unknown-outcome ошибка при `PlaceOrder` не разрешает немедленно создавать новый
ордер. Сначала выполняется поиск по client order ID или reconciliation.

#### FR-ENGINE-005

Частичные исполнения должны поддерживаться как отдельные execution records.

Каждое полученное исполнение до изменения `orders`, `positions`, P&L и
статистики сохраняется в durable `execution_inbox`. Inbox не имеет внешнего
ключа на локальный order, поэтому допускает получение исполнения до завершения
локального order recovery. Staging дедуплицируется по стабильному exchange trade
ID и checksum payload. Применение исполнения, всех производных проекций и
переход inbox `pending → applied` выполняется одной транзакцией.

Неразрешённая запись inbox не удаляется и блокирует переход account runtime в
`running`. Startup обязан повторно применить все `pending` записи после
восстановления orders. Inbox защищает уже полученные события, но не заменяет
восстановление истории биржи: executions, которые процесс не успел получить до
crash, загружаются history reconciliation и проходят через тот же staging API.

Для T-Invest оперативное восстановление использует bounded UTC window с
перекрытием и полностью проходит пагинацию `GetOperationsByCursor`. Каждая
BUY/SELL operation связывается с заявкой через документированный bridge
`OperationItem.id → GetOrderState`; только после этого разрешено использовать
`order_request_id`, exchange order ID и `OrderState.stages[].trade_id`.
Невозможность доказать эту связь блокирует runtime. API cursor применяется
только внутри одного scan и не сохраняется как долговечный checkpoint.

Checkpoint хранит account-wide временной watermark `covered_through` и
продвигается только после полной загрузки окна и успешного применения данных.
Повторный scan начинается с overlap до watermark; inbox дедуплицирует stage
trade IDs. Накопительная `executed_commission` не распределяется повторно по
immutable fills: для неё используется отдельная монотонная order-level
projection, применяющая только новую положительную дельту комиссии. Одинаковое
значение идемпотентно; регресс и смена asset блокируют runtime.

Startup recovery выполняется в порядке:

```text
resolve persisted orders
→ open execution stream and durable ingress pump
→ scan and attribute bounded history
→ stage/apply fills and cumulative commission
→ drain pending inbox
→ reconcile portfolio/open orders
→ advance history checkpoint
→ submit ready intents
→ post-subscription reconciliation
→ market stream / running
```

Для sandbox без checkpoint допускается ограниченный bootstrap lookback 24 часа
с overlap 15 минут. До включения production обязательны конфигурируемый
`bootstrap_from`, overlap, visibility delay, recovery deadline и convergence
policy; произвольный production lookback запрещён.

Реальная sandbox-проверка 2026-08-02 установила: cursor API возвращает `70001`
для окна, заканчивающегося непосредственно текущим временем, поэтому runtime
использует visibility delay 5 минут. Для старых завершённых sandbox operations
bridge `OperationItem.id → GetSandboxOrderState` может вернуть `50005 NotFound`.
Такой результат считается неразрешимой историей и блокирует recovery; считать
operation trade автоматически принадлежащим стратегии по инструменту или
времени запрещено. Перед production требуется подтвердить bridge на свежем
ордере и определить fallback (например broker report для live API).

Повторная проверка 2026-08-02 не смогла создать свежую контрольную операцию:
Московская биржа была закрыта (воскресенье), `PostOrder` для GAZP вернул
`30079 Instrument is not available for trading`; exchange order не был создан.
Sandbox в тот же период эпизодически возвращал `70001 Internal` для read-only
accounts/portfolio вызовов, при повторе `account list` завершался успешно.
Fresh-order bridge probe необходимо повторить в торговую сессию; эти ошибки не
доказывают ни работоспособность, ни неработоспособность bridge.

### Политика повторов T-Invest API

Адаптер автоматически повторяет только идемпотентные unary-операции чтения:
accounts, instruments, portfolio, order state/list, market data и bounded
execution history. Повтор разрешён лишь для transient transport/server ошибок
(`Internal`, `Unavailable`, `DeadlineExceeded`) и rate limit, пока общий
`context` запроса не отменён. По умолчанию выполняется не более трёх попыток с
ограниченным exponential backoff и jitter. `NotFound`, ошибки валидации,
аутентификации, прав и прочие permanent ошибки возвращаются после первой
попытки. Все попытки одного чтения используют неизменный request, включая
границы `from`/`to` history scan.

Mutation RPC (`OpenSandboxAccount`, `SandboxPayIn`, `PostOrder`, `CancelOrder`)
никогда не отправляются повторно на уровне адаптера. При неоднозначном
transport outcome дальнейшее решение принимается reconciliation по внешнему
состоянию и idempotency key, а не слепым повтором команды. Открытие streaming
RPC и их reconnect policy также не используют unary retry helper. Обогащение
execution stream через `GetOrderState` ограничено двумя попытками, чтобы один
медленный ответ не блокировал поток надолго.

Для mutation error действует безопасная классификация по умолчанию:
`Canceled`, `Unknown`, `DeadlineExceeded`, `AlreadyExists`, `Aborted`,
`Internal`, `Unavailable`, `DataLoss` и неизвестные статусы означают
`UnknownOutcome` и не являются retryable. `KnownNotApplied` разрешён только для
явно распознанных отказов до применения операции: invalid request,
authentication, permission, not found, insufficient funds, rate limit и
business rejection. Если mutation RPC завершился без transport error, но его
ответ отсутствует, пуст или не проходит domain validation, результат также
считается `UnknownOutcome`: сервер уже мог применить команду.

Текущий sandbox smoke cleanup при `CancelOrder(UnknownOutcome)` обязан вызвать
отмену ровно один раз и затем разрешать исход только чтением `GetOrder`:
`cancelled` подтверждает отмену, а `filled` или `rejected` подтверждают иной
терминальный исход. `accepted`/`partially_filled` и временный `NotFound`
остаются неоднозначными и наблюдаются лишь до bounded cleanup timeout. После
timeout cleanup завершается fail-closed и требует ручной проверки; повторный
`CancelOrder` запрещён.

Перед добавлением отмены в основной agent runtime требуется отдельная durable
модель cancel-команды, не смешанная с `OrderStatus`:
`ready → cancelling → confirmed | unknown | not_applied | superseded`.
Переход в `cancelling` сохраняется до RPC; crash или неоднозначный ответ
восстанавливаются только через order/history reconciliation. Разрешение должно
атомарно обновлять cancellation record, наблюдаемый terminal order status и
audit event. Пока этой модели нет, runtime не инициирует отмены заявок.

#### FR-ENGINE-006

Worker одной стратегии не должен останавливать workers других стратегий.
Критическая ошибка инфраструктуры может перевести весь агент в blocked mode.
После локальной ошибки worker стратегия переводится в `failed` и перестаёт
получать market events. Общий execution stream продолжает обрабатываться:
исполнения ранее принятых ордеров failed-стратегии должны быть сохранены.
Остальные стратегии того же exchange account продолжают работу.
Сохранённые signals failed-стратегии, ещё не прошедшие risk processing,
терминализируются как `reject` с reason code `strategy_failed`. Операция
фильтруется по `strategy_id`, повторяема и выполняется до глобального signal
recovery при следующем запуске.

Неразрешённые order intents failed-стратегии обрабатываются fail-closed:

- найденный по `client_order_id` биржевой ордер сохраняется как `submitted` и
  продолжает отслеживаться общим execution stream;
- гарантированно неотправленный `ready` переводится в `not_submitted`;
- `submitting` и `unknown`, для которых lookup вернул `NotFound`, не отправляются
  повторно: `NotFound` не доказывает отсутствие заявки из-за eventual
  consistency и crash boundary;
- неоднозначный intent сохраняет исходный status и переводит account runtime в
  `blocked` до reconciliation;
- intents других стратегий не изменяются;
- автоматическая отмена найденного ордера не является частью worker-failure
  policy, поскольку cancel также может иметь unknown outcome.

Order intent использует durable phase model: `ready → submitting →
submitted|rejected|unknown`. Переход `ready → submitting` фиксируется атомарным
compare-and-set вместе с audit event строго до вызова exchange API. Только
`ready` разрешено отправлять или переводить в `not_submitted`. После restart
`submitting` и `unknown` сначала разрешаются lookup по `client_order_id` и
никогда автоматически не отправляются повторно. Legacy `pending` мигрируется в
`unknown`, поскольку старая запись не позволяет доказать, был ли начат API call.

### FR-RECON: восстановление и reconciliation

#### FR-RECON-001

До разрешения торговли агент должен:

1. открыть и мигрировать БД;
2. загрузить runtime state;
3. подключиться к биржам;
4. получить портфель и открытые ордера;
5. сопоставить их с локальными order intents/orders;
6. восстановить неизвестные fills, если возможно;
7. отметить расхождения;
8. только затем запустить стратегии.

#### FR-RECON-002

Критическими считаются:

- неизвестная открытая позиция по инструменту стратегии;
- unknown-outcome intent, который нельзя однозначно разрешить;
- несовместимая валюта позиции;
- повреждённое состояние стратегии;
- невозможность получить authoritative state биржи.

При критическом расхождении соответствующая стратегия получает `blocked`.

#### FR-RECON-003

Reconciliation должен быть повторяемым и идемпотентным.

### FR-BACKTEST: историческое тестирование стратегий

#### FR-BACKTEST-001

Команда:

```text
lazytrade backtest --config <path> [--output <path>]
```

должна запускать один или несколько описанных backtest runs без подключения к
торговому API.

#### FR-BACKTEST-002

Backtest обязан использовать ту же зарегистрированную реализацию `Strategy`,
что и live agent. Запрещено создавать отдельную «backtest-версию» алгоритма.

Runtime-specific зависимости должны передаваться через порты:

```go
type MarketEventSource interface {
    Events(ctx context.Context) (<-chan MarketEvent, <-chan error)
}

type Broker interface {
    Submit(ctx context.Context, order NewOrder) (Order, error)
    Cancel(ctx context.Context, orderID string) error
    OnMarketEvent(ctx context.Context, event MarketEvent) ([]Execution, error)
}
```

Конкретные интерфейсы могут уточняться, но strategy API и risk pipeline должны
оставаться общими для live и backtest.

#### FR-BACKTEST-003

MVP должен поддерживать CSV dataset со следующими обязательными полями:

```text
timestamp,open,high,low,close,volume
```

Metadata запуска либо config дополнительно определяют:

- exchange;
- instrument;
- interval;
- price asset;
- timezone исходных timestamp;
- tick size;
- lot size.

Decimal-поля читаются без промежуточного `float64`.

#### FR-BACKTEST-004

Dataset validator должен проверять:

- корректность заголовка и типов;
- строго возрастающее время;
- отсутствие дублирующихся timestamp;
- `low <= open/close <= high`;
- неотрицательный volume;
- соответствие interval;
- отсутствие либо явное наличие gaps;
- соответствие instrument и asset конфигурации;
- checksum файла.

При gap политика задаётся явно: `fail`, `allow` или `mark`. По умолчанию —
`fail`.

#### FR-BACKTEST-005

Виртуальные часы продвигаются только по событиям dataset. Стратегия, risk
manager, trading-day boundary и simulated broker используют один backtest
`Clock`.

Запрещено использовать wall clock для решений backtest.

#### FR-BACKTEST-006

События сортируются детерминированно по:

1. event timestamp;
2. типу события согласно документированному приоритету;
3. стабильному sequence внутри dataset.

Порядок не должен зависеть от map iteration или goroutine scheduling.

#### FR-BACKTEST-007

Для candle-based MVP применяется консервативная fill model:

- signal, возникший после закрытия свечи `N`, не может исполниться по цене,
  существовавшей раньше этого закрытия;
- market order исполняется не раньше open свечи `N+1`;
- limit order исполняется на свече `N+1` или позднее, только если диапазон
  свечи пересёк limit price;
- если внутри одной свечи могли сработать несколько взаимоисключающих условий и
  порядок неизвестен, применяется заранее описанная консервативная политика;
- fill price корректируется на commission и slippage согласно config;
- частичное исполнение в candle-based MVP по умолчанию не моделируется.

Нельзя использовать close той же свечи для мгновенного исполнения сигнала,
рассчитанного по этому close.

#### FR-BACKTEST-008

Execution model задаётся явно:

```yaml
execution:
  initial_cash:
    amount: "100000"
    asset: RUB
  commission:
    type: percent
    value: "0.03"
  slippage:
    type: basis_points
    value: "5"
  market_fill: next_open
  limit_fill: touch
```

Значения процентов и basis points должны иметь однозначно документированные
единицы. Нулевые комиссия и slippage разрешены только при явном указании.

#### FR-BACKTEST-009

Backtest применяет strategy risk configuration, включая:

- max daily loss;
- max position value;
- trading-day timezone/reset;
- состояние `risk_paused`.

Для backtest `risk_paused` сохраняется до конца запуска, если config явно не
задаёт сценарий ручного resume. Автоматически моделировать действия
пользователя запрещено.

#### FR-BACKTEST-010

Каждый run обязан сохранять:

- run ID;
- timestamp запуска;
- application version и commit, если доступен;
- strategy type и все параметры;
- risk и execution config;
- dataset path/ID, размер и checksum;
- начальный капитал и asset;
- период данных;
- seed;
- status и error;
- итоговые metrics;
- список simulated orders, executions, positions и risk events.

#### FR-BACKTEST-011

Минимальные итоговые metrics:

- initial и final equity;
- absolute и percentage return;
- realized, unrealized и total P&L;
- commissions и modeled slippage cost;
- maximum drawdown в amount и percent;
- число orders и executions;
- число закрытых сделок;
- win rate;
- gross profit и gross loss;
- profit factor;
- средняя прибыльная и убыточная сделка;
- exposure time;
- количество risk rejections и risk pauses;
- completeness/warnings.

Sharpe ratio не является обязательным для MVP. Если он будет добавлен, формула,
частота доходностей и risk-free rate должны присутствовать в отчёте.

#### FR-BACKTEST-012

Результат выводится:

- краткой таблицей в stdout;
- полным JSON-отчётом;
- при необходимости CSV-файлом simulated trades.

Путь вывода задаётся CLI/config. Формат JSON должен иметь `schema_version`.

#### FR-BACKTEST-013

Metadata и summary backtest run сохраняются в основной БД. Большие временные
ряды equity и списки событий допускается хранить в отдельных artifact-файлах,
сохраняя их путь и checksum в БД.

Исторические свечи не требуется постоянно дублировать в SQLite.

#### FR-BACKTEST-014

Повторный запуск с одинаковыми:

- application version;
- config;
- dataset checksum;
- seed

должен создавать идентичные orders, executions и metrics, кроме run ID и
wall-clock metadata.

#### FR-BACKTEST-015

Backtest report обязан содержать предупреждение, что исторические результаты не
гарантируют будущую доходность, а candle-based fill model не воспроизводит
реальную очередь заявок и внутрисвечной порядок цен.

#### FR-BACKTEST-016

Прерывание через SIGINT/SIGTERM должно завершить run со статусом `cancelled`,
закрыть artifact-файлы и не оставлять результат со статусом `completed`.

### FR-STORAGE: хранение

#### FR-STORAGE-001

Торговое ядро использует интерфейс:

```go
type Storage interface {
    Strategies() StrategyRepository
    Orders() OrderRepository
    Trades() TradeRepository
    Positions() PositionRepository
    Statistics() StatisticsRepository
    Controls() ControlRepository
    Audit() AuditRepository
    WithinTx(ctx context.Context, fn func(Storage) error) error
    Close() error
}
```

Интерфейс может быть разделён на меньшие зависимости для сервисов.

#### FR-STORAGE-002

sqlc-типы не должны выходить за пределы storage adapter.

#### FR-STORAGE-003

Минимальные таблицы:

- `strategy_instances`;
- `strategy_states`;
- `order_intents`;
- `orders`;
- `order_executions`;
- `positions`;
- `equity_snapshots`;
- `pnl_events`;
- `control_states`;
- `daily_statistics`;
- `audit_events`;
- `backtest_runs`;
- `backtest_artifacts`;
- `schema_migrations`.

#### FR-STORAGE-004

Для SQLite включить:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

#### FR-STORAGE-005

Записи executions и audit events не обновляются задним числом без отдельного
correction event.

#### FR-STORAGE-006

Схема должна обеспечивать уникальность:

- strategy ID;
- client order ID;
- exchange order ID в пределах exchange account;
- execution ID в пределах exchange account;
- однократную обработку signal ID.

#### FR-STORAGE-007

Нужно реализовать contract tests для Storage. В будущем тот же набор тестов
обязан выполняться для PostgreSQL adapter.

#### FR-STORAGE-008

Конфигурация БД:

```yaml
database:
  driver: sqlite
  dsn: "./lazytrade.db"
```

Будущий вариант:

```yaml
database:
  driver: postgres
  dsn_env: LAZYTRADE_DATABASE_URL
```

#### FR-STORAGE-009

Правило эксплуатации: одна база принадлежит одному запущенному агенту.
Реализация должна использовать fail-fast lock:

- SQLite — process/file lock либо надёжный DB lock;
- PostgreSQL в будущем — session advisory lock.

Lock служит только защитой от случайного второго запуска, а не распределённым
механизмом.

### FR-STATS: P&L и статистика

#### FR-STATS-001

Статистика рассчитывается отдельно по каждой стратегии и asset.

#### FR-STATS-002

Минимальные показатели:

- realized P&L;
- unrealized P&L, если доступен;
- total P&L;
- commissions;
- funding, если применимо;
- количество orders;
- количество executions;
- число прибыльных и убыточных закрытий;
- start-of-day equity;
- текущая equity;
- расстояние до max daily loss;
- статус полноты расчёта.

#### FR-STATS-003

Агрегировать денежные значения разных asset в одно число запрещено.

#### FR-STATS-004

Web UI должен явно показывать asset возле каждого денежного значения.

### FR-CONTROL: управление

#### FR-CONTROL-001

Поддерживаются pause/resume на уровнях:

- exchange account;
- instrument;
- strategy instance.

#### FR-CONTROL-002

Эффективное состояние определяется наиболее строгим активным ограничением.
Например, resume стратегии не должен обходить pause exchange account.

#### FR-CONTROL-003

Поддерживается global emergency stop для запрета новых ордеров. Это логический
stop, а не глобальный денежный лимит.

#### FR-CONTROL-004

Каждая управляющая операция содержит actor, timestamp, scope, old state, new
state и reason и сохраняется в audit log.

#### FR-CONTROL-005

Resume из `risk_paused` требует явного подтверждения через отдельную команду.

### FR-WEB: web-интерфейс агента

#### FR-WEB-001

Web server по умолчанию слушает `127.0.0.1`.

#### FR-WEB-002

Статические файлы встроены через `go:embed`.

#### FR-WEB-003

Минимальные endpoints:

```text
GET  /health/live
GET  /health/ready
GET  /api/v1/status
GET  /api/v1/strategies
GET  /api/v1/orders
GET  /api/v1/executions
GET  /api/v1/statistics
GET  /api/v1/events

POST /api/v1/control/pause
POST /api/v1/control/resume
POST /api/v1/control/emergency-stop
```

#### FR-WEB-004

`/api/v1/events` использует SSE. Клиент после переподключения должен получить
актуальный snapshot, даже если промежуточные UI-события были пропущены.

#### FR-WEB-005

При bind не на loopback требуется bearer token из environment variable.
Секрет не попадает в HTML и логи.

#### FR-WEB-006

Web API не обращается напрямую к sqlc queries. Оно вызывает application
services.

### FR-TUI: терминал

#### FR-TUI-001

`lazytrade terminal` не должен выставлять, отменять или изменять ордера.

#### FR-TUI-002

TUI строится как:

```text
AppModel
├── TabBar
├── InstrumentTab
│   ├── Chart
│   ├── OrderBook
│   ├── Trades
│   └── InstrumentInfo
└── StatusBar
```

#### FR-TUI-003

Типы панелей и вкладки задаются YAML.

#### FR-TUI-004

Графический слой использует внутренний интерфейс:

```go
type Chart interface {
    SetSize(width, height int)
    SetCandles(candles []Candle)
    SetSeries(series []Series)
    SetViewport(from, to time.Time)
    View() string
}
```

`ntcharts` должен быть только одной реализацией этого интерфейса.

#### FR-TUI-005

Минимальные chart modes:

- line;
- time series;
- sparkline;
- OHLC/candles;
- volume bars.

#### FR-TUI-006

График должен корректно реагировать на resize terminal и не паниковать при
нулевом либо слишком малом размере.

#### FR-TUI-007

Рекомендуемые клавиши:

```text
q / ctrl+c  exit
tab         next tab
shift+tab   previous tab
left/right  move viewport
+/-         zoom
r           reset viewport
?           help
```

### FR-LOG: логирование и аудит

#### FR-LOG-001

Используются structured logs с уровнями debug/info/warn/error.

#### FR-LOG-002

Каждая запись, связанная со стратегией или ордером, по возможности содержит:

- strategy ID;
- exchange account ID;
- instrument ID;
- signal ID;
- client order ID;
- exchange order ID.

#### FR-LOG-003

Токены, пароли, authorization headers и полный DSN логировать запрещено.

#### FR-LOG-004

Audit log является отдельным доменным журналом, а не заменяется обычными
application logs.

## 9. Нефункциональные требования

### NFR-001: корректность

- Денежные расчёты детерминированы.
- Округление тестируется на границах tick size и lot size.
- Ни один signal не может миновать risk manager.
- Ни один разрешённый ордер не создаётся без персистентного intent.

### NFR-002: надёжность

- Перезапуск процесса не должен приводить к дублированию ордеров.
- Повторная доставка execution event не должна дублировать fill.
- Временная недоступность market data вызывает reconnect, а не потерю процесса.
- Критическая ошибка переводит затронутый scope в безопасное состояние.

### NFR-003: производительность

Для MVP целевые значения:

- обработка market event внутри процесса без сетевых операций: p95 < 50 ms;
- обновление web dashboard: не реже одного раза в секунду;
- обновление TUI: целевой диапазон 4–10 FPS, без перерисовки на каждый tick;
- bounded memory при длительной работе;
- отсутствие неограниченных каналов и коллекций.

Эти значения не являются HFT SLA.

Backtest должен обрабатывать данные потоково и не загружать dataset целиком в
память. Допускается хранить только окно, требуемое стратегией, текущие позиции,
orders, executions и агрегаты отчёта. Большие detail artifacts записываются
потоково.

### NFR-004: безопасность

- Live trading выключен по умолчанию.
- Секреты читаются из environment.
- Web server слушает loopback по умолчанию.
- Все mutating web endpoints авторизованы при внешнем bind.
- Конфигурация и логи не содержат credentials.
- Ошибка web UI не должна влиять на торговый engine.

### NFR-005: наблюдаемость

Должны быть доступны:

- health/readiness;
- статус каждого exchange connection;
- status каждого strategy worker;
- время последнего market event;
- время последней успешной reconciliation;
- текущая глубина внутренних очередей;
- число reconnects и rejected signals;
- причина pause/blocked/failed.

### NFR-006: сопровождаемость

- Пакеты имеют одно назначение.
- Domain не импортирует UI, SQL или exchange SDK.
- Публичные интерфейсы минимальны.
- Не создавать интерфейс на каждую структуру без необходимости тестирования или
  смены адаптера.
- Сложная бизнес-логика покрывается table-driven tests.

### NFR-007: переносимость

- Поддерживаются Linux и macOS.
- Сборка выполняется стандартным Go toolchain.
- SQLite-драйвер выбирается с учётом требования одного бинарника и политики
  CGO. Решение фиксируется в ADR до реализации storage.
- Пути в конфигурации обрабатываются кроссплатформенно.

### NFR-008: база данных

- Все schema changes выполняются миграциями.
- Миграции применяются последовательно и идемпотентно в части проверки версии.
- Автоматический destructive rollback в production не выполняется.
- SQLite transactions должны быть короткими.
- PostgreSQL portability проверяется repository contract, а не универсальностью
  SQL-текста.

### NFR-009: тестируемость

- Clock, ID generator и exchange adapter заменяемы в тестах.
- Для engine используется fake exchange.
- Для storage — реальная временная SQLite DB, а не mock SQL.
- Race-sensitive код проверяется с `go test -race`.
- Backtest использует golden datasets с результатом, вычисленным вручную.
- Тесты обязаны обнаруживать исполнение по данным из будущей свечи.

### NFR-010: воспроизводимость backtest

- Dataset идентифицируется cryptographic checksum.
- Config нормализуется и идентифицируется checksum.
- Любая случайность получает явный seed.
- Основной candle fill model не должен зависеть от случайности.
- Параллелизм не должен менять порядок событий либо результат.
- Версия report schema сохраняется в каждом artifact.
- Warnings и assumptions являются частью результата, а не только логов.

### NFR-011: документация

Обязательны:

- README с quick start;
- полный example config;
- описание каждой команды CLI;
- описание risk semantics;
- инструкция sandbox;
- инструкция восстановления после blocked state;
- ADR для значимых архитектурных решений.

## 10. Референсная конфигурация

```yaml
version: 1

database:
  driver: sqlite
  dsn: "./lazytrade.db"

logging:
  level: info
  format: json
  output: stderr

exchanges:
  tinvest-main:
    type: tinvest
    token_env: TINVEST_SANDBOX_TOKEN
    account_id_env: TINVEST_ACCOUNT_ID
    ca_cert_path: "./misc/certs/russiantrustedca"
    sandbox: true
    allow_live_trading: false

agent:
  web:
    listen: "127.0.0.1:8080"
    auth_token_env: LAZYTRADE_WEB_TOKEN

  emergency_stop: false

  strategies:
    - id: sber-ma
      exchange: tinvest-main
      instrument: BBG004730N88

      strategy:
        type: moving_average_cross
        params:
          candle_interval: 1m
          fast_period: 10
          slow_period: 30

      execution:
        quantity: "1"
        order_type: market

      trading_day:
        timezone: Europe/Moscow
        reset_at: "00:00"

      risk:
        max_daily_loss:
          amount: "1000"
          asset: RUB
          pnl: total
          action: pause

        max_position_value:
          amount: "15000"
          asset: RUB

terminal:
  refresh_interval: 250ms

  tabs:
    - title: SBER
      exchange: tinvest-main
      instrument: BBG004730N88

      panels:
        - type: chart
          mode: candles
          interval: 1m
          history: 120
          overlays:
            - type: sma
              period: 10
            - type: sma
              period: 30

        - type: order_book
          depth: 10

        - type: trades
          limit: 30

backtest:
  runs:
    - id: sber-ma-2025
      strategy: sber-ma

      data:
        type: csv
        path: "./data/sber-1m-2025.csv"
        interval: 1m
        timezone: Europe/Moscow
        gap_policy: fail

      execution:
        initial_cash:
          amount: "100000"
          asset: RUB
        commission:
          type: percent
          value: "0.03"
        slippage:
          type: basis_points
          value: "5"
        market_fill: next_open
        limit_fill: touch

      output:
        directory: "./backtests/sber-ma-2025"
        json: true
        trades_csv: true
```

Точная схема должна быть закреплена тестами декодирования и
`configs/example.yaml`.

## 11. Модель данных SQLite

Ниже указана логическая, а не окончательная физическая схема.

### `strategy_instances`

- `id`;
- `exchange_account_id`;
- `instrument_id`;
- `strategy_type`;
- `config_hash`;
- `created_at`;
- `updated_at`.

### `strategy_states`

- `strategy_id`;
- `state_version`;
- `state_payload`;
- `runtime_status`;
- `status_reason`;
- `updated_at`.

### `order_intents`

- `id`;
- `signal_id`;
- `strategy_id`;
- `client_order_id`;
- `side`;
- `order_type`;
- `quantity`;
- `limit_price`;
- `asset`;
- `status`;
- `created_at`;
- `updated_at`.

### `orders`

- `id`;
- `order_intent_id`;
- `exchange_account_id`;
- `exchange_order_id`;
- `status`;
- `requested_quantity`;
- `filled_quantity`;
- `average_price`;
- `created_at`;
- `updated_at`.

### `order_executions`

- `id`;
- `exchange_account_id`;
- `exchange_execution_id`;
- `order_id`;
- `quantity`;
- `price`;
- `commission_amount`;
- `commission_asset`;
- `executed_at`;
- `received_at`.

### `execution_inbox`

- `id`;
- `exchange_account_id`;
- `source_family`;
- `dedupe_key`;
- `payload_checksum`;
- `payload`;
- `trading_day`;
- `status` (`pending` или `applied`);
- `received_at`;
- `applied_at`.

### `positions`

- `strategy_id`;
- `instrument_id`;
- `quantity`;
- `average_price`;
- `valuation_asset`;
- `updated_at`.

### `equity_snapshots`

- `id`;
- `strategy_id`;
- `trading_day`;
- `equity_amount`;
- `asset`;
- `snapshot_type`;
- `captured_at`.

### `pnl_events`

- `id`;
- `strategy_id`;
- `event_type`;
- `amount`;
- `asset`;
- `source_execution_id`;
- `occurred_at`.

### `control_states`

- `scope_type`;
- `scope_id`;
- `state`;
- `reason`;
- `updated_at`.

### `daily_statistics`

- `strategy_id`;
- `trading_day`;
- `asset`;
- realized/unrealized/total P&L;
- commissions;
- funding;
- counters;
- completeness status;
- `updated_at`.

### `audit_events`

- `id`;
- `event_type`;
- `actor`;
- `scope_type`;
- `scope_id`;
- `payload`;
- `created_at`.

### `backtest_runs`

- `id`;
- `configured_run_id`;
- `strategy_id`;
- `application_version`;
- `config_hash`;
- `dataset_checksum`;
- `seed`;
- `status`;
- `started_at`;
- `finished_at`;
- `metrics_payload`;
- `warnings_payload`;
- `error`.

### `backtest_artifacts`

- `id`;
- `backtest_run_id`;
- `artifact_type`;
- `path`;
- `checksum`;
- `size_bytes`;
- `created_at`.

## 12. Состояния и переходы стратегии

Допустимые переходы:

```text
startup       → reconciling
reconciling   → running
reconciling   → blocked
running       → user_paused
running       → risk_paused
running       → failed
running       → blocked
running       → stopped       при гарантированно штатной остановке процесса
user_paused   → running
risk_paused   → stopped       при гарантированно штатной остановке процесса
user_paused   → stopped       при гарантированно штатной остановке процесса
risk_paused   → running       только explicit resume
stopped       → reconciling    при следующем запуске
failed        → reconciling    после устранения ошибки/retry
blocked       → reconciling    после explicit reconcile
```

Недопустимые переходы должны возвращать conflict error и не менять состояние.

`emergency_stop`, exchange pause и instrument pause являются внешними control
gates. Они могут блокировать исполнение даже при локальном статусе стратегии
`running`.

## 13. План реализации

Каждый этап должен завершаться рабочим состоянием main branch, тестами и
обновлением документации.

### Этап 0. Bootstrap и архитектурные решения

Задачи:

1. Инициализировать Go module.
2. Добавить базовую структуру каталогов.
3. Настроить formatter, vet, tests и race tests.
4. Выбрать CLI library.
5. Выбрать decimal library.
6. Выбрать SQLite driver и зафиксировать CGO-решение.
7. Добавить ADR:
   - modular monolith;
   - exchange ports/adapters;
   - SQLite now/PostgreSQL later;
   - decimal representation;
   - `ntcharts` behind internal interface.
8. Добавить CI.

Критерии приёмки:

- `go test ./...` выполняется;
- бинарник собирается;
- `lazytrade version` работает;
- архитектурные решения документированы.

### Этап 1. CLI и конфигурация

Задачи:

1. Реализовать root command и подкоманды-заглушки.
2. Описать Go-структуры конфигурации.
3. Реализовать strict YAML decoding.
4. Реализовать environment secret resolution.
5. Реализовать статическую валидацию.
6. Добавить `config validate`.
7. Создать `configs/example.yaml`.
8. Добавить unit tests и golden tests ошибок.

Критерии приёмки:

- валидный example config принимается;
- неизвестное поле отклоняется;
- секреты не печатаются;
- ошибки содержат путь поля;
- agent не начинает подключение при ошибке.

### Этап 2. Domain model и storage contracts

Задачи:

1. Реализовать ID, Money, decimal quantity, timestamps.
2. Реализовать основные domain enums и validation.
3. Описать repository interfaces.
4. Описать transaction boundary.
5. Создать storage contract test suite.
6. Создать fake Clock и ID generator для тестов.

Критерии приёмки:

- нельзя сложить Money разных asset;
- domain не импортирует SQL и SDK;
- repository API покрывает pipeline order intent → execution → P&L.

### Этап 3. SQLite и sqlc

Задачи:

1. Создать migrations.
2. Создать sqlc queries.
3. Настроить `sqlc.sqlite.yaml`.
4. Реализовать SQLite Store и mapper.
5. Включить pragmas.
6. Реализовать transaction support.
7. Реализовать single-agent lock.
8. Добавить `db migrate`.
9. Запустить storage contract tests на временной БД.

Критерии приёмки:

- миграции применяются на пустой БД;
- повторный запуск не повреждает схему;
- уникальные ограничения обеспечивают идемпотентность;
- rollback транзакции проверен;
- второй lock получить нельзя.

### Этап 4. Exchange abstraction и fake exchange

Задачи:

1. Реализовать exchange interfaces и capabilities.
2. Реализовать нормализованные market/order модели.
3. Реализовать классификацию ошибок.
4. Создать fake exchange со сценариями:
   - success;
   - partial fill;
   - duplicate event;
   - transient error;
   - unknown outcome;
   - disconnect/reconnect.
5. Добавить contract tests адаптера.

Критерии приёмки:

- fake exchange позволяет тестировать engine без сети;
- SDK-типы не попадают в domain;
- duplicate execution можно воспроизвести тестом.

### Этап 5. T-Invest read-only adapter

Задачи:

1. Реализовать credentials и client lifecycle.
2. Получать instrument metadata.
3. Получать portfolio snapshot.
4. Подписываться на необходимые market data.
5. Реализовать mapper цен и quantities.
6. Реализовать reconnect/backoff.
7. Добавить sandbox integration tests, запускаемые при наличии env vars.
8. Добавить recorded/fake fixtures для обычного CI.

Критерии приёмки:

- можно получить выбранный инструмент;
- market events приходят в нормализованном формате;
- после тестового disconnect подписка восстанавливается;
- отсутствие credentials даёт понятную ошибку.

### Этап 6. Read-only terminal

Задачи:

1. Реализовать Bubble Tea app model.
2. Реализовать tabs/layout/status bar/help.
3. Реализовать dispatcher market events.
4. Реализовать chart abstraction.
5. Подключить `ntcharts`.
6. Реализовать line, sparkline и candles.
7. Реализовать order book и recent trades.
8. Реализовать resize, viewport и zoom.
9. Ограничить частоту перерисовки.
10. Добавить model tests и snapshot/golden tests View.

Критерии приёмки:

- терминал отображает real-time sandbox data;
- UI не содержит mutating trading paths;
- resize не вызывает panic;
- медленный render не блокирует market stream;
- `ntcharts` не импортируется вне chart adapter.

### Этап 7. Strategy runtime

Задачи:

1. Реализовать registry strategy types.
2. Реализовать worker lifecycle.
3. Реализовать последовательную очередь событий worker.
4. Реализовать versioned state persistence.
5. Реализовать moving average cross.
6. Реализовать signal IDs и дедупликацию.
7. Реализовать pause gates без исполнения ордеров.

Критерии приёмки:

- стратегия детерминированно выдаёт signal на фиксированном наборе candles;
- после restart состояние восстанавливается;
- один worker failure не завершает другой;
- paused worker не создаёт signals для исполнения.

### Этап 8. Risk manager и P&L

Задачи:

1. Реализовать max position value.
2. Реализовать realized и total P&L.
3. Реализовать trading-day boundary через Clock.
4. Сохранять start-of-day snapshot.
5. Реализовать max daily loss per strategy.
6. Проверять совпадение asset.
7. Реализовать state machine `risk_paused`.
8. Реализовать явный resume.
9. Добавить тесты границ, комиссий, funding и смены дня.

Критерии приёмки:

- лимит одной стратегии не влияет на другую;
- разные asset никогда не агрегируются;
- ровно на границе лимита стратегия ставится на pause;
- restart не сбрасывает дневной убыток;
- новый день не выполняет automatic resume.

### Этап 8A. Backtesting engine

Этот этап выполняется после Strategy runtime и Risk manager, но до разрешения
live/sandbox order execution. Он должен подтвердить, что стратегия и risk
pipeline не зависят от сетевой биржи.

Задачи:

1. Реализовать virtual Clock.
2. Реализовать `MarketEventSource` для исторических данных.
3. Реализовать строгий CSV OHLCV reader и validator.
4. Рассчитывать checksum dataset.
5. Реализовать deterministic event ordering.
6. Реализовать simulated broker.
7. Реализовать next-open market fill.
8. Реализовать touch-based limit fill с консервативной ambiguous-bar policy.
9. Реализовать commission и slippage models.
10. Подключить общие Strategy, Risk manager и P&L.
11. Реализовать начальный cash/equity и simulated positions.
12. Реализовать metrics и drawdown.
13. Реализовать JSON report и trades CSV.
14. Сохранять run metadata и artifacts.
15. Добавить `backtest` и `backtest validate-data`.
16. Добавить маленькие hand-calculated golden datasets.
17. Добавить тест на look-ahead bias.
18. Добавить reproducibility test.
19. Добавить cancellation handling.

Критерии приёмки:

- live и backtest используют один strategy type;
- signal по close свечи не исполняется на этой же свече;
- на hand-calculated dataset orders, fills, P&L и drawdown совпадают с
  ожидаемыми значениями;
- одинаковый запуск воспроизводит идентичный отчёт без учёта служебных ID;
- max daily loss срабатывает в виртуальной timezone;
- разные asset не агрегируются;
- backtest не создаёт ни одного сетевого торгового запроса;
- invalid либо unordered dataset отклоняется до запуска стратегии;
- report содержит config hash, dataset checksum и assumptions.

### Этап 9. Order execution

Задачи:

1. Реализовать pipeline Signal → risk decision.
2. Сохранять OrderIntent до API call.
3. Реализовать client order ID.
4. Реализовать PlaceOrder/CancelOrder/GetOrder в T-Invest sandbox.
5. Сохранять orders и partial fills.
6. Обновлять positions и P&L.
7. Обрабатывать duplicate execution.
8. Обрабатывать unknown outcome без повторного ордера.
9. Сохранять входящие fills в durable execution inbox до обновления проекций.
10. Повторно применять pending inbox при startup.
11. Реализовать отмену opening orders при risk pause.

Критерии приёмки:

- crash после сохранения intent не создаёт дубль после restart;
- partial fills учитываются один раз;
- rejected signal имеет сохранённую причину;
- production trading остаётся выключенным.

### Этап 10. Reconciliation

Задачи:

1. Реализовать startup orchestration.
2. Сверять open orders.
3. Сверять positions.
4. Отправлять `ready` intents и разрешать `submitting`/`unknown` через lookup.
5. Восстанавливать доступные executions.
6. Реализовать blocked state и причины.
7. Реализовать manual reconcile.
8. Добавить crash-point integration tests.

Критерии приёмки:

- strategies не переходят в running до reconciliation;
- повторный reconcile идемпотентен;
- неизвестная позиция блокирует только релевантный scope;
- причина видна в логах и application status.

### Этап 11. Web API и dashboard

Задачи:

1. Описать OpenAPI.
2. Реализовать application query services.
3. Реализовать REST handlers.
4. Реализовать SSE hub с bounded queues.
5. Реализовать snapshot after reconnect.
6. Реализовать dashboard:
   - status;
   - strategies;
   - P&L и лимиты;
   - orders/executions;
   - pause/resume;
   - emergency stop.
7. Встроить static assets.
8. Реализовать auth policy.
9. Добавить handler и browser-level tests.

Критерии приёмки:

- UI обновляется без перезагрузки;
- asset показан у каждой суммы;
- pause/resume фиксируются в audit;
- внешний bind без auth запрещён;
- отключённый web client не тормозит engine.

### Этап 12. Hardening и выпуск sandbox MVP

Задачи:

1. Провести длительный sandbox soak test.
2. Проверить reconnect при сетевых сбоях.
3. Проверить recovery после принудительного завершения процесса в разных
   точках order pipeline.
4. Запустить race detector.
5. Добавить resource/queue metrics.
6. Проверить graceful shutdown.
7. Подготовить README, operations guide и troubleshooting.
8. Собрать бинарники для поддерживаемых платформ.
9. Зафиксировать ограничения MVP.

Критерии приёмки:

- нет известных дублирующихся ордеров при crash tests;
- память не растёт неограниченно в soak test;
- graceful shutdown сохраняет состояние;
- sandbox сценарий воспроизводится по README;
- live trading всё ещё требует явного отдельного разрешения.

### Этап 13. PostgreSQL adapter — будущий этап

Не выполнять до появления реальной потребности.

Задачи:

1. Создать `db/postgres/migrations` и `queries`.
2. Добавить отдельный sqlc config с `pgx/v5`.
3. Реализовать PostgreSQL Store.
4. Запустить общий storage contract suite.
5. Реализовать session advisory lock как защиту от второго агента.
6. Добавить команду controlled copy/import из SQLite.
7. Документировать остановку агента, перенос, verification и reconciliation.

Ограничение сохраняется: одна PostgreSQL database обслуживает один agent.

## 14. Стратегия тестирования

### Unit tests

Обязательны для:

- Money и decimal operations;
- config validation;
- strategy calculations;
- risk state machine;
- P&L;
- trading-day boundaries;
- error classification;
- chart data transformation.
- virtual Clock и порядок historical events;
- candle fill model;
- commission/slippage;
- drawdown и backtest metrics;

### Integration tests

Обязательны для:

- SQLite repositories;
- migrations;
- transaction rollback;
- engine + fake exchange;
- duplicate fills;
- unknown order outcome;
- reconciliation;
- HTTP handlers и SSE.
- backtest runner + SQLite metadata/artifacts;
- reproducibility на golden dataset;
- look-ahead bias guard.

### Sandbox tests

Должны иметь build tag или отдельную команду и запускаться только при наличии
credentials. Они не должны быть обязательны для обычного CI.

Для локальных T-Invest sandbox tests токен хранится в environment variable
`TINVEST_SANDBOX_TOKEN`. Тесты ожидают, что переменная уже присутствует в
окружении процесса.

Если в текущей интерактивной shell-сессии переменная ещё не загружена и нужно
проверить её наличие либо запустить тест, окружение можно однократно подхватить:

```zsh
source ~/.zshrc.private
```

Выполнять `source` перед каждым тестом не требуется. Разрешено проверять только
факт наличия переменной. Значение токена запрещено выводить в stdout/stderr,
логи, отчёты тестов или shell trace. Нельзя включать `set -x` для команд,
работающих с sandbox credentials.

### Crash tests

Нужно эмулировать остановку:

- до сохранения intent;
- после сохранения intent, до API call;
- во время API call с unknown outcome;
- после ответа биржи, до сохранения order;
- после fill, до обновления P&L.

После restart система должна прийти к одному корректному состоянию без дублей.

### Race tests

Как минимум:

```text
go test -race ./...
```

Особое внимание:

- market dispatcher;
- SSE hub;
- worker lifecycle;
- shutdown;
- storage transaction wrapper.

## 15. Правила работы агентов

Каждый агент, реализующий задачу, должен:

1. Указать, какой этап и какие requirement IDs он реализует.
2. Перед изменением проверить существующую реализацию и тесты.
3. Не вводить прямые зависимости domain → infrastructure.
4. Не использовать `float64` для денег.
5. Не добавлять live trading по умолчанию.
6. Не изменять семантику daily P&L без обновления этого ТЗ.
7. Не добавлять глобальный абсолютный max daily loss.
8. Не добавлять multi-agent database coordination.
9. Не раскрывать credentials в fixture, log или error.
10. Добавить тесты, соответствующие риску изменения.
11. Выполнить formatter, unit tests и релевантные integration tests.
12. В отчёте перечислить:
    - реализованные требования;
    - изменённые файлы;
    - выполненные проверки;
    - известные ограничения;
    - следующий рекомендуемый этап.

Если требование невозможно реализовать без изменения принятой архитектуры,
исполнитель обязан остановиться и описать конфликт, а не принимать новое
решение молча.

## 16. Definition of Done для любой задачи

Задача считается завершённой, когда:

- код компилируется;
- публичное поведение соответствует requirement IDs;
- добавлены позитивные и негативные тесты;
- ошибки содержат полезный контекст;
- секреты не появляются в выводе;
- нет необоснованных TODO в критическом пути;
- документация и example config обновлены;
- formatter и tests проходят;
- изменение не расширяет scope незаметно;
- агент предоставил краткий handoff.

## 17. Критерии готовности MVP

MVP считается готовым, если пользователь может:

1. Создать конфигурацию по примеру.
2. Проверить её через `config validate`.
3. Запустить T-Invest sandbox.
4. Открыть read-only terminal и увидеть real-time данные.
5. Провалидировать локальный OHLCV dataset.
6. Запустить backtest той же стратегии без подключения к торговому API.
7. Получить воспроизводимый JSON-отчёт и список simulated trades.
8. Увидеть P&L, комиссии, drawdown и risk events backtest.
9. Запустить одну или несколько стратегий в sandbox.
10. Увидеть созданные sandbox orders и fills.
11. Увидеть отдельный P&L каждой стратегии с указанием asset.
12. Настроить отдельный max daily loss каждой стратегии.
13. Убедиться, что достигшая лимита стратегия перешла в `risk_paused`.
14. Вручную выполнить resume.
15. Поставить на pause exchange, instrument или strategy через web UI.
16. Перезапустить агент без потери состояния и дублирования ордеров.
17. Получить понятный blocked state при неразрешимом расхождении.
18. Корректно остановить приложение через SIGINT/SIGTERM.

## 18. Открытые решения, которые нужно принять перед соответствующим этапом

Эти вопросы не блокируют написание ТЗ, но должны быть закреплены ADR до
реализации зависимого этапа:

1. CLI library: Cobra либо минимальная стандартная реализация.
2. Decimal library.
3. SQLite driver и допустимость CGO.
4. Конкретный способ SQLite single-agent lock.
5. Формула attribution общей позиции exchange account к стратегии, если на
   одном инструменте позднее разрешат несколько стратегий.
6. Поддерживаемый набор T-Invest market-data subscriptions.
7. Политика отмены частично исполненного opening order при risk pause.
8. Источник authoritative unrealized P&L для каждого адаптера.
9. Формат долгосрочного хранения больших historical datasets после CSV MVP.
10. Нужна ли отдельная команда загрузки истории T-Invest в первом MVP или
    достаточно импорта подготовленного CSV.
11. Нужна ли визуализация результатов backtest в web UI после MVP. Для MVP
    обязательны stdout, JSON и trades CSV; web-визуализация не требуется.

До решения пункта 5 действует ограничение: один exchange account + instrument
не должен одновременно использоваться несколькими торгующими strategy
instances. Это предотвращает неоднозначное владение позицией и P&L.

### 18.1. Отложенный этап: multi-exchange runtime одного агента

Этот этап выполняется после проверки основного контура на одном exchange и не
входит в sandbox MVP. Здесь `agent` означает один процесс приложения; этап не
требует запуска нескольких процессов и не отменяет SQLite single-agent policy.

Функциональные требования:

1. Создавать независимый adapter/runtime scope для каждого используемого
   `exchange account ID`.
2. Маршрутизировать strategy worker, market subscriptions, orders, execution
   stream и history recovery строго по ссылке стратегии на exchange account.
3. Поддерживать одновременную работу стратегий, использующих разные exchange
   types, например T-Invest и Bybit.
4. Выполнять startup reconciliation и хранить history checkpoint отдельно для
   каждого exchange account.
5. Изолировать account-level `blocked`, reconnect и rate limiting: отказ одной
   биржи не должен останавливать стратегии других бирж, если общая
   инфраструктура исправна.
6. Сохранять strategy-level risk limits в native asset стратегии; суммы разных
   бирж и валют не агрегируются без явно настроенного valuation/conversion
   policy.
7. Обеспечить отдельные pause/resume/emergency-stop scopes для strategy,
   exchange account и всего процесса.
8. Показывать exchange type и exchange account рядом с каждой стратегией,
   заявкой, сделкой, позицией и причиной blocked state в CLI/API/UI.

Нефункциональные требования:

- один медленный adapter не блокирует event loop других exchange scopes;
- очереди, reconnect backoff и API rate limits изолированы по adapter/account;
- одинаковые внешние order/trade IDs разных бирж не конфликтуют: ключи
  хранения всегда включают exchange account/source scope;
- shutdown останавливает все scopes и ждёт завершения их durable writes;
- тесты используют минимум два fake adapters и доказывают корректную
  маршрутизацию, изоляцию отказа и отсутствие cross-account attribution;
- публичные domain/application контракты не зависят от SDK конкретной биржи;
- архитектура остаётся совместимой с будущим PostgreSQL, но coordination
  нескольких одновременно работающих процессов по-прежнему не требуется.

Критерии приёмки:

- две стратегии на разных fake exchanges одновременно получают только свои
  market events и executions;
- order каждой стратегии отправляется ровно в назначенный adapter/account;
- disconnect или reconciliation mismatch одного exchange scope переводит в
  blocked только связанные стратегии;
- restart независимо восстанавливает intents, inbox и history checkpoint
  каждого exchange account;
- `go test -race` не обнаруживает гонок в multi-exchange dispatcher и shutdown.

## 19. Отложенный этап: каталог торговых стратегий

Расширение набора strategy types выполняется только после того, как основной
торговый контур прошёл integration, restart/recovery, risk, reconciliation,
execution-stream и продолжительные sandbox-тесты. Добавление новых стратегий
не должно отвлекать от устранения дефектов общего runtime.

После подтверждения основного функционала необходимо добавить как минимум:

1. `buy_and_hold` как контрольный benchmark для backtest.
2. `periodic_investment` для регулярной покупки заданного набора инструментов
   по календарному расписанию (DCA).
3. `rsi_mean_reversion` как первую стратегию возврата к среднему.
4. `donchian_breakout` либо эквивалентную breakout-стратегию.
5. `bollinger_reversion` для проверки rolling variance и band signals.

Каждый новый strategy type обязан использовать существующие `Strategy`,
`Worker`, signal, risk и backtest contracts без специальных веток live/backtest.
Для него обязательны deterministic fixtures, state-version tests, отсутствие
look-ahead, backtest report и sandbox-compatible configuration validation.

### Требования к `periodic_investment`

Стратегия предназначена для накопления позиции без условий технического
анализа. Например: раз в месяц купить SBER и облигацию на заданную сумму.

Конфигурация должна задавать:

- timezone и расписание: daily, weekly либо monthly;
- локальное время запуска;
- правило календарного дня месяца;
- список инструментов и целевые веса либо отдельную сумму на каждый инструмент;
- режим sizing: фиксированное количество, фиксированная сумма в settlement
  asset либо распределение общего бюджета по весам;
- максимальный бюджет одного периода;
- поведение при недостатке средств: skip, partial allocation либо fail;
- допустимое отклонение фактической суммы из-за размера лота;
- политику нерабочего дня и закрытого рынка: следующий доступный торговый день,
  предыдущий торговый день либо skip;
- разрешённое временное окно исполнения и максимальное число retry;
- только покупку либо опциональную периодическую rebalance-политику.

Каждый запланированный период получает детерминированный `schedule occurrence
ID`, включающий strategy ID и календарный период. Один occurrence может создать
не более одного intent на каждый настроенный инструмент. Restart, повторный
timer tick и reconnect не должны создавать повторную покупку.

До размещения ордеров стратегия обязана:

1. получить актуальные instrument metadata, lot size и settlement asset;
2. проверить торговый статус инструмента;
3. преобразовать бюджет в целое количество лотов без превышения бюджета;
4. применить per-strategy risk и position limits;
5. атомарно сохранить occurrence, signals и intents;
6. разместить ордера через общий execution/reconciliation pipeline.

Пропущенный occurrence не исполняется задним числом без явно настроенной
catch-up policy. По умолчанию допускается один catch-up в пределах текущего
календарного периода; покупки за несколько прошлых месяцев не объединяются.

Для этой стратегии runtime должен поддерживать календарные события независимо
от market candle stream. Backtest обязан использовать тот же scheduler и
биржевой календарь, детерминированное округление до лотов, комиссии и выбранную
политику нерабочих дней.

### Реализованный вертикальный срез `periodic_investment` v1

По явному изменению приоритета до завершения sandbox MVP реализована ограниченная
версия DCA, использующая общий `Strategy → Worker → signal → risk → intent`
pipeline без специальных торговых веток:

- одна strategy instance управляет одним инструментом;
- поддерживается monthly schedule с `day_of_month` от 1 до 28, локальным
  временем `HH:MM` и IANA timezone;
- размер задаётся фиксированным `execution.quantity`, тип заявки — market;
- occurrence срабатывает на первой complete candle требуемого interval, чей
  `Candle.End` не раньше планового локального времени;
- закрытый рынок естественно переносит срабатывание до первой доступной свечи;
- catch-up ограничен текущим календарным месяцем, старые месяцы не догоняются;
- durable state хранит `last_occurrence=YYYY-MM`; state и signal сохраняются
  атомарно, поэтому повторный event и restart не создают вторую покупку;
- live runtime и backtest создают стратегию через один built-in builder;
- live candle cursor нормализован на `Candle.End`, как в backtest.

Ограничения v1, которые не следует считать выполнением полного контракта выше:

- нет daily/weekly schedule и независимого calendar event source;
- нет общего бюджета, денежных allocations, weights и basket из нескольких
  инструментов; для нескольких инструментов пока нужны отдельные instances;
- нет явного биржевого календаря и configurable closed-market policy;
- нет budget-to-lot sizing, проверки доступного cash и rebalancing;
- две стратегии всё ещё нельзя привязать к одному инструменту одного exchange,
  потому что runtime маршрутизирует `instrument → worker` однозначно.

Полная версия требует `ScheduleEvent`, trading calendar, occurrence ledger и
multi-instrument allocation/sizing. Эти расширения остаются после проверки
основного sandbox-контура; v1 уже пригодна для регулярной покупки фиксированного
количества одного инструмента и для детерминированного backtest.
