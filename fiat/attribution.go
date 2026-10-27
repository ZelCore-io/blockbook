package fiat

import (
	"net/url"
	"strings"
)

const (
	// RatesSourceCoingecko names Coingecko as the source of the exchange rates.
	RatesSourceCoingecko = "Coingecko"
	// RatesSourceZelCore names ZelCore as the source: the rates come through a relay that is not a
	// Coingecko or Trezor host (fiat_rates_params "url" or "tipUrl").
	RatesSourceZelCore = "ZelCore"
)

// ratesSource returns who provides the exchange rates, derived from the fiat_rates_params URLs.
// Empty URLs fall back to the Coingecko API and the Trezor bootstrap CDN, so they count as
// Coingecko; so does a value without a host (such as "none").
func ratesSource(urls ...string) string {
	for _, u := range urls {
		p, err := url.Parse(strings.TrimSpace(u))
		if err != nil {
			continue
		}
		host := strings.ToLower(p.Hostname())
		if host == "" || isCoingeckoUpstreamHost(host) {
			continue
		}
		return RatesSourceZelCore
	}
	return RatesSourceCoingecko
}

func isCoingeckoUpstreamHost(host string) bool {
	return host == "coingecko.com" || strings.HasSuffix(host, ".coingecko.com") || host == "cdn.trezor.io"
}

// RatesSource returns the name to credit for the exchange rates ("Coingecko" or "ZelCore"), or an
// empty string when fiat rates are not configured.
func (fr *FiatRates) RatesSource() string {
	if fr == nil {
		return ""
	}
	return fr.ratesSource
}
