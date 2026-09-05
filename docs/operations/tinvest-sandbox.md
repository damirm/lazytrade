# T-Invest sandbox

## SDK и режим

- Используется `opensource.tbank.ru/invest/invest-go`, сейчас версия v1.48.0.
- Endpoint жёстко ограничен sandbox:
  `sandbox-invest-public-api.tbank.ru:443`.
- Любой другой endpoint отклоняется как production; live trading не разрешён.
- TLS может дополнять системный trust store сертификатами из файла или каталога
  через `ca_cert_path`. В репозитории сертификаты лежат в
  `misc/certs/russiantrustedca/`.

Не возвращать проект к `invest-api-go-sdk`: это отменённое решение.

## Account ID

В конфигурации exchange имеет логический ID, например `tinvest-main`. Это ID
adapter scope внутри lazytrade. `account_id_env` указывает переменную с UUID
реального sandbox broker account, полученного через account list/create.

В domain/runtime передаётся логический ID. T-Invest adapter валидирует его и
сам отправляет broker UUID в API. Portfolio ID из внешнего UI нельзя молча
считать account ID без подтверждения через sandbox accounts API.

## Credentials

Пример:

```yaml
exchanges:
  tinvest-main:
    type: tinvest
    token_env: TINVEST_SANDBOX_TOKEN
    account_id_env: TINVEST_ACCOUNT_ID
    ca_cert_path: ../misc/certs/russiantrustedca
    sandbox: true
    allow_live_trading: false
```

CLI загружает `.env` из текущего рабочего каталога. Не выводить содержимое
token/account variables. Историческая договорённость для ручной проверки token
в текущей shell session: сначала выполнить `source ~/.zshrc.private`. Это не
нужно делать перед каждым тестом и не следует встраивать в программу/scripts.

## Диагностические команды

После сборки `./lazytrade` доступны:

```text
config validate --config <file>
account list --config <file> --exchange <id>
account create --config <file> --exchange <id> --name <name>
account pay-in --config <file> --exchange <id> --amount <rub>
account smoke-test --config <file> --exchange <id> --instrument <id> \
  --quantity <units> --confirm
agent preflight --config <file>
agent history-probe --config <file> ...
data download --config <file> --exchange <id> --instrument <id> ...
```

Перед mutating командой проверить её `--help`; smoke-test требует явный
`--confirm` и должен выполняться минимальным количеством во время открытого
рынка.

## History recovery

- Источник — `GetSandboxOperationsByCursor`, затем bridge operation ID в
  `GetSandboxOrderState`.
- Runtime bootstrap window: 24 часа.
- Overlap: 15 минут.
- Visibility delay: 5 минут.
- Scan использует фиксированные UTC `from/to`; checkpoint продвигается только
  после полного успешного применения.
- История фильтруется локально до BUY/SELL EXECUTED, поскольку server filters
  в sandbox ранее провоцировали нестабильный `70001`.

Реальные наблюдения 2 августа 2026 года:

- cursor request с `to` непосредственно равным now мог вернуть `70001`; fixed
  past window работал;
- старый operation ID при `GetSandboxOrderState` возвращал `50005 NotFound`;
- read-only account/portfolio calls эпизодически возвращали `70001`, повтор
  account list затем был успешен;
- fresh bridge не проверен: рынок был закрыт, PostOrder вернул `30079`, и
  заявка не была создана.

Нельзя объявлять history bridge рабочим для production, пока свежий order не
проверен в торговую сессию. `NotFound` в recovery считается неразрешимой
историей, а не отсутствием исполнения.

## Retry policy

- Read-only unary calls: максимум 3 попытки для transient/rate-limit.
- Execution-stream enrichment через GetOrderState: максимум 2 попытки, чтобы
  не блокировать receiver надолго.
- Stream reconnect не оборачивается в unary retry helper.
- Mutations всегда one-shot; unknown outcome разрешается lookup/reconciliation.

