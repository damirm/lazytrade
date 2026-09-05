# Стратегии и backtest

## Общий контракт

Стратегия не вызывает exchange и storage напрямую. Она принимает
нормализованный market event и versioned state, затем возвращает новое state и
signal drafts. Worker:

- сериализует обработку событий одной стратегии;
- отклоняет out-of-order cursor;
- подавляет повтор того же cursor;
- строит детерминированные signal IDs;
- атомарно сохраняет state и signals.

Live и backtest обязаны использовать тот же strategy type. Специальные ветки
торгового pipeline под конкретную стратегию запрещены.

## moving_average_cross

Параметры:

- `candle_interval`;
- `fast_period`;
- `slow_period`, строго больше fast;
- `execution.quantity` и order type.

Стратегия хранит rolling closes и предыдущую связь fast/slow average. Сигнал
возникает только при пересечении после warmup.

## periodic_investment v1

Это реализованный ограниченный DCA-срез:

```yaml
strategy:
  type: periodic_investment
  params:
    candle_interval: 1m
    day_of_month: 10
    time: "11:00"
    timezone: Europe/Moscow
execution:
  quantity: "1"
  order_type: market
```

Семантика:

- одна instance покупает один instrument;
- monthly schedule, день только 1–28;
- фиксированное количество единиц, market order;
- due определяется по `Candle.End` в настроенной timezone;
- signal создаётся на первой complete candle не раньше due time;
- при закрытом рынке это естественно означает первую последующую свечу;
- только один occurrence текущего месяца (`YYYY-MM`);
- старые пропущенные месяцы не догоняются;
- `last_occurrence` хранится в versioned durable state, поэтому restart и
  несколько свечей одного месяца не создают повторную покупку.

Live T-Invest candle event timestamp нормализован к `Candle.End`, как и CSV
backtest. Это важно для одинакового causative cursor и отсутствия look-ahead.

Ограничения DCA v1:

- нет basket/weights/shared budget;
- нет sizing из суммы денег и округления по lot;
- нет daily/weekly schedule;
- нет отдельного calendar event source/trading calendar;
- нет configurable skip/next/previous trading-day policy;
- нет available-cash allocation и rebalance;
- несколько инструментов моделируются отдельными strategy instances.

Полная версия потребует `ScheduleEvent`, occurrence ledger/items и чистого
budget-to-lot sizing. Не пытаться скрытно реализовать это через fake market
events или менять identity occurrence при переносе торгового дня.

## Backtest data

- Источник MVP — строгий CSV OHLCV плюс metadata manifest.
- Dataset содержит exchange/instrument, interval, timezone, tick/lot size,
  checksum и gap policy.
- `data download` получает завершённые свечи T-Invest и создаёт immutable CSV
  с manifest.
- `data validate` проверяет порядок, интервалы, gaps и checksum.
- Сеть не используется во время собственно backtest.

## Execution model

- Market signal исполняется на следующем open, не на той же свече.
- Limit fill — touch-based с conservative ambiguous-bar policy.
- Комиссия и slippage конфигурируются.
- Используются virtual Clock, simulated cash/positions и общий risk pipeline.
- Результат включает JSON report, trades CSV, metrics, drawdown, assumptions,
  config hash и dataset checksum.

Backtest — модель, а не доказательство будущей доходности. Изменение fill model
или event ordering требует golden/reproducibility/no-look-ahead tests.

