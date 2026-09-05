package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/shopspring/decimal"
)

var ErrReconciliationMismatch = errors.New("exchange reconciliation mismatch")

type ReconciliationIssue struct {
	Kind          string
	InstrumentID  domain.InstrumentID
	ClientOrderID domain.ClientOrderID
	Local         string
	Remote        string
}

type ReconciliationReport struct {
	AccountID domain.ExchangeAccountID
	Issues    []ReconciliationIssue
}

type StartupReconciler interface {
	Reconcile(context.Context, domain.ExchangeAccountID) (ReconciliationReport, error)
}

type Reconciler struct {
	Exchange exchange.Exchange
	Store    storage.ReconciliationStore
}

func (r Reconciler) Reconcile(ctx context.Context, accountID domain.ExchangeAccountID) (ReconciliationReport, error) {
	report := ReconciliationReport{AccountID: accountID}
	if r.Exchange == nil || r.Store == nil {
		return report, errors.New("reconciliation exchange and store are required")
	}
	remotePortfolio, err := r.Exchange.Portfolio(ctx, accountID)
	if err != nil {
		return report, fmt.Errorf("load exchange portfolio: %w", err)
	}
	localPositions, err := r.Store.ListPositionsByExchange(ctx, accountID)
	if err != nil {
		return report, err
	}
	report.Issues = append(report.Issues, comparePositions(localPositions, remotePortfolio.Positions)...)

	remoteOrders, err := r.Exchange.OpenOrders(ctx, accountID)
	if err != nil {
		return report, fmt.Errorf("load exchange open orders: %w", err)
	}
	localOrders, err := r.Store.ListOpenOrdersByExchange(ctx, accountID)
	if err != nil {
		return report, err
	}
	if registrar, ok := r.Exchange.(exchange.OrderContextRegistrar); ok {
		for _, order := range localOrders {
			registrar.RegisterOrderContext(order.ExchangeOrderID, order.StrategyID, order.InstrumentID, order.Side)
		}
	}
	report.Issues = append(report.Issues, compareOpenOrders(localOrders, remoteOrders)...)
	sort.Slice(report.Issues, func(i, j int) bool {
		left, right := report.Issues[i], report.Issues[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.InstrumentID != right.InstrumentID {
			return left.InstrumentID < right.InstrumentID
		}
		return left.ClientOrderID < right.ClientOrderID
	})
	if len(report.Issues) > 0 {
		return report, fmt.Errorf("%w: %d issue(s)", ErrReconciliationMismatch, len(report.Issues))
	}
	return report, nil
}

func comparePositions(local []storage.Position, remote []exchange.Position) []ReconciliationIssue {
	localByInstrument := make(map[domain.InstrumentID]decimal.Decimal, len(local))
	for _, position := range local {
		localByInstrument[position.InstrumentID] = localByInstrument[position.InstrumentID].Add(position.Quantity.Value)
	}
	remoteByInstrument := make(map[domain.InstrumentID]decimal.Decimal, len(remote))
	for _, position := range remote {
		remoteByInstrument[position.InstrumentID] = remoteByInstrument[position.InstrumentID].Add(position.Quantity.Value)
	}
	keys := make(map[domain.InstrumentID]struct{}, len(localByInstrument)+len(remoteByInstrument))
	for id := range localByInstrument {
		keys[id] = struct{}{}
	}
	for id := range remoteByInstrument {
		keys[id] = struct{}{}
	}
	var issues []ReconciliationIssue
	for id := range keys {
		left, right := localByInstrument[id], remoteByInstrument[id]
		if !left.Equal(right) {
			issues = append(issues, ReconciliationIssue{
				Kind: "position_quantity", InstrumentID: id,
				Local: left.String(), Remote: right.String(),
			})
		}
	}
	return issues
}

func compareOpenOrders(local []storage.LocalOpenOrder, remote []domain.Order) []ReconciliationIssue {
	localByClient := make(map[domain.ClientOrderID]storage.LocalOpenOrder, len(local))
	for _, order := range local {
		localByClient[order.ClientOrderID] = order
	}
	remoteByClient := make(map[domain.ClientOrderID]domain.Order, len(remote))
	for _, order := range remote {
		remoteByClient[order.ClientOrderID] = order
	}
	keys := make(map[domain.ClientOrderID]struct{}, len(localByClient)+len(remoteByClient))
	for id := range localByClient {
		keys[id] = struct{}{}
	}
	for id := range remoteByClient {
		keys[id] = struct{}{}
	}
	var issues []ReconciliationIssue
	for id := range keys {
		left, localOK := localByClient[id]
		right, remoteOK := remoteByClient[id]
		if !localOK || !remoteOK {
			issues = append(issues, ReconciliationIssue{
				Kind: "open_order_presence", ClientOrderID: id,
				Local: fmt.Sprint(localOK), Remote: fmt.Sprint(remoteOK),
			})
			continue
		}
		if left.ExchangeOrderID != right.ID || left.InstrumentID != right.InstrumentID ||
			!left.RequestedQuantity.Value.Equal(right.Quantity.Value) ||
			!left.FilledQuantity.Value.Equal(right.FilledQuantity.Value) ||
			left.Status != orderStatus(right.Status) {
			issues = append(issues, ReconciliationIssue{
				Kind: "open_order_state", InstrumentID: left.InstrumentID, ClientOrderID: id,
				Local:  fmt.Sprintf("%s/%s/%s", left.ExchangeOrderID, left.Status, left.FilledQuantity.Value),
				Remote: fmt.Sprintf("%s/%s/%s", right.ID, orderStatus(right.Status), right.FilledQuantity.Value),
			})
		}
	}
	return issues
}
