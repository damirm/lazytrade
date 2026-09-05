package cli

import (
	"testing"
	"time"
)

func TestParseDownloadBoundary(t *testing.T) {
	t.Parallel()
	value, err := parseBoundary("2025-01-02")
	if err != nil || !value.Equal(time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseBoundary() = %v, %v", value, err)
	}
	value, err = parseBoundary("2025-01-02T03:04:05+03:00")
	if err != nil || value.Location() != time.UTC {
		t.Fatalf("parseBoundary() = %v, %v", value, err)
	}
}

func TestParseDownloadInterval(t *testing.T) {
	t.Parallel()
	if value, err := parseDownloadInterval("1m"); err != nil || value != time.Minute {
		t.Fatalf("parseDownloadInterval() = %v, %v", value, err)
	}
	if _, err := parseDownloadInterval("2m"); err == nil {
		t.Fatal("expected unsupported interval error")
	}
}
