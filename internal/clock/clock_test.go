package clock

import (
	"errors"
	"testing"
	"time"
)

func TestClockImplementationsReturnUTC(t *testing.T) {
	input := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3*60*60))
	clocks := []struct {
		name  string
		clock Clock
	}{
		{"fixed", NewFixed(input)},
		{"virtual", NewVirtual(input)},
		{"system", SystemClock{}},
	}
	for _, tt := range clocks {
		t.Run(tt.name, func(t *testing.T) {
			if tt.clock.Now().Location() != time.UTC {
				t.Fatalf("location = %v, want UTC", tt.clock.Now().Location())
			}
		})
	}
}

func TestVirtualClockAdvanceTo(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name    string
		target  time.Time
		wantErr error
		want    time.Time
	}{
		{name: "forward", target: start.Add(time.Minute), want: start.Add(time.Minute)},
		{name: "same instant", target: start, want: start},
		{name: "backwards", target: start.Add(-time.Nanosecond), wantErr: ErrTimeBackwards, want: start},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := NewVirtual(start)
			err := clock.AdvanceTo(tt.target)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AdvanceTo error = %v, want %v", err, tt.wantErr)
			}
			if got := clock.Now(); !got.Equal(tt.want) {
				t.Fatalf("Now = %v, want %v", got, tt.want)
			}
		})
	}
}
