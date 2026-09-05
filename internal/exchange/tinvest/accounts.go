package tinvest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type SandboxAccount struct {
	ID          string
	Name        string
	Type        string
	Status      string
	AccessLevel string
	OpenedAt    *time.Time
}

func (a *Adapter) CreateSandboxAccount(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("sandbox account name is required")
	}
	cctx, cancel := a.timeoutContext(ctx)
	defer cancel()
	response, err := a.sandbox.OpenSandboxAccount(cctx, &pb.OpenSandboxAccountRequest{Name: &name})
	if err != nil {
		return "", mapMutationError("create sandbox account", err)
	}
	if strings.TrimSpace(response.GetAccountId()) == "" {
		return "", mutationResponseError("map created sandbox account response", errors.New("response contains an empty account ID"))
	}
	return response.GetAccountId(), nil
}

func (a *Adapter) PayInSandbox(ctx context.Context, accountID string, amount domain.Money) (domain.Money, error) {
	if strings.TrimSpace(accountID) == "" {
		return domain.Money{}, errors.New("sandbox account ID is required")
	}
	if err := amount.Validate(); err != nil {
		return domain.Money{}, fmt.Errorf("sandbox pay-in amount: %w", err)
	}
	if amount.Asset != "RUB" || !amount.Amount.IsPositive() {
		return domain.Money{}, errors.New("sandbox pay-in must be a positive RUB amount")
	}
	value := decimalQuotation(amount.Amount)
	cctx, cancel := a.timeoutContext(ctx)
	defer cancel()
	response, err := a.sandbox.SandboxPayIn(cctx, &pb.SandboxPayInRequest{
		AccountId: accountID,
		Amount:    &pb.MoneyValue{Units: value.Units, Nano: value.Nano, Currency: "rub"},
	})
	if err != nil {
		return domain.Money{}, mapMutationError("pay in sandbox account", err)
	}
	balance, err := money(response.GetBalance())
	if err != nil {
		return domain.Money{}, mutationResponseError("map sandbox pay-in response", fmt.Errorf("sandbox balance: %w", err))
	}
	if balance.Asset != amount.Asset {
		return domain.Money{}, mutationResponseError("map sandbox pay-in response", fmt.Errorf("sandbox balance asset is %q, want %q", balance.Asset, amount.Asset))
	}
	return balance, nil
}

func (a *Adapter) SandboxAccounts(ctx context.Context) ([]SandboxAccount, error) {
	request := &pb.GetAccountsRequest{}
	response, err := retryRead(ctx, "list sandbox accounts", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetAccountsResponse, error) {
		return a.sandbox.GetSandboxAccounts(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	result := make([]SandboxAccount, 0, len(response.GetAccounts()))
	for _, account := range response.GetAccounts() {
		item := SandboxAccount{
			ID: account.GetId(), Name: account.GetName(),
			Type:        enumSuffix(account.GetType().String(), "ACCOUNT_TYPE_"),
			Status:      enumSuffix(account.GetStatus().String(), "ACCOUNT_STATUS_"),
			AccessLevel: enumSuffix(account.GetAccessLevel().String(), "ACCOUNT_ACCESS_LEVEL_"),
		}
		if account.GetOpenedDate() != nil {
			opened := account.GetOpenedDate().AsTime().UTC()
			item.OpenedAt = &opened
		}
		result = append(result, item)
	}
	return result, nil
}

func enumSuffix(value, prefix string) string {
	if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
		return value[len(prefix):]
	}
	return value
}
