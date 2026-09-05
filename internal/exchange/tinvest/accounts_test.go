package tinvest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func assertUnknownMutationOutcome(t *testing.T, err error) {
	t.Helper()
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) || exchangeErr.Category != exchange.ErrorUnknownOutcome ||
		exchangeErr.Outcome != exchange.OutcomeUnknown || exchangeErr.Retryable {
		t.Fatalf("mutation error = %T %#v", err, err)
	}
}

func TestCreateSandboxAccountReturnsAccountID(t *testing.T) {
	stub := &sandboxStub{openResponse: &pb.OpenSandboxAccountResponse{AccountId: "new-account"}}
	adapter := orderTestAdapter(stub)
	accountID, err := adapter.CreateSandboxAccount(context.Background(), "Lazytrade")
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "new-account" || stub.openRequest.GetName() != "Lazytrade" {
		t.Fatalf("account ID=%q request=%+v", accountID, stub.openRequest)
	}
}

func TestCreateSandboxAccountMalformedResponseHasUnknownOutcome(t *testing.T) {
	stub := &sandboxStub{openResponse: &pb.OpenSandboxAccountResponse{}}
	adapter := orderTestAdapter(stub)
	_, err := adapter.CreateSandboxAccount(context.Background(), "Lazytrade")
	assertUnknownMutationOutcome(t, err)
}

func TestPayInSandboxUsesRUBAndReturnsBalance(t *testing.T) {
	stub := &sandboxStub{payInResponse: &pb.SandboxPayInResponse{
		Balance: &pb.MoneyValue{Units: 100000, Currency: "rub"},
	}}
	adapter := orderTestAdapter(stub)
	balance, err := adapter.PayInSandbox(context.Background(), "account-id", domain.Money{
		Amount: decimal.RequireFromString("100000.25"), Asset: "RUB",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.payInRequest.GetAccountId() != "account-id" ||
		stub.payInRequest.GetAmount().GetUnits() != 100000 ||
		stub.payInRequest.GetAmount().GetNano() != 250000000 {
		t.Fatalf("pay-in request = %+v", stub.payInRequest)
	}
	if balance.Asset != "RUB" || !balance.Amount.Equal(decimal.NewFromInt(100000)) {
		t.Fatalf("balance = %+v", balance)
	}
}

func TestPayInSandboxMalformedResponseHasUnknownOutcome(t *testing.T) {
	stub := &sandboxStub{payInResponse: &pb.SandboxPayInResponse{}}
	adapter := orderTestAdapter(stub)
	_, err := adapter.PayInSandbox(context.Background(), "account-id", domain.Money{
		Amount: decimal.NewFromInt(1), Asset: "RUB",
	})
	assertUnknownMutationOutcome(t, err)
}

func TestPayInSandboxRejectsNonRUBBeforeAPI(t *testing.T) {
	stub := &sandboxStub{}
	adapter := orderTestAdapter(stub)
	_, err := adapter.PayInSandbox(context.Background(), "account-id", domain.Money{
		Amount: decimal.NewFromInt(1), Asset: "USD",
	})
	if err == nil || stub.payInRequest != nil {
		t.Fatalf("error=%v request=%+v", err, stub.payInRequest)
	}
}

func TestSandboxAccountsMapsIdentifiersAndAccess(t *testing.T) {
	opened := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	stub := &sandboxStub{accountsResponse: &pb.GetAccountsResponse{Accounts: []*pb.Account{{
		Id: "account-id", Name: "Sandbox",
		Type:        pb.AccountType_ACCOUNT_TYPE_TINKOFF,
		Status:      pb.AccountStatus_ACCOUNT_STATUS_OPEN,
		AccessLevel: pb.AccessLevel_ACCOUNT_ACCESS_LEVEL_FULL_ACCESS,
		OpenedDate:  timestamppb.New(opened),
	}}}}
	adapter := orderTestAdapter(stub)
	accounts, err := adapter.SandboxAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "account-id" ||
		accounts[0].Status != "OPEN" || accounts[0].AccessLevel != "FULL_ACCESS" ||
		accounts[0].OpenedAt == nil || !accounts[0].OpenedAt.Equal(opened) {
		t.Fatalf("accounts = %+v", accounts)
	}
}
