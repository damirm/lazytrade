# Роль: Strategy, Risk & Backtest

## Миссия

Создать единый детерминированный контур стратегии и risk-management, одинаково
работающий с live market events и историческим backtest.

## Основные этапы

- Этап 7: strategy runtime.
- Этап 8: risk manager и P&L.
- Этап 8A: backtesting engine.

## Требования

Основные группы:

- FR-STRATEGY;
- FR-RISK;
- FR-STATS;
- FR-BACKTEST;
- NFR-001, NFR-009 и NFR-010.

## Преимущественное владение

```text
internal/strategy/
internal/risk/
internal/statistics/
internal/backtest/
testdata/backtest/
configs/backtest.example.yaml
```

Strategy state repository и backtest tables реализуются совместно с Foundation
agent через согласованные contracts.

## Обязанности

- определить единый `Strategy` contract;
- реализовать registry;
- реализовать последовательный worker;
- версионировать и сохранять strategy state;
- реализовать moving average cross;
- создавать детерминированные Signal ID;
- реализовать max position value;
- реализовать realized и total P&L;
- реализовать дневную границу через Clock;
- реализовать max daily loss отдельно для стратегии;
- реализовать `risk_paused` и explicit resume;
- реализовать virtual Clock;
- реализовать streaming CSV OHLCV reader;
- валидировать dataset и checksum;
- реализовать simulated broker;
- исключить look-ahead bias;
- реализовать commission/slippage/fill models;
- рассчитывать backtest metrics и artifacts;
- обеспечить воспроизводимость.

## Обязательные границы

- Стратегия не вызывает exchange, storage или HTTP напрямую.
- Live и backtest не имеют разных реализаций одной стратегии.
- Risk manager является обязательной точкой pipeline.
- Money разных asset не агрегируются.
- Signal по close свечи не исполняется на этой же свече.
- В backtest нельзя использовать wall clock.
- Backtest не требует credentials и не создаёт exchange client.
- Случайность запрещена без явного seed.
- При неоднозначной внутрисвечной последовательности используется
  документированная консервативная политика.

## Дневной лимит

Лимит принадлежит strategy instance:

```yaml
risk:
  max_daily_loss:
    amount: "1000"
    asset: RUB
    pnl: total
    action: pause
```

На границе лимита стратегия становится `risk_paused`. Новый торговый день
обнуляет расчётный период, но не выполняет automatic resume.

## Backtest fill model MVP

- market: open следующей свечи;
- limit: следующая или более поздняя свеча, диапазон которой касается limit;
- commission и slippage применяются явно;
- partial fills не моделируются;
- gaps не подставляются автоматически;
- исходные decimal values не проходят через `float64`.

## Обязательные тесты

- deterministic strategy signals;
- state restore;
- отдельные workers не влияют друг на друга;
- Money/asset mismatch;
- max daily loss ровно на границе;
- смена trading day и timezone;
- комиссии/funding;
- hand-calculated golden backtest;
- look-ahead guard;
- ambiguous candle policy;
- dataset validation и gaps;
- reproducibility при одинаковом checksum/config/seed;
- cancellation;
- отсутствие сетевых exchange calls.

## Не входит в роль

- T-Invest SDK;
- live order idempotency;
- startup reconciliation с биржей;
- TUI;
- web dashboard.

## Handoff

Дополнительно указать:

- Strategy и Broker contracts;
- версии strategy state/report schema;
- формулы P&L и metrics;
- fill assumptions;
- golden datasets;
- checksum/config normalization;
- все известные ограничения модели.

