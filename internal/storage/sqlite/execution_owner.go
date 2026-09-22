package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

func (s *Store) FindExecutionOwner(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	orderID domain.OrderID,
	clientID domain.ClientOrderID,
) (storage.ExecutionOwner, error) {
	if err := accountID.Validate(); err != nil {
		return storage.ExecutionOwner{}, fmt.Errorf("execution account: %w", err)
	}
	if err := orderID.Validate(); err != nil {
		return storage.ExecutionOwner{}, fmt.Errorf("execution order ID: %w", err)
	}
	if clientID != "" {
		if err := clientID.Validate(); err != nil {
			return storage.ExecutionOwner{}, fmt.Errorf("execution client order ID: %w", err)
		}
	}
	rows, err := s.queries.FindExecutionOwners(ctx, generated.FindExecutionOwnersParams{
		AccountID:     string(accountID),
		OrderID:       sql.NullString{String: string(orderID), Valid: true},
		ClientOrderID: string(clientID),
	})
	if err != nil {
		return storage.ExecutionOwner{}, fmt.Errorf("sqlite: find execution owner: %w", err)
	}
	if len(rows) == 0 {
		return storage.ExecutionOwner{}, fmt.Errorf("execution order %s: %w", orderID, storage.ErrNotFound)
	}
	if len(rows) != 1 {
		return storage.ExecutionOwner{}, fmt.Errorf("execution order identifiers identify different intents: %w", storage.ErrConflict)
	}
	row := rows[0]
	if row.OrderAccountID.Valid && row.OrderAccountID.String != string(accountID) {
		return storage.ExecutionOwner{}, fmt.Errorf("persisted order and intent accounts disagree: %w", storage.ErrConflict)
	}
	if (row.ExchangeOrderID.Valid && row.ExchangeOrderID.String != string(orderID)) ||
		(clientID != "" && row.ClientOrderID != string(clientID)) {
		return storage.ExecutionOwner{}, fmt.Errorf("execution order identifiers disagree with persisted order: %w", storage.ErrConflict)
	}
	return storage.ExecutionOwner{
		StrategyID:   domain.StrategyID(row.StrategyID),
		InstrumentID: domain.InstrumentID(row.InstrumentID), Side: domain.OrderSide(row.Side),
	}, nil
}
