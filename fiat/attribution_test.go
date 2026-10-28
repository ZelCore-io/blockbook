//go:build unittest

package fiat

import "testing"

func Test_ratesSource(t *testing.T) {
	tests := []struct {
		name        string
		attribution string
		want        string
	}{
		{"absent", "", RatesSourceCoingecko},
		{"blank", "  \t ", RatesSourceCoingecko},
		{"explicit", "ZelCore", "ZelCore"},
		{"trimmed and collapsed", "  Example   Rates \n", "Example Rates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ratesSource(tt.attribution); got != tt.want {
				t.Errorf("ratesSource(%q) = %q, want %q", tt.attribution, got, tt.want)
			}
		})
	}
	var nilRates *FiatRates
	if got := nilRates.RatesSource(); got != "" {
		t.Errorf("nil FiatRates: %q", got)
	}
}
