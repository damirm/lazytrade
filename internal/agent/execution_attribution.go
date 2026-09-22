package agent

import (
	"context"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
)

func (r Runtime) attributeExecution(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	incoming exchange.Execution,
) (domain.Execution, error) {
	owner, err := r.Intents.FindExecutionOwner(ctx, accountID, incoming.OrderID, incoming.ClientOrderID)
	if err != nil {
		return domain.Execution{}, fmt.Errorf("resolve execution owner: %w", err)
	}
	if owner.InstrumentID != incoming.InstrumentID || owner.Side != incoming.Side {
		return domain.Execution{}, fmt.Errorf("execution does not match persisted instrument or side: %w", storage.ErrConflict)
	}
	// An inactive/failed strategy may still own late fills. Ownership is
	// established from storage, not from the currently running workers.
	return incoming.Attribute(owner.StrategyID)
}
