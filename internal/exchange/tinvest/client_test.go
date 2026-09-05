package tinvest

import (
	"testing"

	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestSandboxEndpointUsesCurrentTBankHost(t *testing.T) {
	const want = "sandbox-invest-public-api.tbank.ru:443"
	if sandboxEndpoint != want {
		t.Fatalf("sandbox endpoint = %q, want %q", sandboxEndpoint, want)
	}
}

func TestPortfolioMapperExcludesCashPositions(t *testing.T) {
	if includePortfolioPosition(&pb.PortfolioPosition{InstrumentType: "currency"}) {
		t.Fatal("cash position was included")
	}
	if !includePortfolioPosition(&pb.PortfolioPosition{InstrumentType: "share"}) {
		t.Fatal("security position was excluded")
	}
}
