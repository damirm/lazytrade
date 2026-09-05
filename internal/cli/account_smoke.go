package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"
)

const smokeStrategyID = domain.StrategyID("sandbox-smoke-test")

type smokeTestExchange interface {
	exchange.Exchange
}

type smokeTestReport struct {
	BuyOrderID, SellOrderID   domain.OrderID
	BuyClientID, SellClientID domain.ClientOrderID
	BuyExecutions             int
	SellExecutions            int
	BuyQuantity               decimal.Decimal
	SellQuantity              decimal.Decimal
	BuyCommission             decimal.Decimal
	SellCommission            decimal.Decimal
	CommissionAsset           string
	FinalQuantity             decimal.Decimal
	OpenOrders                int
	CleanupAttempted          bool
	CleanupError              error
}

func newAccountSmokeTestCommand() *cobra.Command {
	var configPath, exchangeID, instrument, quantity string
	var confirm bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "smoke-test",
		Short: "Buy and sell an instrument on a T-Invest sandbox account",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !confirm {
				return errors.New("smoke-test places orders; pass --confirm to continue")
			}
			cfg, err := appconfig.LoadFile(configPath)
			if err != nil {
				return err
			}
			exchangeConfig, err := accountListExchange(cfg, exchangeID)
			if err != nil {
				return err
			}
			accountID, err := requiredEnvironment(exchangeConfig.AccountIDEnv)
			if err != nil {
				return err
			}
			orderQuantity, err := domain.NewQuantity(quantity)
			if err != nil || !orderQuantity.Value.IsPositive() {
				return errors.New("--quantity must be a positive decimal")
			}
			token, err := requiredEnvironment(exchangeConfig.TokenEnv)
			if err != nil {
				return err
			}
			adapter, err := tinvest.Open(command.Context(), tinvest.Config{
				Name: exchangeID, Token: token, AccountID: accountID,
				CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
			})
			if err != nil {
				return err
			}
			defer adapter.Close()
			ctx, cancel := context.WithTimeout(command.Context(), timeout)
			defer cancel()
			resolvedInstrument, err := adapter.Instrument(ctx, domain.InstrumentID(instrument))
			if err != nil {
				return fmt.Errorf("smoke-test instrument: %w", err)
			}
			report, smokeErr := runSandboxSmokeTest(ctx, adapter, domain.ExchangeAccountID(exchangeID),
				resolvedInstrument.ID, orderQuantity)
			writeSmokeTestReport(command, report, smokeErr)
			return smokeErr
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&exchangeID, "exchange", "", "configured sandbox exchange ID")
	command.Flags().StringVar(&instrument, "instrument", "", "instrument UID or FIGI")
	command.Flags().StringVar(&quantity, "quantity", "", "positive quantity in instrument units")
	command.Flags().BoolVar(&confirm, "confirm", false, "confirm placement of sandbox market orders")
	command.Flags().DurationVar(&timeout, "timeout", 45*time.Second, "overall smoke-test timeout")
	for _, flag := range []string{"config", "exchange", "instrument", "quantity"} {
		_ = command.MarkFlagRequired(flag)
	}
	return command
}

// runSandboxSmokeTest requires an initially flat instrument and performs one
// round trip. On a failure after buying it makes a best-effort flatten attempt.
func runSandboxSmokeTest(
	ctx context.Context,
	adapter smokeTestExchange,
	accountID domain.ExchangeAccountID,
	instrumentID domain.InstrumentID,
	quantity domain.Quantity,
) (report smokeTestReport, resultErr error) {
	if err := validateSmokeInputs(accountID, instrumentID, quantity); err != nil {
		return report, err
	}
	instruments, err := adapter.Instruments(ctx)
	if err != nil {
		return report, fmt.Errorf("smoke-test instrument metadata: %w", err)
	}
	instrument, ok := findInstrument(instruments, instrumentID)
	if !ok {
		return report, fmt.Errorf("smoke-test instrument %q was not found", instrumentID)
	}
	if err := validateWholeLots(quantity, instrument.QuantityStep); err != nil {
		return report, fmt.Errorf("smoke-test quantity: %w", err)
	}
	if err := requireCleanInstrument(ctx, adapter, accountID, instrumentID); err != nil {
		return report, err
	}

	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	stream, err := adapter.SubscribeExecutions(streamCtx, accountID)
	if err != nil {
		return report, fmt.Errorf("smoke-test execution stream: %w", err)
	}

	buyAttempted := false
	defer func() {
		if resultErr == nil || !buyAttempted {
			return
		}
		report.CleanupAttempted = true
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		report.CleanupError = cleanupSmokePosition(cleanupCtx, adapter, accountID, instrumentID, quantity, report.BuyOrderID, report.BuyClientID)
	}()
	buyRequest := newSmokeOrder(accountID, instrumentID, domain.OrderSideBuy, quantity)
	report.BuyClientID = buyRequest.ClientOrderID
	buyAttempted = true
	buy, err := placeSmokeOrder(ctx, adapter, buyRequest)
	if err != nil {
		return report, fmt.Errorf("smoke-test buy: %w", err)
	}
	report.BuyOrderID = buy.ID
	report.BuyExecutions, report.BuyQuantity, report.BuyCommission, report.CommissionAsset, err =
		waitSmokeFill(ctx, adapter, stream, buy.ID, quantity)
	if err != nil {
		return report, fmt.Errorf("smoke-test buy fill: %w", err)
	}
	if err := requirePositionQuantity(ctx, adapter, accountID, instrumentID, quantity.Value); err != nil {
		return report, fmt.Errorf("smoke-test bought position: %w", err)
	}

	sellRequest := newSmokeOrder(accountID, instrumentID, domain.OrderSideSell, quantity)
	report.SellClientID = sellRequest.ClientOrderID
	sell, err := placeSmokeOrder(ctx, adapter, sellRequest)
	if err != nil {
		return report, fmt.Errorf("smoke-test sell: %w", err)
	}
	report.SellOrderID = sell.ID
	var sellAsset string
	report.SellExecutions, report.SellQuantity, report.SellCommission, sellAsset, err =
		waitSmokeFill(ctx, adapter, stream, sell.ID, quantity)
	if err != nil {
		return report, fmt.Errorf("smoke-test sell fill: %w", err)
	}
	if sellAsset != report.CommissionAsset {
		return report, fmt.Errorf("smoke-test commission asset changed from %s to %s", report.CommissionAsset, sellAsset)
	}
	portfolio, err := adapter.Portfolio(ctx, accountID)
	if err != nil {
		return report, fmt.Errorf("smoke-test final portfolio: %w", err)
	}
	report.FinalQuantity = instrumentQuantity(portfolio, instrumentID)
	if !report.FinalQuantity.IsZero() {
		return report, fmt.Errorf("smoke-test final position is %s, expected 0", report.FinalQuantity)
	}
	orders, err := adapter.OpenOrders(ctx, accountID)
	if err != nil {
		return report, fmt.Errorf("smoke-test final open orders: %w", err)
	}
	report.OpenOrders = len(orders)
	if report.OpenOrders != 0 {
		return report, fmt.Errorf("smoke-test left %d open orders", report.OpenOrders)
	}
	return report, nil
}

func validateSmokeInputs(accountID domain.ExchangeAccountID, instrumentID domain.InstrumentID, quantity domain.Quantity) error {
	if err := accountID.Validate(); err != nil {
		return fmt.Errorf("smoke-test account: %w", err)
	}
	if err := instrumentID.Validate(); err != nil {
		return fmt.Errorf("smoke-test instrument: %w", err)
	}
	if err := quantity.Validate(); err != nil || !quantity.Value.IsPositive() {
		return errors.New("smoke-test quantity must be positive")
	}
	return nil
}

func findInstrument(instruments []domain.Instrument, id domain.InstrumentID) (domain.Instrument, bool) {
	for _, instrument := range instruments {
		if instrument.ID == id {
			return instrument, true
		}
	}
	return domain.Instrument{}, false
}

func requireCleanInstrument(ctx context.Context, adapter smokeTestExchange, accountID domain.ExchangeAccountID, instrumentID domain.InstrumentID) error {
	portfolio, err := adapter.Portfolio(ctx, accountID)
	if err != nil {
		return fmt.Errorf("smoke-test initial portfolio: %w", err)
	}
	if value := instrumentQuantity(portfolio, instrumentID); !value.IsZero() {
		return fmt.Errorf("smoke-test requires a flat instrument; current position is %s", value)
	}
	orders, err := adapter.OpenOrders(ctx, accountID)
	if err != nil {
		return fmt.Errorf("smoke-test initial open orders: %w", err)
	}
	if len(orders) != 0 {
		return fmt.Errorf("smoke-test requires no open orders on the account; found %d", len(orders))
	}
	return nil
}

func newSmokeOrder(accountID domain.ExchangeAccountID, instrumentID domain.InstrumentID, side domain.OrderSide, quantity domain.Quantity) exchange.NewOrder {
	return exchange.NewOrder{
		ClientOrderID: domain.ClientOrderID(uuid.NewString()), StrategyID: smokeStrategyID,
		ExchangeAccountID: accountID, InstrumentID: instrumentID, Side: side,
		Type: domain.OrderTypeMarket, Quantity: quantity,
	}
}

func placeSmokeOrder(ctx context.Context, adapter smokeTestExchange, request exchange.NewOrder) (domain.Order, error) {
	order, err := adapter.PlaceOrder(ctx, request)
	if err == nil {
		return order, nil
	}
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) || exchangeErr.Outcome != exchange.OutcomeUnknown {
		return domain.Order{}, err
	}
	resolved, resolveErr := adapter.GetOrderByClientID(ctx, request.ClientOrderID)
	if resolveErr != nil {
		return domain.Order{}, fmt.Errorf("placement outcome unknown and lookup by client ID failed: %w", resolveErr)
	}
	return resolved, nil
}

func waitSmokeFill(
	ctx context.Context,
	adapter smokeTestExchange,
	stream exchange.ExecutionStream,
	orderID domain.OrderID,
	expected domain.Quantity,
) (int, decimal.Decimal, decimal.Decimal, string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var executions int
	total := decimal.Zero
	commission := decimal.Zero
	var commissionAsset string
	executionEvents, executionErrors := stream.Executions, stream.Errors
	for {
		select {
		case <-ctx.Done():
			return executions, total, commission, commissionAsset, ctx.Err()
		case err, ok := <-executionErrors:
			if !ok {
				return executions, total, commission, commissionAsset, errors.New("execution error stream closed")
			}
			if err != nil {
				return executions, total, commission, commissionAsset, err
			}
		case execution, ok := <-executionEvents:
			if !ok {
				return executions, total, commission, commissionAsset, errors.New("execution stream closed")
			}
			if execution.OrderID == orderID {
				if commissionAsset != "" && execution.Commission.Asset != commissionAsset {
					return executions, total, commission, commissionAsset, errors.New("execution commission asset changed")
				}
				commissionAsset = execution.Commission.Asset
				executions++
				total = total.Add(execution.Quantity.Value)
				commission = commission.Add(execution.Commission.Amount)
			}
		case <-ticker.C:
			order, err := adapter.GetOrder(ctx, orderID)
			if err != nil {
				return executions, total, commission, commissionAsset, err
			}
			switch order.Status {
			case domain.OrderStatusRejected, domain.OrderStatusCancelled:
				return executions, total, commission, commissionAsset, fmt.Errorf("order reached terminal status %d", order.Status)
			case domain.OrderStatusFilled:
				if total.Equal(expected.Value) {
					return executions, total, commission, commissionAsset, nil
				}
				// The order state may precede the corresponding stream event.
			}
		}
	}
}

func requirePositionQuantity(ctx context.Context, adapter smokeTestExchange, accountID domain.ExchangeAccountID, instrumentID domain.InstrumentID, expected decimal.Decimal) error {
	portfolio, err := adapter.Portfolio(ctx, accountID)
	if err != nil {
		return err
	}
	actual := instrumentQuantity(portfolio, instrumentID)
	if !actual.Equal(expected) {
		return fmt.Errorf("position is %s, expected %s", actual, expected)
	}
	return nil
}

func instrumentQuantity(portfolio exchange.Portfolio, instrumentID domain.InstrumentID) decimal.Decimal {
	result := decimal.Zero
	for _, position := range portfolio.Positions {
		if position.InstrumentID == instrumentID {
			result = result.Add(position.Quantity.Value)
		}
	}
	return result
}

func cleanupSmokePosition(ctx context.Context, adapter smokeTestExchange, accountID domain.ExchangeAccountID, instrumentID domain.InstrumentID, maximum domain.Quantity, buyOrderID domain.OrderID, buyClientID domain.ClientOrderID) error {
	if buyOrderID == "" && buyClientID != "" {
		resolved, resolveErr := adapter.GetOrderByClientID(ctx, buyClientID)
		if resolveErr != nil {
			return fmt.Errorf("resolve buy order %s before cleanup: %w", buyClientID, resolveErr)
		}
		buyOrderID = resolved.ID
	}
	if buyOrderID == "" {
		return errors.New("resolve buy order before cleanup: exchange order ID is unknown")
	}
	order, err := adapter.GetOrder(ctx, buyOrderID)
	if err == nil && (order.Status == domain.OrderStatusAccepted || order.Status == domain.OrderStatusPartiallyFilled) {
		if _, cancelErr := cancelOnceAndObserve(ctx, adapter, buyOrderID); cancelErr != nil {
			return fmt.Errorf("cancel buy order: %w", cancelErr)
		}
	}
	portfolio, err := adapter.Portfolio(ctx, accountID)
	if err != nil {
		return fmt.Errorf("read position: %w", err)
	}
	position := instrumentQuantity(portfolio, instrumentID)
	if position.IsZero() {
		return verifySmokeClean(ctx, adapter, accountID, instrumentID)
	}
	if position.IsNegative() || position.GreaterThan(maximum.Value) {
		return fmt.Errorf("refuse automatic cleanup of unexpected position %s", position)
	}
	cleanupOrder, err := placeSmokeOrder(ctx, adapter, newSmokeOrder(accountID, instrumentID, domain.OrderSideSell, domain.Quantity{Value: position}))
	if err != nil {
		return fmt.Errorf("place cleanup sell: %w", err)
	}
	terminal, err := waitOrderTerminal(ctx, adapter, cleanupOrder.ID)
	if err != nil {
		return fmt.Errorf("confirm cleanup sell: %w", err)
	}
	if terminal.Status != domain.OrderStatusFilled {
		return fmt.Errorf("confirm cleanup sell: terminal status %d, expected filled", terminal.Status)
	}
	return verifySmokeClean(ctx, adapter, accountID, instrumentID)
}

// cancelOnceAndObserve never repeats the mutation. A successful response and
// an unknown outcome are both resolved by observing the order until it reaches
// a terminal state. An active order is not proof that an uncertain cancellation
// was not applied, because the read model may lag behind the command.
func cancelOnceAndObserve(ctx context.Context, adapter smokeTestExchange, orderID domain.OrderID) (domain.Order, error) {
	cancelErr := adapter.CancelOrder(ctx, orderID)
	if cancelErr != nil && !isUnknownOutcome(cancelErr) {
		return domain.Order{}, cancelErr
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		order, observeErr := adapter.GetOrder(ctx, orderID)
		if observeErr == nil {
			switch order.Status {
			case domain.OrderStatusFilled, domain.OrderStatusCancelled, domain.OrderStatusRejected:
				return order, nil
			}
		} else if !exchange.IsCategory(observeErr, exchange.ErrorNotFound) {
			return domain.Order{}, errors.Join(cancelErr, fmt.Errorf("observe order after cancellation: %w", observeErr))
		}

		select {
		case <-ctx.Done():
			return domain.Order{}, errors.Join(cancelErr, fmt.Errorf("observe order after cancellation: %w", ctx.Err()))
		case <-ticker.C:
		}
	}
}

func isUnknownOutcome(err error) bool {
	var exchangeErr *exchange.Error
	return errors.As(err, &exchangeErr) && exchangeErr.Outcome == exchange.OutcomeUnknown
}

func waitOrderTerminal(ctx context.Context, adapter smokeTestExchange, orderID domain.OrderID) (domain.Order, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		order, err := adapter.GetOrder(ctx, orderID)
		if err != nil {
			return domain.Order{}, err
		}
		switch order.Status {
		case domain.OrderStatusFilled, domain.OrderStatusCancelled, domain.OrderStatusRejected:
			return order, nil
		}
		select {
		case <-ctx.Done():
			return domain.Order{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func verifySmokeClean(ctx context.Context, adapter smokeTestExchange, accountID domain.ExchangeAccountID, instrumentID domain.InstrumentID) error {
	portfolio, err := adapter.Portfolio(ctx, accountID)
	if err != nil {
		return fmt.Errorf("verify cleanup portfolio: %w", err)
	}
	if position := instrumentQuantity(portfolio, instrumentID); !position.IsZero() {
		return fmt.Errorf("verify cleanup position: got %s, expected 0", position)
	}
	orders, err := adapter.OpenOrders(ctx, accountID)
	if err != nil {
		return fmt.Errorf("verify cleanup open orders: %w", err)
	}
	if len(orders) != 0 {
		return fmt.Errorf("verify cleanup open orders: found %d", len(orders))
	}
	return nil
}

func writeSmokeTestReport(command *cobra.Command, report smokeTestReport, smokeErr error) {
	status := "OK"
	if smokeErr != nil {
		status = "FAILED"
	}
	out := command.OutOrStdout()
	_, _ = fmt.Fprintf(out, "STATUS\t%s\nBUY_CLIENT_ID\t%s\nBUY_ORDER_ID\t%s\nBUY_EXECUTIONS\t%d\nBUY_QUANTITY\t%s\n",
		status, report.BuyClientID, report.BuyOrderID, report.BuyExecutions, report.BuyQuantity)
	_, _ = fmt.Fprintf(out, "SELL_CLIENT_ID\t%s\nSELL_ORDER_ID\t%s\nSELL_EXECUTIONS\t%d\nSELL_QUANTITY\t%s\nFINAL_QUANTITY\t%s\nOPEN_ORDERS\t%d\n",
		report.SellClientID, report.SellOrderID, report.SellExecutions, report.SellQuantity, report.FinalQuantity, report.OpenOrders)
	_, _ = fmt.Fprintf(out, "BUY_COMMISSION\t%s %s\nSELL_COMMISSION\t%s %s\n",
		report.BuyCommission, report.CommissionAsset, report.SellCommission, report.CommissionAsset)
	if report.CleanupAttempted {
		cleanup := "OK"
		if report.CleanupError != nil {
			cleanup = "FAILED: " + report.CleanupError.Error()
		}
		_, _ = fmt.Fprintf(out, "CLEANUP\t%s\n", cleanup)
	}
}
