//go:build unittest

package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

func TestInsightEstimateFee(t *testing.T) {
	f := insightFixture()
	f.fees = map[int]int64{2: 12345, 3: 2000}
	s := insightTestServer(t, f)

	for _, prefix := range []string{"/api", "/insight-api"} {
		w := insightRequest(s, "GET", prefix+"/utils/estimatefee?nbBlocks=2,3", "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"2":0.00012345,"3":0.00002}` {
			t.Fatalf("%s estimatefee %d %s", prefix, w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(f.feeCons, []bool{true, true, true, true}) {
		t.Fatalf("conservative flags %v", f.feeCons)
	}

	f.feeCalls = nil
	for _, path := range []string{"/api/utils/estimatefee", "/api/utils/estimatefee?nbBlocks="} {
		w := insightRequest(s, "GET", path, "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"2":0.00012345}` {
			t.Fatalf("%s default %d %s", path, w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(f.feeCalls, []int{2, 2}) {
		t.Fatalf("default targets %v", f.feeCalls)
	}

	// keys keep the request order, as stock's zipObject does
	f.fees[10] = 1000
	w := insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=10,2", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"10":0.00001,"2":0.00012345}` {
		t.Fatalf("order %d %s", w.Code, w.Body.String())
	}

	f.feeCalls = nil
	eleven := strings.TrimSuffix(strings.Repeat("2,", 11), ",")
	for _, q := range []string{"0", "x", "2,,3", eleven, "1009", "-1", "2.5", "%202", "2%2B3", "99999999999999999999"} {
		w := insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks="+q, "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"status":"error"`) {
			t.Fatalf("nbBlocks=%q gave %d %s", q, w.Code, w.Body.String())
		}
	}
	if len(f.feeCalls) != 0 {
		t.Fatalf("invalid input reached the worker: %v", f.feeCalls)
	}
	w = insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=1,1008", "")
	if w.Code != 200 {
		t.Fatalf("bounds %d %s", w.Code, w.Body.String())
	}
}

func TestInsightEstimateFeeUnavailable(t *testing.T) {
	f := insightFixture()
	f.fees = map[int]int64{2: 12345, 3: 0, 5: -7}
	f.feeErr = map[int]error{4: errors.New("secret daemon text")}
	s := insightTestServer(t, f)
	w := insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=2,3,4,5", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"2":0.00012345,"3":-1,"4":-1,"5":-1}` {
		t.Fatalf("unavailable %d %s", w.Code, w.Body.String())
	}
}

func TestInsightCurrency(t *testing.T) {
	f := insightFixture()
	f.fiat = &api.FiatTicker{Rates: map[string]float32{"usd": 41.5}}
	s := insightTestServer(t, f)
	s.SetBackend(nil, "ZEC")
	for _, prefix := range []string{"/api", "/insight-api"} {
		w := insightRequest(s, "GET", prefix+"/currency", "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":200,"data":{"rate":41.5,"short":"ZEC"}}` {
			t.Fatalf("%s currency %d %s", prefix, w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(f.fiatArgs, []string{"usd", ""}) {
		t.Fatalf("fiat args %v", f.fiatArgs)
	}

	// float32 rates are printed at float32 precision, not as widened float64 noise
	f.fiat = &api.FiatTicker{Rates: map[string]float32{"usd": 0.1}}
	w := insightRequest(s, "GET", "/api/currency", "")
	if strings.TrimSpace(w.Body.String()) != `{"status":200,"data":{"rate":0.1,"short":"ZEC"}}` {
		t.Fatalf("precision %s", w.Body.String())
	}

	zero := `{"status":200,"data":{"rate":0,"short":"ZEC"}}`
	cases := []struct {
		name   string
		ticker *api.FiatTicker
		err    error
	}{
		{"error", nil, api.NewAPIError("No tickers found!", true)},
		{"nil ticker", nil, nil},
		{"missing usd", &api.FiatTicker{Rates: map[string]float32{"eur": 3}}, nil},
		{"negative", &api.FiatTicker{Rates: map[string]float32{"usd": -1}}, nil},
	}
	for _, c := range cases {
		f.fiat, f.fiatErr = c.ticker, c.err
		w := insightRequest(s, "GET", "/api/currency", "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != zero {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body.String())
		}
	}
}

func insightVerifyRPC(result interface{}, err error, seen *[][]interface{}) InsightRPC {
	return func(method string, params []interface{}, out interface{}) error {
		if method != "verifymessage" {
			return errors.New("unexpected method " + method)
		}
		*seen = append(*seen, params)
		if err != nil {
			return err
		}
		*(out.(*bool)) = result.(bool)
		return nil
	}
}

func TestInsightVerifyMessage(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	var seen [][]interface{}
	s.SetBackend(insightVerifyRPC(true, nil, &seen), "ZEC")

	w := insightRequest(s, "GET", "/api/messages/verify?address=t1A&signature=c2ln&message=hello%20world", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"result":true}` {
		t.Fatalf("GET verify %d %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(seen, [][]interface{}{{"t1A", "c2ln", "hello world"}}) {
		t.Fatalf("rpc params %v", seen)
	}

	seen = nil
	w = insightTypedRequest(s, "POST", "/insight-api/messages/verify", `{"address":"t1B","signature":"c2ln","message":"m"}`, "application/json")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"result":true}` {
		t.Fatalf("POST JSON verify %d %s", w.Code, w.Body.String())
	}
	w = insightTypedRequest(s, "POST", "/api/messages/verify", "address=t1C&signature=c2ln&message=m2", "application/x-www-form-urlencoded")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"result":true}` {
		t.Fatalf("POST form verify %d %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(seen, [][]interface{}{{"t1B", "c2ln", "m"}, {"t1C", "c2ln", "m2"}}) {
		t.Fatalf("POST rpc params %v", seen)
	}

	// as stock (req.body.x || req.query.x): a non-empty body field beats the
	// query, an empty or absent one falls back to it
	seen = nil
	w = insightTypedRequest(s, "POST", "/api/messages/verify?address=q1&signature=qs&message=qm", `{"address":"b1","signature":"","message":"bm"}`, "application/json")
	if w.Code != 200 || !reflect.DeepEqual(seen, [][]interface{}{{"b1", "qs", "bm"}}) {
		t.Fatalf("body/query precedence %d %v", w.Code, seen)
	}

	// as stock: an empty message is a message; a missing one is an error
	seen = nil
	w = insightRequest(s, "GET", "/api/messages/verify?address=t1A&signature=c2ln&message=", "")
	if w.Code != 200 || len(seen) != 1 {
		t.Fatalf("empty message %d %s", w.Code, w.Body.String())
	}
	seen = nil
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/messages/verify?address=t1A&signature=c2ln", ""},
		{"GET", "/api/messages/verify?address=t1A&message=m", ""},
		{"GET", "/api/messages/verify?signature=c2ln&message=m", ""},
		{"POST", "/api/messages/verify", `{"address":"t1A","signature":"c2ln"}`},
		{"POST", "/api/messages/verify", `{"address":"t1A","signature":"c2ln","message":5}`},
		{"POST", "/api/messages/verify", `[1]`},
	} {
		w := insightRequest(s, c.method, c.path, c.body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"status":"error"`) {
			t.Fatalf("%s %s %s gave %d %s", c.method, c.path, c.body, w.Code, w.Body.String())
		}
	}
	if len(seen) != 0 {
		t.Fatalf("invalid input reached the daemon: %v", seen)
	}

	for _, code := range []int{-3, -5, -8} {
		s.SetBackend(insightVerifyRPC(nil, &bchain.RPCError{Code: code, Message: "Invalid address"}, &seen), "ZEC")
		w = insightRequest(s, "GET", "/api/messages/verify?address=bad&signature=c2ln&message=m", "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"result":false}` {
			t.Fatalf("code %d: %d %s", code, w.Code, w.Body.String())
		}
	}
	s.SetBackend(insightVerifyRPC(false, nil, &seen), "ZEC")
	w = insightRequest(s, "GET", "/api/messages/verify?address=t1A&signature=c2ln&message=m", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"result":false}` {
		t.Fatalf("false %d %s", w.Code, w.Body.String())
	}

	for _, err := range []error{
		&bchain.RPCError{Code: -1, Message: "secret daemon text"},
		errors.New("secret daemon text http://user:pass@127.0.0.1:8232"),
	} {
		s.SetBackend(insightVerifyRPC(nil, err, &seen), "ZEC")
		w = insightRequest(s, "GET", "/api/messages/verify?address=t1A&signature=c2ln&message=m", "")
		if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "127.0.0.1") {
			t.Fatalf("%v: %d %s", err, w.Code, w.Body.String())
		}
	}
}

func TestInsightVerifyMessageUnsupported(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	s.SetBackend(nil, "ZEC")
	w := insightRequest(s, "GET", "/api/messages/verify?address=t1A&signature=c2ln&message=m", "")
	if w.Code != 501 || !strings.Contains(w.Body.String(), "Not supported") {
		t.Fatalf("nil rpc %d %s", w.Code, w.Body.String())
	}
	var seen [][]interface{}
	s.SetBackend(insightVerifyRPC(nil, &bchain.RPCError{Code: -32601, Message: "Method not found"}, &seen), "ZEC")
	w = insightRequest(s, "GET", "/api/messages/verify?address=t1A&signature=c2ln&message=m", "")
	if w.Code != 501 || !strings.Contains(w.Body.String(), "Not supported") || strings.Contains(w.Body.String(), "Method not found") {
		t.Fatalf("-32601 %d %s", w.Code, w.Body.String())
	}
}

func insightTypedRequest(s *InsightServer, method, path, body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestInsightEstimateFeeDedupeCacheAndGone(t *testing.T) {
	f := insightFixture()
	f.fees = map[int]int64{2: 12345, 3: 2000, 500: 700}
	s := insightTestServer(t, f)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	insightNow = func() time.Time { return now }
	t.Cleanup(func() { insightNow = time.Now })

	// repeated targets collapse to one key at their first place, one call each
	w := insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=3,2,3,2", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"3":0.00002,"2":0.00012345}` || !reflect.DeepEqual(f.feeCalls, []int{3, 2}) {
		t.Fatalf("dedupe %d %s %v", w.Code, w.Body.String(), f.feeCalls)
	}

	// targets the worker does not cache are cached for 10 s here
	f.feeCalls = nil
	for i := 0; i < 2; i++ {
		w = insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=500,500", "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"500":0.000007}` {
			t.Fatalf("high target %d %s", w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(f.feeCalls, []int{500}) {
		t.Fatalf("high target not cached: %v", f.feeCalls)
	}
	now = now.Add(insightFeeCacheTTL)
	insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=500", "")
	if !reflect.DeepEqual(f.feeCalls, []int{500, 500}) {
		t.Fatalf("expired entry not refreshed: %v", f.feeCalls)
	}
	// an unavailable high target is not cached
	f.fees[600] = 0
	f.feeCalls = nil
	insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=600", "")
	insightRequest(s, "GET", "/api/utils/estimatefee?nbBlocks=600", "")
	if !reflect.DeepEqual(f.feeCalls, []int{600, 600}) {
		t.Fatalf("failure cached: %v", f.feeCalls)
	}

	// a gone client stops the loop before any backend call
	f.feeCalls = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/api/utils/estimatefee?nbBlocks=2,3", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if rec.Code == 200 || len(f.feeCalls) != 0 {
		t.Fatalf("gone client: %d %v", rec.Code, f.feeCalls)
	}
}
