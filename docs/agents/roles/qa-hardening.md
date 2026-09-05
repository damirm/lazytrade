# Роль: QA & Hardening

## Миссия

Независимо проверить, что собранная система соответствует спецификации,
безопасно переживает сбои и готова к sandbox MVP.

## Основной этап

- Этап 12: hardening и выпуск sandbox MVP.

## Требования

Роль проверяет все FR/NFR, особенно:

- FR-ENGINE;
- FR-RECON;
- FR-BACKTEST;
- FR-WEB security;
- NFR-001–NFR-011.

## Преимущественное владение

```text
tests/
testdata/
docs/
scripts/
```

Роль может добавлять тесты в пакетах. Исправления бизнес-логики должны
координироваться с владельцем соответствующей роли.

## Обязанности

- построить requirement-to-test matrix;
- выявить непокрытые acceptance criteria;
- запустить unit/integration/race tests;
- реализовать crash-point tests;
- реализовать reconnect scenarios;
- провести sandbox smoke и soak tests;
- проверить bounded memory/queues;
- проверить graceful shutdown;
- проверить redaction secrets;
- проверить live-trading safeguards;
- проверить воспроизводимость backtest;
- проверить look-ahead guard;
- проверить migrations на чистой и существующей БД;
- проверить один бинарник на Linux/macOS;
- подготовить operations guide и troubleshooting;
- оформить известные ограничения выпуска.

## Независимость проверки

QA agent не должен считать тест достаточным только потому, что его написал
владелец реализации. Для критических инвариантов нужны независимые сценарии:

- нет order без intent;
- нет дубля после crash;
- нет агрегации разных asset;
- risk limit изолирован по strategy;
- backtest не видит будущие данные;
- backtest не вызывает торговый API;
- второй агент не получает DB lock;
- внешний web bind без auth невозможен.

## Политика дефектов

Для каждого дефекта указать:

- severity;
- нарушенный requirement ID;
- минимальный сценарий воспроизведения;
- ожидаемое и фактическое поведение;
- риск для денег/данных;
- рекомендуемого владельца исправления.

Critical/High дефекты не исправляются скрыто внутри большого QA change.

## Обязательные проверки

```text
format/lint
go test ./...
go test -race ./...
storage contract tests
engine crash tests
backtest golden/reproducibility tests
HTTP/SSE tests
sandbox smoke tests (при credentials)
soak test
single-binary build
```

Точные команды фиксируются после появления toolchain и CI.

Локальные T-Invest sandbox проверки используют уже присутствующую в окружении
`TINVEST_SANDBOX_TOKEN`. Если переменная ещё не загружена в текущую
интерактивную shell-сессию, допускается однократно выполнить
`source ~/.zshrc.private`; делать это перед каждым тестом не требуется. QA
запрещено печатать значение токена, включать `set -x` или прикладывать
environment dump к отчёту.

## Не входит в роль

- принятие новых продуктовых требований;
- изменение архитектуры без ADR;
- включение live trading;
- маскировка flaky tests повторными запусками;
- объявление MVP готовым при известных Critical/High дефектах.

## Handoff

Дополнительно указать:

- requirement-to-test matrix;
- окружение и продолжительность тестов;
- найденные дефекты;
- показатели soak test;
- неподтверждённые требования;
- release recommendation: ready/not ready с обоснованием.
