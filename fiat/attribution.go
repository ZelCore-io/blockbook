package fiat

import "strings"

// RatesSourceCoingecko names Coingecko as the source of the exchange rates. It is the credit
// whenever fiat_rates_params carries no "attribution".
const RatesSourceCoingecko = "Coingecko"

// ratesSource returns who to credit for the exchange rates: the optional fiat_rates_params
// "attribution" value (whitespace collapsed), else the upstream, Coingecko. The credit never
// depends on the configured URLs.
func ratesSource(attribution string) string {
	if a := strings.Join(strings.Fields(attribution), " "); a != "" {
		return a
	}
	return RatesSourceCoingecko
}

// RatesSource returns the name to credit for the exchange rates (the configured attribution, else
// "Coingecko"), or an empty string when fiat rates are not configured.
func (fr *FiatRates) RatesSource() string {
	if fr == nil {
		return ""
	}
	return fr.ratesSource
}
