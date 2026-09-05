package backtest

import (
	"encoding/json"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

const ReportSchemaVersion = 3

type DecimalMoney struct {
	Amount string `json:"amount"`
	Asset  string `json:"asset"`
}

type Metrics struct {
	InitialEquity  DecimalMoney `json:"initial_equity"`
	FinalEquity    DecimalMoney `json:"final_equity"`
	TotalPnL       DecimalMoney `json:"total_pnl"`
	RealizedPnL    DecimalMoney `json:"realized_pnl"`
	UnrealizedPnL  DecimalMoney `json:"unrealized_pnl"`
	ReturnPercent  string       `json:"return_percent"`
	Commissions    DecimalMoney `json:"commissions"`
	SlippageCost   DecimalMoney `json:"slippage_cost"`
	MaxDrawdown    DecimalMoney `json:"max_drawdown"`
	MaxDrawdownPct string       `json:"max_drawdown_percent"`
	Orders         uint64       `json:"orders"`
	Fills          uint64       `json:"fills"`
	RiskRejections uint64       `json:"risk_rejections"`
	RiskPauses     uint64       `json:"risk_pauses"`
	ClosedTrades   uint64       `json:"closed_trades"`
	WinningTrades  uint64       `json:"winning_trades"`
	LosingTrades   uint64       `json:"losing_trades"`
	WinRate        string       `json:"win_rate"`
	GrossProfit    DecimalMoney `json:"gross_profit"`
	GrossLoss      DecimalMoney `json:"gross_loss"`
	ProfitFactor   string       `json:"profit_factor"`
	ExposureTime   string       `json:"exposure_time"`
	ExposurePct    string       `json:"exposure_percent"`
}

type Assumptions struct {
	FillModel         string `json:"fill_model"`
	CommissionUnit    string `json:"commission_unit"`
	SlippageUnit      string `json:"slippage_unit"`
	PartialFills      bool   `json:"partial_fills"`
	VolumeLimitsFills bool   `json:"volume_limits_fills"`
	ForceCloseAtEnd   bool   `json:"force_close_at_end"`
	ShortSelling      bool   `json:"short_selling"`
}

type Report struct {
	SchemaVersion  uint32             `json:"schema_version"`
	Metrics        Metrics            `json:"metrics"`
	Orders         []domain.Order     `json:"orders"`
	Executions     []domain.Execution `json:"executions"`
	Assumptions    Assumptions        `json:"assumptions"`
	Warnings       []string           `json:"warnings"`
	LastRiskReason string             `json:"last_risk_reason,omitempty"`
	peak           decimal.Decimal
	maxDrawdown    decimal.Decimal
	maxDrawdownPct decimal.Decimal
	initial        decimal.Decimal
	asset          string
	position       decimal.Decimal
	averagePrice   decimal.Decimal
	realized       decimal.Decimal
	grossProfit    decimal.Decimal
	grossLoss      decimal.Decimal
	closedTrades   uint64
	winningTrades  uint64
	losingTrades   uint64
	firstEventAt   time.Time
	lastEventAt    time.Time
	positionSince  time.Time
	exposure       time.Duration
}

func NewReport(config BrokerConfig) Report {
	initial := money(config.InitialCash)
	return Report{
		SchemaVersion: ReportSchemaVersion,
		Metrics: Metrics{InitialEquity: initial, FinalEquity: initial, TotalPnL: DecimalMoney{Amount: "0", Asset: config.InitialCash.Asset},
			ReturnPercent: "0", Commissions: DecimalMoney{Amount: "0", Asset: config.InitialCash.Asset},
			SlippageCost: DecimalMoney{Amount: "0", Asset: config.InitialCash.Asset},
			MaxDrawdown:  DecimalMoney{Amount: "0", Asset: config.InitialCash.Asset}, MaxDrawdownPct: "0"},
		Assumptions: Assumptions{FillModel: FillModelVersion, CommissionUnit: "percent (0.03 means 0.03%)",
			SlippageUnit: "basis_points", PartialFills: false, VolumeLimitsFills: false,
			ForceCloseAtEnd: false, ShortSelling: false},
		Warnings: []string{
			"Historical results do not guarantee future performance.",
			"Candle fills do not model exchange queues or intrabar price ordering.",
			"Open positions are marked to the final close and are not force-closed.",
			"Short selling is disabled; sell orders exceeding the current long position are rejected.",
		},
		peak: config.InitialCash.Amount, initial: config.InitialCash.Amount, asset: config.InitialCash.Asset,
	}
}

func (r *Report) Observe(event domain.MarketEvent, executions []domain.Execution, snapshot PortfolioSnapshot) {
	at := event.ExchangeTime.UTC()
	if r.firstEventAt.IsZero() {
		r.firstEventAt = at
	}
	if !r.position.IsZero() && !r.lastEventAt.IsZero() && at.After(r.lastEventAt) {
		r.exposure += at.Sub(r.lastEventAt)
	}
	for _, execution := range executions {
		signed := execution.Quantity.Value
		if execution.Side == domain.OrderSideSell {
			signed = signed.Neg()
		}
		before := r.position
		closing := decimal.Zero
		if !before.IsZero() && before.Sign() != signed.Sign() {
			closing = decimal.Min(before.Abs(), signed.Abs())
			tradePnL := decimal.Zero
			if before.IsPositive() {
				tradePnL = execution.Price.Value.Sub(r.averagePrice).Mul(closing)
			} else {
				tradePnL = r.averagePrice.Sub(execution.Price.Value).Mul(closing)
			}
			r.realized = r.realized.Add(tradePnL)
			r.closedTrades++
			if tradePnL.IsPositive() {
				r.winningTrades++
				r.grossProfit = r.grossProfit.Add(tradePnL)
			} else if tradePnL.IsNegative() {
				r.losingTrades++
				r.grossLoss = r.grossLoss.Add(tradePnL.Abs())
			}
		}
		remaining := before.Add(signed)
		switch {
		case before.IsZero() || before.Sign() == signed.Sign():
			total := before.Abs().Add(signed.Abs())
			if before.IsZero() {
				r.averagePrice = execution.Price.Value
			} else {
				r.averagePrice = r.averagePrice.Mul(before.Abs()).Add(execution.Price.Value.Mul(signed.Abs())).Div(total)
			}
		case remaining.IsZero():
			r.averagePrice = decimal.Zero
		case remaining.Sign() != before.Sign():
			r.averagePrice = execution.Price.Value
		}
		r.position = remaining
	}
	r.lastEventAt = at
	r.ObserveEquity(snapshot.Equity)
}

func (r *Report) ObserveEquity(equity domain.Money) {
	if equity.Asset != r.asset {
		return
	}
	if equity.Amount.GreaterThan(r.peak) {
		r.peak = equity.Amount
	}
	drawdown := r.peak.Sub(equity.Amount)
	if drawdown.GreaterThan(r.maxDrawdown) {
		r.maxDrawdown = drawdown
		if r.peak.IsPositive() {
			r.maxDrawdownPct = drawdown.Div(r.peak).Mul(decimal.NewFromInt(100))
		}
	}
}

func (r *Report) Finalize(snapshot PortfolioSnapshot) {
	pnl := snapshot.Equity.Amount.Sub(r.initial)
	returnPct := decimal.Zero
	if !r.initial.IsZero() {
		returnPct = pnl.Div(r.initial).Mul(decimal.NewFromInt(100))
	}
	r.Metrics.FinalEquity = money(snapshot.Equity)
	r.Metrics.TotalPnL = DecimalMoney{Amount: pnl.String(), Asset: r.asset}
	unrealized := decimal.Zero
	if snapshot.LastPrice != nil && !r.position.IsZero() {
		unrealized = snapshot.LastPrice.Value.Sub(r.averagePrice).Mul(r.position)
	}
	r.Metrics.RealizedPnL = DecimalMoney{Amount: r.realized.String(), Asset: r.asset}
	r.Metrics.UnrealizedPnL = DecimalMoney{Amount: unrealized.String(), Asset: r.asset}
	r.Metrics.ReturnPercent = returnPct.String()
	r.Metrics.Commissions = money(snapshot.Commissions)
	r.Metrics.SlippageCost = money(snapshot.SlippageCost)
	r.Metrics.MaxDrawdown = DecimalMoney{Amount: r.maxDrawdown.String(), Asset: r.asset}
	r.Metrics.MaxDrawdownPct = r.maxDrawdownPct.String()
	r.Metrics.Orders = uint64(len(r.Orders))
	r.Metrics.Fills = uint64(len(r.Executions))
	r.Metrics.ClosedTrades = r.closedTrades
	r.Metrics.WinningTrades = r.winningTrades
	r.Metrics.LosingTrades = r.losingTrades
	if r.closedTrades > 0 {
		r.Metrics.WinRate = decimal.NewFromInt(int64(r.winningTrades)).Div(decimal.NewFromInt(int64(r.closedTrades))).Mul(decimal.NewFromInt(100)).String()
	} else {
		r.Metrics.WinRate = "0"
	}
	r.Metrics.GrossProfit = DecimalMoney{Amount: r.grossProfit.String(), Asset: r.asset}
	r.Metrics.GrossLoss = DecimalMoney{Amount: r.grossLoss.String(), Asset: r.asset}
	if r.grossLoss.IsZero() {
		if r.grossProfit.IsPositive() {
			r.Metrics.ProfitFactor = "unbounded"
		} else {
			r.Metrics.ProfitFactor = "0"
		}
	} else {
		r.Metrics.ProfitFactor = r.grossProfit.Div(r.grossLoss).String()
	}
	r.Metrics.ExposureTime = r.exposure.String()
	elapsed := r.lastEventAt.Sub(r.firstEventAt)
	if elapsed > 0 {
		r.Metrics.ExposurePct = decimal.NewFromInt(r.exposure.Nanoseconds()).Div(decimal.NewFromInt(elapsed.Nanoseconds())).Mul(decimal.NewFromInt(100)).String()
	} else {
		r.Metrics.ExposurePct = "0"
	}
}

func (r Report) MarshalJSON() ([]byte, error) {
	type wire Report
	r.peak, r.maxDrawdown, r.maxDrawdownPct, r.initial = decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	return json.Marshal(wire(r))
}

func money(value domain.Money) DecimalMoney {
	return DecimalMoney{Amount: value.Amount.String(), Asset: value.Asset}
}
