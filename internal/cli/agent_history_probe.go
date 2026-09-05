package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
	"github.com/spf13/cobra"
)

type historyProbeReport struct {
	AccountID domain.ExchangeAccountID `json:"account_id"`
	From      time.Time                `json:"from"`
	To        time.Time                `json:"to"`
	Complete  bool                     `json:"complete"`
	Orders    []historyProbeOrder      `json:"orders"`
}

type historyProbeOrder struct {
	ExchangeOrderID      domain.OrderID       `json:"exchange_order_id"`
	ClientOrderID        domain.ClientOrderID `json:"client_order_id"`
	InstrumentID         domain.InstrumentID  `json:"instrument_id"`
	Side                 domain.OrderSide     `json:"side"`
	OrderType            domain.OrderType     `json:"order_type"`
	RequestedQuantity    string               `json:"requested_quantity"`
	Status               domain.OrderStatus   `json:"status"`
	SubmittedAt          time.Time            `json:"submitted_at"`
	Complete             bool                 `json:"complete"`
	CumulativeCommission domain.Money         `json:"cumulative_commission"`
	Fills                []historyProbeFill   `json:"fills"`
}

type historyProbeFill struct {
	TradeID    string       `json:"trade_id"`
	Quantity   string       `json:"quantity"`
	Price      domain.Price `json:"price"`
	ExecutedAt time.Time    `json:"executed_at"`
}

func newAgentHistoryProbeCommand() *cobra.Command {
	var configPath, fromValue, toValue string
	var lookback, timeout time.Duration
	var allowEmpty bool
	command := &cobra.Command{
		Use:   "history-probe",
		Short: "Read and validate T-Invest sandbox execution history",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFileFor(configPath, appconfig.CommandAgent)
			if err != nil {
				return err
			}
			to, err := parseHistoryProbeTime(toValue, time.Now().UTC().Add(-5*time.Minute))
			if err != nil {
				return fmt.Errorf("history probe --to: %w", err)
			}
			from := to.Add(-lookback)
			if fromValue != "" {
				from, err = parseHistoryProbeTime(fromValue, time.Time{})
				if err != nil {
					return fmt.Errorf("history probe --from: %w", err)
				}
			}
			if lookback <= 0 || !from.Before(to) {
				return errors.New("history probe requires a positive non-empty UTC window")
			}
			if to.Sub(from) > 7*24*time.Hour {
				return errors.New("history probe window must not exceed 168h")
			}
			if timeout <= 0 {
				return errors.New("history probe timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(command.Context(), timeout)
			defer cancel()
			report, err := runConfiguredHistoryProbe(ctx, configPath, cfg, from, to)
			if err != nil {
				return err
			}
			if len(report.Orders) == 0 && !allowEmpty {
				return errors.New("history probe found no orders; bridge was not exercised (use --allow-empty to accept)")
			}
			encoder := json.NewEncoder(command.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&fromValue, "from", "", "history start in RFC3339 (overrides lookback)")
	command.Flags().StringVar(&toValue, "to", "", "history end in RFC3339 (default: now)")
	command.Flags().DurationVar(&lookback, "lookback", 72*time.Hour, "history lookback when --from is omitted")
	command.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "overall probe timeout")
	command.Flags().BoolVar(&allowEmpty, "allow-empty", false, "accept an empty history window")
	_ = command.MarkFlagRequired("config")
	return command
}

func parseHistoryProbeTime(raw string, defaultValue time.Time) (time.Time, error) {
	if raw == "" {
		return defaultValue.UTC(), nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}

func runConfiguredHistoryProbe(
	ctx context.Context,
	configPath string,
	cfg appconfig.Config,
	from, to time.Time,
) (historyProbeReport, error) {
	if len(cfg.Agent.Strategies) == 0 {
		return historyProbeReport{}, errors.New("history probe requires at least one configured strategy")
	}
	strategyConfig := cfg.Agent.Strategies[0]
	for _, configured := range cfg.Agent.Strategies[1:] {
		if configured.Exchange != strategyConfig.Exchange {
			return historyProbeReport{}, errors.New("history probe currently requires all strategies to use one exchange")
		}
	}
	exchangeConfig := cfg.Exchanges[strategyConfig.Exchange]
	if !exchangeConfig.Sandbox || exchangeConfig.AllowLiveTrading {
		return historyProbeReport{}, errors.New("history probe supports T-Invest sandbox mode only")
	}
	token, err := requiredEnvironment(exchangeConfig.TokenEnv)
	if err != nil {
		return historyProbeReport{}, err
	}
	accountID, err := requiredEnvironment(exchangeConfig.AccountIDEnv)
	if err != nil {
		return historyProbeReport{}, err
	}
	adapter, err := tinvest.Open(ctx, tinvest.Config{
		Name: strategyConfig.Exchange, Token: token, AccountID: accountID,
		CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
	})
	if err != nil {
		return historyProbeReport{}, fmt.Errorf("history probe connect: %w", err)
	}
	defer adapter.Close()
	return runHistoryProbe(ctx, adapter, domain.ExchangeAccountID(strategyConfig.Exchange), from, to)
}

func runHistoryProbe(
	ctx context.Context,
	provider exchange.ExecutionHistoryProvider,
	accountID domain.ExchangeAccountID,
	from, to time.Time,
) (historyProbeReport, error) {
	if err := accountID.Validate(); err != nil {
		return historyProbeReport{}, fmt.Errorf("history probe account: %w", err)
	}
	if from.IsZero() || to.IsZero() || from.Location() != time.UTC || to.Location() != time.UTC || !from.Before(to) {
		return historyProbeReport{}, errors.New("history probe requires a non-empty increasing UTC window")
	}
	history, err := provider.ExecutionHistory(ctx, exchange.ExecutionHistoryRequest{
		AccountID: accountID, From: from, To: to,
	})
	if err != nil {
		return historyProbeReport{}, fmt.Errorf("history probe: %w", err)
	}
	if !history.Complete || !history.From.Equal(from) || !history.To.Equal(to) {
		return historyProbeReport{}, errors.New("history probe returned an incomplete or different window")
	}
	report := historyProbeReport{AccountID: accountID, From: from, To: to, Complete: true}
	report.Orders = make([]historyProbeOrder, 0, len(history.Orders))
	for _, snapshot := range history.Orders {
		order := historyProbeOrder{
			ExchangeOrderID: snapshot.ExchangeOrderID, ClientOrderID: snapshot.ClientOrderID,
			InstrumentID: snapshot.InstrumentID, Side: snapshot.Side, OrderType: snapshot.OrderType,
			RequestedQuantity: snapshot.RequestedQuantity.Value.String(), Status: snapshot.Status,
			SubmittedAt: snapshot.SubmittedAt, Complete: snapshot.Complete,
			CumulativeCommission: snapshot.CumulativeCommission,
			Fills:                make([]historyProbeFill, 0, len(snapshot.Fills)),
		}
		for _, fill := range snapshot.Fills {
			order.Fills = append(order.Fills, historyProbeFill{
				TradeID: fill.TradeID, Quantity: fill.Quantity.Value.String(),
				Price: fill.Price, ExecutedAt: fill.ExecutedAt,
			})
		}
		report.Orders = append(report.Orders, order)
	}
	return report, nil
}
