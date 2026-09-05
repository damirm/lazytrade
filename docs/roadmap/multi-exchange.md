# Multi-exchange runtime

- Статус: planned
- Горизонт: после подтверждения sandbox MVP на одном exchange
- Последнее обновление: 2026-09-05

## Долгосрочная цель

Один процесс `lazytrade agent` должен одновременно запускать стратегии на
разных биржах. Например, одна strategy instance работает через T-Invest, а
другая — через Bybit. Каждая стратегия выбирает свой exchange account в
конфигурации; несколько стратегий могут разделять один account или использовать
разные accounts и exchange types.

Это принятое продуктовое направление, но не реализованная возможность.
Текущий runtime требует, чтобы все стратегии использовали один T-Invest
sandbox exchange, и маршрутизирует worker по instrument ID.

## Требуемые свойства

1. Создавать независимый adapter/runtime scope для каждого настроенного
   exchange account.
2. Маршрутизировать market subscriptions, strategy workers, orders, execution
   streams и history recovery строго по `(exchange account, strategy)`.
3. Поддерживать разные adapter types в одном процессе, в первую очередь
   T-Invest и Bybit.
4. Хранить history checkpoint, reconnect state, rate limits и blocked reason
   отдельно для каждого exchange account.
5. Отказ или disconnect одной биржи не должен останавливать несвязанные
   стратегии другой биржи.
6. Внешние order/trade/instrument IDs всегда получают exchange/account scope;
   одинаковые IDs разных бирж не должны конфликтовать в storage.
7. Risk limits остаются per strategy и в native settlement asset. RUB, USDT,
   BTC и другие asset не агрегируются без явной valuation/conversion policy.
8. Control scopes должны поддерживать strategy, exchange account и весь
   процесс: pause/resume/emergency stop с понятным приоритетом.
9. Shutdown должен остановить все scopes и дождаться durable writes каждого из
   них.

## Архитектурное направление

Целевая композиция:

```text
agent process
  -> exchange scope: tinvest-main
       -> adapter + streams + recovery + strategies
  -> exchange scope: bybit-main
       -> adapter + streams + recovery + strategies
```

Публичные domain/application contracts не должны зависеть от SDK T-Invest или
Bybit. Для реализации потребуется dispatcher с ключом exchange account, а
текущие структуры `instrument -> worker` нужно заменить scope-aware routing.

Точный concurrency model, lifecycle supervisor и схема storage будут приняты
отдельным ADR перед реализацией. Этот roadmap не выбирает их преждевременно.

## Что не входит в требование

- Запуск нескольких процессов/bots над одной PostgreSQL database.
- Распределённый leader election.
- Автоматическая конвертация валют и общий абсолютный daily loss.
- Одновременное добавление всех перечисленных crypto exchanges.

PostgreSQL может быть добавлен независимо, но даже с PostgreSQL пока действует
ограничение: один agent process владеет своей database/runtime state.

## Предварительные критерии приёмки

- Две стратегии на разных fake exchanges получают только свои events и fills.
- Order каждой стратегии отправляется ровно в назначенный adapter/account.
- Disconnect Bybit переводит в blocked только связанные стратегии; T-Invest
  продолжает работу.
- Restart независимо восстанавливает intents, execution inbox и history
  checkpoint каждого account.
- Storage keys не конфликтуют при одинаковых external IDs разных exchanges.
- `go test -race` не выявляет гонок в dispatcher и shutdown.

## Условие начала

Этап начинается только после успешного T-Invest sandbox round trip, проверки
одного-exchange runtime и базовых crash/restart сценариев. До этого найденные
multi-exchange требования накапливаются здесь, но не подменяют текущий milestone.

