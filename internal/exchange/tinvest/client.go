package tinvest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

const sandboxEndpoint = "sandbox-invest-public-api.tbank.ru:443"

type Config struct {
	Name, Token, AccountID, Endpoint, AppName, CACertPath string
	UnaryTimeout                                          time.Duration
}

type Adapter struct {
	name, accountID string
	timeout         time.Duration
	conn            *grpc.ClientConn
	instruments     pb.InstrumentsServiceClient
	market          pb.MarketDataServiceClient
	marketStream    pb.MarketDataStreamServiceClient
	sandbox         sandboxService
	orders          ordersService
	operations      operationsService
	orderStream     tradesOpener
	metadataMu      sync.RWMutex
	metadata        map[domain.InstrumentID]domain.Instrument
	orderContextMu  sync.RWMutex
	orderContexts   map[domain.OrderID]executionOrderContext
	clientContexts  map[domain.ClientOrderID]executionOrderContext
	readRetry       readRetryPolicy
}

func Open(ctx context.Context, cfg Config, opts ...grpc.DialOption) (*Adapter, error) {
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("T-Invest name and token are required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = sandboxEndpoint
	}
	if endpoint != sandboxEndpoint {
		return nil, fmt.Errorf("production endpoint is disabled: %s", endpoint)
	}
	rootCAs, err := loadRootCAs(cfg.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("T-Invest CA certificates: %w", err)
	}
	tlsCredentials := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
	})
	opts = append(opts, grpc.WithTransportCredentials(tlsCredentials), grpc.WithPerRPCCredentials(
		oauth.TokenSource{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})},
	))
	conn, err := grpc.DialContext(ctx, endpoint, opts...)
	if err != nil {
		return nil, mapError("connect", err)
	}
	timeout := cfg.UnaryTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	a := &Adapter{name: cfg.Name, accountID: cfg.AccountID, timeout: timeout, conn: conn,
		metadata:      make(map[domain.InstrumentID]domain.Instrument),
		orderContexts: make(map[domain.OrderID]executionOrderContext)}
	a.clientContexts = make(map[domain.ClientOrderID]executionOrderContext)
	a.instruments = pb.NewInstrumentsServiceClient(conn)
	a.market = pb.NewMarketDataServiceClient(conn)
	a.marketStream = pb.NewMarketDataStreamServiceClient(conn)
	a.sandbox = pb.NewSandboxServiceClient(conn)
	a.orders = pb.NewOrdersServiceClient(conn)
	a.operations = pb.NewOperationsServiceClient(conn)
	a.orderStream = grpcTradesOpener{client: pb.NewOrdersStreamServiceClient(conn)}
	_ = ctx
	return a, nil
}

func (a *Adapter) Close() error { return a.conn.Close() }
func (a *Adapter) Name() string { return a.name }
func (a *Adapter) Capabilities() exchange.Capabilities {
	return exchange.Capabilities{OrderBook: true, StreamingCandles: true, StreamingTrades: true, StreamingLastPrice: true, Sandbox: true}
}

func (a *Adapter) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, a.timeout)
}

func (a *Adapter) readRetryPolicy() readRetryPolicy {
	if a.readRetry.MaxAttempts <= 0 {
		return defaultReadRetryPolicy
	}
	return a.readRetry
}

func (a *Adapter) Instruments(ctx context.Context) ([]domain.Instrument, error) {
	status := pb.InstrumentStatus_INSTRUMENT_STATUS_BASE
	request := &pb.InstrumentsRequest{InstrumentStatus: &status}
	resp, err := retryRead(ctx, "list instruments", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.SharesResponse, error) {
		return a.instruments.Shares(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.Instrument, 0, len(resp.Instruments))
	for _, item := range resp.Instruments {
		// The BASE catalogue can contain informational/non-tradable records
		// without the metadata required to validate and round orders.
		if item.GetMinPriceIncrement() == nil || item.GetLot() <= 0 {
			continue
		}
		mapped, mapErr := mapShare(domain.ExchangeAccountID(a.name), item)
		if mapErr != nil {
			return nil, fmt.Errorf("map instrument %q: %w", item.GetUid(), mapErr)
		}
		result = append(result, mapped)
		a.storeMetadata(mapped)
	}
	return result, nil
}

// Instrument resolves full metadata by any identifier accepted by T-Invest
// (UID, FIGI, ticker_class-code, or position UID).
func (a *Adapter) Instrument(ctx context.Context, id domain.InstrumentID) (domain.Instrument, error) {
	if err := id.Validate(); err != nil {
		return domain.Instrument{}, fmt.Errorf("instrument ID: %w", err)
	}
	if cached, ok := a.cachedMetadata(id); ok {
		return cached, nil
	}
	request := &pb.InstrumentRequest{
		IdType: pb.InstrumentIdType_INSTRUMENT_ID_TYPE_ID,
		Id:     string(id),
	}
	resp, err := retryRead(ctx, "get instrument", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.InstrumentResponse, error) {
		return a.instruments.GetInstrumentBy(callCtx, request)
	})
	if err != nil {
		return domain.Instrument{}, err
	}
	result, err := mapInstrument(domain.ExchangeAccountID(a.name), resp.Instrument)
	if err != nil {
		return domain.Instrument{}, fmt.Errorf("map instrument %q: %w", id, err)
	}
	a.storeMetadata(result)
	return result, nil
}

func (a *Adapter) cachedMetadata(id domain.InstrumentID) (domain.Instrument, bool) {
	a.metadataMu.RLock()
	defer a.metadataMu.RUnlock()
	instrument, ok := a.metadata[id]
	return instrument, ok
}

func (a *Adapter) storeMetadata(instrument domain.Instrument) {
	a.metadataMu.Lock()
	defer a.metadataMu.Unlock()
	a.metadata[instrument.ID] = instrument
}

func (a *Adapter) Portfolio(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.Portfolio, error) {
	if err := a.validateAccount(accountID); err != nil {
		return exchange.Portfolio{}, err
	}
	request := &pb.PortfolioRequest{AccountId: a.accountID}
	resp, err := retryRead(ctx, "get portfolio", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.PortfolioResponse, error) {
		return a.operations.GetPortfolio(callCtx, request)
	})
	if err != nil {
		return exchange.Portfolio{}, err
	}
	result := exchange.Portfolio{AccountID: accountID, AsOf: time.Now().UTC()}
	if resp.TotalAmountPortfolio != nil {
		total, mapErr := money(resp.TotalAmountPortfolio)
		if mapErr != nil {
			return result, mapErr
		}
		result.TotalValue = []domain.Money{total}
	}
	for _, item := range resp.Positions {
		// Cash is part of the broker portfolio but not a strategy-owned
		// instrument position. It is represented separately by TotalValue.
		if !includePortfolioPosition(item) {
			continue
		}
		qty, mapErr := quotation(item.Quantity)
		if mapErr != nil {
			return result, mapErr
		}
		pos := exchange.Position{InstrumentID: domain.InstrumentID(item.InstrumentUid), Quantity: domain.Quantity{Value: qty}}
		if item.AveragePositionPrice != nil {
			p, priceErr := price(&pb.Quotation{Units: item.AveragePositionPrice.Units, Nano: item.AveragePositionPrice.Nano}, item.AveragePositionPrice.Currency)
			if priceErr != nil {
				return result, priceErr
			}
			pos.AveragePrice = &p
		}
		result.Positions = append(result.Positions, pos)
	}
	return result, nil
}

func includePortfolioPosition(position *pb.PortfolioPosition) bool {
	return position != nil && !strings.EqualFold(position.GetInstrumentType(), "currency")
}

var _ exchange.Exchange = (*Adapter)(nil)

// loadRootCAs returns nil when no additional CA path is configured, allowing
// crypto/tls to use the platform roots directly. A configured path augments,
// rather than replaces, the platform trust store.
func loadRootCAs(path string) (*x509.CertPool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	count, err := appendCertificates(pool, path)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("%q contains no certificates", path)
	}
	return pool, nil
}

func appendCertificates(pool *x509.CertPool, path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("%q is not a regular file or directory", path)
		}
		if err := appendCertificateFile(pool, path); err != nil {
			return 0, err
		}
		return 1, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, fmt.Errorf("read directory %q: %w", path, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	count := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		filePath := filepath.Join(path, entry.Name())
		if err := appendCertificateFile(pool, filePath); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

func appendCertificateFile(pool *x509.CertPool, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	if pool.AppendCertsFromPEM(data) {
		return nil
	}
	certificate, err := x509.ParseCertificate(data)
	if err != nil {
		return fmt.Errorf("parse %q as PEM or DER certificate: %w", path, err)
	}
	pool.AddCert(certificate)
	return nil
}
