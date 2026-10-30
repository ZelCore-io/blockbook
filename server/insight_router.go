package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// insightRoute is one matched Insight request. kind names the handler.
// addresses holds the path or ?address= value: a single raw entry for the
// single-address kinds, the comma-split path segment for the multi kinds.
// arg is the route's path argument (hash, txid, height, address sub-value or
// the converter's trailing noTxList segment). from/to are history offsets from
// the path or the query. body is the POST body, filled after admission; a
// form-urlencoded body is normalized to a JSON object of string values so
// handlers decode JSON and form bodies the same way.
type insightRoute struct {
	kind      string
	addresses []string
	arg       string
	from, to  string
	query     url.Values
	body      []byte
	// ctx is the request context (nil when a test calls handle directly).
	ctx context.Context
}

// gone reports whether the client has gone away (request context done).
func (route insightRoute) gone() error {
	if route.ctx == nil {
		return nil
	}
	return route.ctx.Err()
}

var (
	insightGet     = []string{http.MethodGet}
	insightPost    = []string{http.MethodPost}
	insightGetPost = []string{http.MethodGet, http.MethodPost}
)

// insightSingleAddressKinds are the routes that accept exactly one address.
var insightSingleAddressKinds = map[string]bool{"summary": true, "utxo": true, "history": true, "addrvalue": true}

var insightAddrValues = map[string]bool{"balance": true, "totalReceived": true, "totalSent": true, "unconfirmedBalance": true}

// insightMatch maps a request path to a route and the methods that route
// allows. The first segment must be "api" or "insight-api"; the rest is
// matched identically for both prefixes.
func insightMatch(r *http.Request) (insightRoute, []string, bool) {
	parts, ok := insightPathParts(r)
	if !ok {
		return insightRoute{}, nil, false
	}
	n := len(parts)
	query := r.URL.Query()
	route := insightRoute{query: query}
	methods := insightGet
	single := ""
	switch parts[0] {
	case "status", "sync", "peer", "version", "blocks", "txs", "currency":
		if n == 1 {
			route.kind = parts[0]
		}
	case "block", "rawblock", "rawtx":
		if n == 2 {
			route.kind, route.arg = parts[0], parts[1]
		}
	case "block-index":
		if n == 2 {
			route.kind, route.arg = "blockindex", parts[1]
		}
	case "tx":
		if n == 2 && parts[1] == "send" {
			route.kind, methods = "send", insightPost
		} else if n == 2 {
			route.kind, route.arg = "tx", parts[1]
		}
	case "utils":
		if n == 2 && parts[1] == "estimatefee" {
			route.kind = "estimatefee"
		}
	case "messages":
		if n == 2 && parts[1] == "verify" {
			route.kind, methods = "verify", insightGetPost
		}
	case "statistics":
		// Only circulating-supply; the other stock statistics/* routes
		// (and markets/*) are not served and stay the Insight 404.
		if n == 2 && parts[1] == "circulating-supply" {
			route.kind = "circulatingsupply"
		}
	case "addr":
		switch {
		case n == 1:
			route.kind, single = "summary", query.Get("address")
		case n == 2 && parts[1] == "utxo":
			route.kind, single = "utxo", query.Get("address")
		case n == 2:
			route.kind, single = "summary", parts[1]
		case n == 3 && parts[2] == "utxo":
			route.kind, single = "utxo", parts[1]
		case n == 3 && insightAddrValues[parts[2]]:
			route.kind, single, route.arg = "addrvalue", parts[1], parts[2]
		case n == 3:
			route.kind, single, route.arg = "summary", parts[1], parts[2]
		}
	case "addrs":
		switch {
		case n == 2 && parts[1] == "utxo":
			route.kind, methods = "multiutxo", insightPost
		case n == 2 && parts[1] == "txs" && r.Method == http.MethodPost:
			route.kind, methods = "multitxs", insightGetPost
		case n >= 2 && n <= 4 && parts[1] == "txs":
			// addrs/txs[/:from[/:to]]?address=: the converter's query form.
			route.kind, single = "history", query.Get("address")
			if n == 2 {
				methods = insightGetPost
			}
			if n > 2 {
				route.from = parts[2]
			}
			if n > 3 {
				route.to = parts[3]
			}
		case n == 3 && parts[2] == "utxo":
			route.kind, route.addresses = "multiutxo", strings.Split(parts[1], ",")
		case n == 3 && parts[2] == "unspent":
			route.kind, route.addresses = "multiunspent", strings.Split(parts[1], ",")
		case n == 3 && parts[2] == "balance":
			route.kind, route.addresses = "multibalance", strings.Split(parts[1], ",")
		case n >= 3 && n <= 5 && parts[2] == "txs":
			// A comma makes it the merged multi-address history.
			if strings.Contains(parts[1], ",") {
				route.kind, route.addresses = "multitxs", strings.Split(parts[1], ",")
			} else {
				route.kind, single = "history", parts[1]
			}
			if n > 3 {
				route.from = parts[3]
			}
			if n > 4 {
				route.to = parts[4]
			}
		}
	}
	if route.kind == "" {
		return insightRoute{}, nil, false
	}
	if insightSingleAddressKinds[route.kind] && single != "" {
		route.addresses = []string{single}
	}
	if route.from == "" {
		route.from = query.Get("from")
	}
	if route.to == "" {
		route.to = query.Get("to")
	}
	return route, methods, true
}

// insightPathParts returns the path segments after the "api" or
// "insight-api" prefix (insightSegments).
func insightPathParts(r *http.Request) ([]string, bool) {
	parts := insightSegments(r)
	if len(parts) < 3 || parts[0] != "" || (parts[1] != "api" && parts[1] != "insight-api") {
		return nil, false
	}
	return parts[2:], true
}

// insightSegments splits the request path on "/" after dropping one trailing
// slash. Empty segments right after a leading "api" or "insight-api" prefix
// are dropped, so /api//addr/A is /api/addr/A: stock serves that form
// (Express mounts the API router with an optional slash after the prefix, and
// about 5% of ZCL's stock traffic uses it). Only those empty segments go.
// Everywhere else an empty segment stays an empty argument (/api/addr//balance
// is a 400, not the address "balance"), "." and ".." stay ordinary segments
// that match no route, and the request itself is unchanged, so a fallthrough
// to the -insightui handler sees the path as sent.
func insightSegments(r *http.Request) []string {
	parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	if len(parts) > 3 && parts[0] == "" && (parts[1] == "api" || parts[1] == "insight-api") && parts[2] == "" {
		i := 2
		for i < len(parts)-1 && parts[i] == "" {
			i++
		}
		parts = append(parts[:2], parts[i:]...)
	}
	return parts
}

// insightOwnsPath reports whether an unmatched path still belongs to the
// Insight API, so it gets the Insight 404 (with CORS headers) instead of the
// -insightui fallback, as on stock: everything under /api/ and /insight-api/
// (and those prefixes themselves) except Blockbook's versioned APIs /api/v1/
// and /api/v2/, which the explorer UI and Blockbook clients use.
func insightOwnsPath(r *http.Request) bool {
	parts := insightSegments(r)
	if len(parts) < 2 || parts[0] != "" {
		return false
	}
	switch parts[1] {
	case "insight-api":
		return true
	case "api":
		return len(parts) < 3 || (parts[2] != "v1" && parts[2] != "v2")
	}
	return false
}

// match is insightMatch plus the routes of the coin's extension, if any.
func (s *InsightServer) match(r *http.Request) (insightRoute, []string, bool) {
	route, methods, ok := insightMatch(r)
	if ok || s.ext == nil {
		return route, methods, ok
	}
	parts, ok := insightPathParts(r)
	if !ok {
		return insightRoute{}, nil, false
	}
	kind, arg, methods, ok := s.ext.match(parts)
	if !ok {
		return insightRoute{}, nil, false
	}
	return insightRoute{kind: kind, arg: arg, query: r.URL.Query()}, methods, true
}

// insightSingleAddress returns the one address of a single-address route.
func insightSingleAddress(route insightRoute) (string, bool) {
	if len(route.addresses) != 1 || route.addresses[0] == "" || strings.Contains(route.addresses[0], ",") {
		return "", false
	}
	return route.addresses[0], true
}

func insightAllowed(methods []string, method string) bool {
	for _, m := range methods {
		if m == method {
			return true
		}
	}
	return false
}

// insightReadBody reads a POST body under insightBodyLimit. It returns 413 for
// an oversized body and 400 for an unreadable or malformed form body.
func insightReadBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, insightBodyLimit))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return nil, http.StatusRequestEntityTooLarge, err
		}
		return nil, http.StatusBadRequest, err
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/x-www-form-urlencoded" {
		return body, 0, nil
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	fields := make(map[string]string, len(values))
	for k, v := range values {
		if len(v) > 0 {
			fields[k] = v[0]
		}
	}
	body, err = json.Marshal(fields)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	return body, 0, nil
}
