# ADR-0003: официальный Go SDK для T-Invest

- Статус: принято
- Дата: 2026-07-29
- Связанный этап: 5

## Решение

Использовать официальный модуль `opensource.tbank.ru/invest/invest-go` версии
`v1.48.0`. Адаптер использует сгенерированные protobuf/gRPC clients из подпакета
`proto`; SDK-типы не выходят за пределы `internal/exchange/tinvest`.

Соединение создаётся адаптером без helper `investgo.NewClient`, чтобы открытие
read-only подключения не создавало sandbox account как побочный эффект.
Разрешён только sandbox endpoint. Токен передаётся как per-RPC credentials и не
включается в ошибки или логи.

При необходимости конфигурация `ca_cert_path` добавляет PEM и DER-сертификаты
из файла либо каталога к системному trust store. Системные корни не заменяются,
`InsecureSkipVerify` не используется.

## Последствия

- локальные proto-контракты остаются нормативным источником поведения API;
- точность `Quotation` и `MoneyValue` контролируется собственными mapper tests;
- lifecycle, deadlines, reconnect и нормализация ошибок принадлежат адаптеру;
- sandbox mutating RPC реализованы на этапе execution; production endpoint и
  live trading остаются выключенными.

## Дополнение 2026-09-05

Sandbox `PostOrder`, `CancelOrder`, account create и pay-in поддерживаются, но
никогда автоматически не повторяются после неоднозначного результата.
Read-only transient RPC используют bounded retry. Детальная текущая политика
описана в `docs/architecture/trading-runtime.md`.
