//go:build unittest

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trezor/blockbook/explorercontent"
	"github.com/trezor/blockbook/tests/dbtestdata"
)

const licenseFooter = "Based on Blockbook by SatoshiLabs · AGPL-3.0 · "

func Test_PublicServer_HubInnerPages(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	txid := dbtestdata.TxidB2T1
	tx := getHTML(t, ts.URL+"/tx/"+txid)
	checkHTML(t, "tx", tx, []string{
		`<h1>Transaction</h1>`,
		`<span class="hub-badge hub-badge-ok">Confirmed · 1 confirmation</span>`,
		`<span class="hub-mono hub-summary-id">` + txid + `</span><button type="button" class="hub-copy" data-copy="` + txid + `" aria-label="Copy transaction ID">`,
		`<dt>Amount</dt>`, `<dt>Fee</dt>`, `<dt>Block</dt>`, `<dt>Size</dt>`,
		`<h2>Inputs and outputs</h2>`,
		`<details class="hub-card hub-raw"><summary>Raw transaction (JSON / hex)</summary>`,
		`id="raw-button"`, `id="raw-hex-button"`, `<pre id="raw"`,
		licenseFooter,
	}, []string{"hub-aside", "<details open", `>Coinbase<`})

	coinbase := getHTML(t, ts.URL+"/tx/"+dbtestdata.TxidB2T4)
	checkHTML(t, "coinbase tx", coinbase, []string{
		`<span class="hub-badge">Coinbase</span>`,
		`No Inputs (Newly Generated Coins)`,
	}, nil)

	address := getHTML(t, ts.URL+"/address/"+dbtestdata.Addr5)
	checkHTML(t, "address", address, []string{
		`<h1 class="hub-summary-title">Address</h1>`,
		`<div class="hub-mono hub-summary-id">` + dbtestdata.Addr5 + `</div>`,
		`<div id="qrcode" role="img" aria-label="QR code for this address"></div>`,
		`<button type="button" class="hub-copy hub-copy-text" data-copy="` + dbtestdata.Addr5 + `">Copy address</button>`,
		`<dt>Total received</dt>`, `<dt>Total sent</dt>`, `<dt>Transactions</dt>`,
		`<label class="hub-muted" for="hub-tx-filter">Show</label><select id="hub-tx-filter"`,
		licenseFooter,
	}, []string{"hub-aside"})

	// Every table inside a card sits in a .hub-scroll box.
	for _, p := range []string{"/blocks", "/block/225494", "/mempool", "/xpub/" + dbtestdata.Xpub} {
		html := getHTML(t, ts.URL+p)
		checkHTML(t, p, html, []string{`class="hub-card`, licenseFooter}, []string{"</row>"})
		if n, m := bytes.Count([]byte(html), []byte("<table")), bytes.Count([]byte(html), []byte(`<div class="hub-scroll"><table`)); n != m {
			t.Errorf("%s: %d tables, %d inside .hub-scroll", p, n, m)
		}
	}
	checkHTML(t, "/sendtx", getHTML(t, ts.URL+"/sendtx"), []string{`<section class="hub-card">`, licenseFooter}, nil)
	// a search without results is a 404 page (see public_seo_test.go)
	if code, html := getStatus(t, ts.URL+"/search?q=1234"); code != http.StatusNotFound {
		t.Errorf("/search?q=1234: status %d, want 404", code)
	} else {
		checkHTML(t, "/search?q=1234", html, []string{`<section class="hub-card hub-error">`, licenseFooter}, nil)
	}

	// The aside renders on inner pages only when there is a campaign or a zelcore product.
	contents := []struct {
		name    string
		content explorercontent.Snapshot
		aside   bool
	}{
		{name: "empty", content: explorercontent.Snapshot{}},
		{name: "other product only", content: explorercontent.Snapshot{Products: []explorercontent.Product{{ID: "ssp", Name: "SSP Wallet", TargetURL: "https://sspwallet.io"}}}},
		{name: "campaign", content: explorercontent.Snapshot{Campaigns: []explorercontent.Campaign{{ID: "c1", Placement: "sidebar", Title: "Campaign title", TargetURL: "https://zelcore.io"}}}, aside: true},
		{name: "zelcore product", content: explorercontent.Snapshot{Products: []explorercontent.Product{{ID: "zelcore", Name: "Zelcore", TargetURL: "https://zelcore.io"}}}, aside: true},
	}
	pages := []struct {
		name    string
		url     string
		handler func(http.ResponseWriter, *http.Request) (tpl, *TemplateData, error)
		want    string
	}{
		{name: "tx", url: "/tx/" + txid, handler: s.explorerTx, want: `aria-label="Copy transaction ID"`},
		{name: "address", url: "/address/" + dbtestdata.Addr5, handler: s.explorerAddress, want: `Copy address`},
	}
	for _, p := range pages {
		for _, c := range contents {
			page, data, err := p.handler(httptest.NewRecorder(), httptest.NewRequest("GET", p.url, nil))
			if err != nil {
				t.Fatal(err)
			}
			data.ExplorerContent = c.content
			var out bytes.Buffer
			if err := s.templates[page].ExecuteTemplate(&out, "base.html", data); err != nil {
				t.Fatal(err)
			}
			html := out.String()
			if !bytes.Contains(out.Bytes(), []byte(p.want)) {
				t.Errorf("%s/%s: missing %q", p.name, c.name, p.want)
			}
			if got := bytes.Contains(out.Bytes(), []byte(`<aside class="hub-aside"`)); got != c.aside {
				t.Errorf("%s/%s: aside rendered = %v, want %v\n%s", p.name, c.name, got, c.aside, html)
			}
		}
	}

	// Confirmation states of the tx summary: plural count, and an unconfirmed RBF tx with an ETA.
	txStates := []struct {
		name    string
		mutate  func(*TemplateData)
		want    []string
		notWant []string
	}{
		{
			name:    "2 confirmations",
			mutate:  func(d *TemplateData) { d.Tx.Confirmations = 2 },
			want:    []string{`<span class="hub-badge hub-badge-ok">Confirmed · 2 confirmations</span>`},
			notWant: []string{">Unconfirmed<", ">RBF<"},
		},
		{
			name: "unconfirmed",
			mutate: func(d *TemplateData) {
				d.Tx.Confirmations = 0
				d.Tx.ConfirmationETABlocks = 2
				d.Tx.ConfirmationETASeconds = 1200
				d.Tx.Rbf = true
			},
			want: []string{
				`<span class="hub-badge hub-badge-warn">Unconfirmed</span>`,
				`<dd>Unconfirmed</dd>`,
				`in approx. `, `(2 blocks)`,
				`<span tt="Replace by fee">RBF</span>`,
				`<span tt="Replace-by-Fee transaction, could be overridden by a new transaction">ON</span>`,
			},
			notWant: []string{"Confirmed ·", `href="/block/`},
		},
	}
	for _, c := range txStates {
		page, data, err := s.explorerTx(httptest.NewRecorder(), httptest.NewRequest("GET", "/tx/"+txid, nil))
		if err != nil {
			t.Fatal(err)
		}
		c.mutate(data)
		var out bytes.Buffer
		if err := s.templates[page].ExecuteTemplate(&out, "base.html", data); err != nil {
			t.Fatal(err)
		}
		checkHTML(t, "tx "+c.name, out.String(), c.want, c.notWant)
	}
}
