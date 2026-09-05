# Architecture Decision Records

ADR фиксируют уже принятые архитектурные решения: контекст, выбранный вариант,
последствия и условия пересмотра. Долгосрочное желание или ещё не выбранный
дизайн относится к `docs/roadmap/` либо `docs/specs/`, а не к ADR.

Текущие ADR:

- [ADR-0001: domain и strategy contracts](0001-domain-and-strategy-contracts.md)
- [ADR-0002: SQLite storage](0002-sqlite-storage.md)
- [ADR-0003: T-Invest Go SDK](0003-tinvest-go-sdk.md)
- [ADR-0004: структурированные логи через log/slog](0004-structured-logging.md)

Новые ADR получают следующий номер. Принятый ADR не переписывается так, будто
первоначального решения не было: существенная замена оформляется новым ADR со
ссылкой `supersedes`, а уточнение текущего факта — датированным дополнением.
