//go:build unittest

package fiat

import "testing"

func Test_ratesSource(t *testing.T) {
	tests := []struct {
		name string
		urls []string
		want string
	}{
		{"defaults", []string{"", ""}, RatesSourceCoingecko},
		{"no host", []string{"none", ""}, RatesSourceCoingecko},
		{"trezor bootstrap and coingecko tip", []string{"https://cdn.trezor.io/dynamic/coingecko/api/v3", "https://api.coingecko.com/api/v3"}, RatesSourceCoingecko},
		{"coingecko pro", []string{"", "https://pro-api.coingecko.com/api/v3"}, RatesSourceCoingecko},
		{"relay", []string{"https://rates.example.com/api/v3", "https://rates.example.com/api/v3"}, RatesSourceZelCore},
		{"relay tip only", []string{"", "https://rates.example.com/api/v3"}, RatesSourceZelCore},
		{"look-alike host", []string{"https://coingecko.com.example.net/api/v3", ""}, RatesSourceZelCore},
		{"upper case", []string{"https://API.CoinGecko.com/api/v3", ""}, RatesSourceCoingecko},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ratesSource(tt.urls...); got != tt.want {
				t.Errorf("ratesSource(%q) = %q, want %q", tt.urls, got, tt.want)
			}
		})
	}
	var nilRates *FiatRates
	if got := nilRates.RatesSource(); got != "" {
		t.Errorf("nil FiatRates: %q", got)
	}
}
