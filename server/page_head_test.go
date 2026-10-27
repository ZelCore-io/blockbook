//go:build unittest

package server

import (
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/eth"
	"github.com/trezor/blockbook/explorercontent"
)

func headAmount(v int64) *api.Amount { return (*api.Amount)(big.NewInt(v)) }

type headCase struct {
	name      string
	tpl       tpl
	url       string
	data      func(td *TemplateData)
	title     string
	desc      []string // substrings of the description
	canonical string   // path after the public URL, "" = none
	robots    string
	crumbs    []string // BreadcrumbList item names, nil = no breadcrumbs
}

func headServer(chainType bchain.ChainType, publicURL string) *PublicServer {
	var parser bchain.BlockChainParser
	if chainType == bchain.ChainEthereumType {
		parser = eth.NewEthereumParser(1, true)
	} else {
		parser = btc.NewBitcoinParser(btc.GetChainParams("test"), &btc.Configuration{})
	}
	s := &PublicServer{chainParser: parser}
	s.SetPublicURL(publicURL)
	return s
}

func headTemplateData(chainType bchain.ChainType, label, shortcut string) *TemplateData {
	return &TemplateData{
		CoinLabel:        label,
		CoinShortcut:     shortcut,
		ChainType:        chainType,
		InternalExplorer: true,
		ExplorerContent: explorercontent.Snapshot{
			Theme:    &explorercontent.Theme{Color: "#1B6E3A", IconURL: "https://cdn.example.com/coin.svg"},
			Products: []explorercontent.Product{{ID: "zelcore", Name: "Zelcore", TargetURL: "https://zelcore.io/?utm_source=explorer", IconURL: "https://cdn.example.com/zelcore.png"}},
		},
	}
}

func runHeadCases(t *testing.T, chainType bchain.ChainType, label, shortcut string, cases []headCase) {
	for _, publicURL := range []string{"https://explorer.example.com/", ""} {
		for _, c := range cases {
			t.Run(c.name+" publicURL="+publicURL, func(t *testing.T) {
				s := headServer(chainType, publicURL)
				td := headTemplateData(chainType, label, shortcut)
				if c.data != nil {
					c.data(td)
				}
				s.setPageHead(c.tpl, td, httptest.NewRequest("GET", c.url, nil))
				if td.PageTitle != c.title {
					t.Errorf("title %q, want %q", td.PageTitle, c.title)
				}
				for _, d := range c.desc {
					if !strings.Contains(td.PageDescription, d) {
						t.Errorf("description %q misses %q", td.PageDescription, d)
					}
				}
				if n := utf8.RuneCountInString(td.PageDescription); n == 0 || n > maxDescriptionLen {
					t.Errorf("description has %d characters: %q", n, td.PageDescription)
				}
				wantCanonical := ""
				if publicURL != "" && c.canonical != "" {
					wantCanonical = "https://explorer.example.com" + c.canonical
				}
				if td.Canonical != wantCanonical {
					t.Errorf("canonical %q, want %q", td.Canonical, wantCanonical)
				}
				if td.Robots != c.robots {
					t.Errorf("robots %q, want %q", td.Robots, c.robots)
				}
				if td.OGImage != "https://cdn.example.com/coin.svg" {
					t.Errorf("og image %q", td.OGImage)
				}
				checkHeadJSONLD(t, c, td, publicURL != "")
			})
		}
	}
}

func checkHeadJSONLD(t *testing.T, c headCase, td *TemplateData, hasPublicURL bool) {
	t.Helper()
	wantLD := hasPublicURL && (c.tpl == indexTpl || c.crumbs != nil)
	if !wantLD {
		if td.JSONLD != "" {
			t.Errorf("unexpected JSON-LD %s", td.JSONLD)
		}
		return
	}
	var ld map[string]any
	if err := json.Unmarshal([]byte(td.JSONLD), &ld); err != nil {
		t.Fatalf("JSON-LD does not parse: %v: %s", err, td.JSONLD)
	}
	if ld["@context"] != "https://schema.org" {
		t.Errorf("JSON-LD context %v", ld["@context"])
	}
	if c.tpl == indexTpl {
		if ld["@type"] != "WebSite" || ld["url"] != "https://explorer.example.com/" {
			t.Errorf("home JSON-LD %s", td.JSONLD)
		}
		action, _ := ld["potentialAction"].(map[string]any)
		target, _ := action["target"].(map[string]any)
		if action["@type"] != "SearchAction" || target["urlTemplate"] != "https://explorer.example.com/search?q={search_term_string}" || action["query-input"] != "required name=search_term_string" {
			t.Errorf("home SearchAction %s", td.JSONLD)
		}
		publisher, _ := ld["publisher"].(map[string]any)
		if publisher["@type"] != "Organization" || publisher["name"] != "Zelcore" || publisher["url"] != "https://zelcore.io" || publisher["logo"] != "https://cdn.example.com/zelcore.png" {
			t.Errorf("home publisher %s", td.JSONLD)
		}
		return
	}
	if ld["@type"] != "BreadcrumbList" {
		t.Errorf("JSON-LD type %v", ld["@type"])
	}
	items, _ := ld["itemListElement"].([]any)
	if len(items) != len(c.crumbs) {
		t.Fatalf("breadcrumbs %s, want %v", td.JSONLD, c.crumbs)
	}
	for i, it := range items {
		m, _ := it.(map[string]any)
		item, _ := m["item"].(string)
		if m["@type"] != "ListItem" || m["position"] != float64(i+1) || m["name"] != c.crumbs[i] || !strings.HasPrefix(item, "https://explorer.example.com/") {
			t.Errorf("breadcrumb %d = %v, want %q", i, m, c.crumbs[i])
		}
	}
}

const headTxid = "fdd824a780cbb718eeb766eb05d83fdefc793a27082cd5e67f856d69798cf7db"

func Test_setPageHead_BitcoinType(t *testing.T) {
	block := func(td *TemplateData) {
		td.Block = &api.Block{BlockInfo: api.BlockInfo{Hash: "00000000eb0443fd7dc4a1ed5c686a8e995057805f9a161d9a5a77a95e72b7b6", Height: 225494, Time: 1521595678, Size: 2345}, TxCount: 2}
	}
	cases := []headCase{
		{name: "home", tpl: indexTpl, url: "/", title: "Fake Coin (FAKE) Explorer · blocks, transactions and addresses",
			desc: []string{"Fake Coin (FAKE) blockchain", "addresses"}, canonical: "/"},
		{name: "home txs tab", tpl: indexTpl, url: "/?tab=txs", title: "Fake Coin (FAKE) Explorer · blocks, transactions and addresses", canonical: "/"},
		{name: "blocks", tpl: blocksTpl, url: "/blocks", data: func(td *TemplateData) { td.Page = 1 }, title: "Latest Fake Coin blocks", canonical: "/blocks"},
		{name: "blocks page 3", tpl: blocksTpl, url: "/blocks?page=3", data: func(td *TemplateData) { td.Page = 3 }, title: "Fake Coin blocks · page 3", canonical: "/blocks?page=3"},
		{name: "block by hash", tpl: blockTpl, url: "/block/00000000eb0443fd7dc4a1ed5c686a8e995057805f9a161d9a5a77a95e72b7b6", data: block,
			title:     "Fake Coin block 225,494 · 2 transactions · 2018-03-21",
			desc:      []string{"Fake Coin block 225,494 mined 2018-03-21 01:27 UTC with 2 transactions, 2,345 bytes", "Hash 00000000eb04"},
			canonical: "/block/225494", crumbs: []string{"Home", "Blocks", "Block 225,494"}},
		{name: "block page 2", tpl: blockTpl, url: "/block/225494?page=2", data: func(td *TemplateData) { block(td); td.Page = 2 },
			title: "Fake Coin block 225,494 · 2 transactions · 2018-03-21", canonical: "/block/225494?page=2", crumbs: []string{"Home", "Blocks", "Block 225,494"}},
		{name: "confirmed tx", tpl: txTpl, url: "/tx/" + headTxid, data: func(td *TemplateData) {
			td.Tx = &api.Tx{Txid: headTxid, Blockheight: 225494, Confirmations: 2, Blocktime: 1521595678, Vin: make([]api.Vin, 2), Vout: make([]api.Vout, 1), ValueOutSat: headAmount(12345678), FeesSat: headAmount(1000)}
		}, title: "Fake Coin transaction fdd824a780…8cf7db · 2 confirmations",
			desc:      []string{"confirmed in block 225,494 on 2018-03-21", "2 inputs", "1 output", "value 0.12345678 FAKE", "fee 0.00001 FAKE"},
			canonical: "/tx/" + headTxid, crumbs: []string{"Home", "Block 225,494", "Transaction fdd824a780…8cf7db"}},
		{name: "mempool tx", tpl: txTpl, url: "/tx/" + headTxid, data: func(td *TemplateData) {
			td.Tx = &api.Tx{Txid: headTxid, Vin: make([]api.Vin, 1), Vout: make([]api.Vout, 2), ValueOutSat: headAmount(1)}
		}, title: "Fake Coin transaction fdd824a780…8cf7db · Unconfirmed", desc: []string{"unconfirmed", "2 outputs"},
			canonical: "/tx/" + headTxid, crumbs: []string{"Home", "Mempool", "Transaction fdd824a780…8cf7db"}},
		{name: "address", tpl: addressTpl, url: "/address/mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz?filter=inputs", data: func(td *TemplateData) {
			td.Address = &api.Address{AddrStr: "mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz", BalanceSat: headAmount(0), TotalReceivedSat: headAmount(1234567800), TotalSentSat: headAmount(1234567800), Txs: 2}
			td.Page = 1
		}, title: "Fake Coin address mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz · balance 0 FAKE",
			desc:      []string{"Fake Coin address mtGXQvBowM…s9eEtz", "balance 0 FAKE", "2 transactions", "received 12.345678 FAKE", "sent 12.345678 FAKE"},
			canonical: "/address/mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz", crumbs: []string{"Home", "Address mtGXQvBowM…s9eEtz"}},
		{name: "address page 2", tpl: addressTpl, url: "/address/mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz?page=2", data: func(td *TemplateData) {
			td.Address = &api.Address{AddrStr: "mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz", BalanceSat: headAmount(5)}
			td.Page = 2
		}, title: "Fake Coin address mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz · balance 0.00000005 FAKE",
			canonical: "/address/mtGXQvBowMkBpnhLckhxhbwYK44Gs9eEtz?page=2", crumbs: []string{"Home", "Address mtGXQvBowM…s9eEtz"}},
		{name: "xpub", tpl: xpubTpl, url: "/xpub/upub5E1xjDmZ7Hhej6LPpS8duATdKXnRYui7bDYj6ehfFGzWDZtmCmQkZhc3Zb7kgRLtHWd16QFxyP86JKL3ShZEBFX88aciJ3xyocuyhZZ8g6q", data: func(td *TemplateData) {
			td.Address = &api.Address{AddrStr: "upub5E1xjDmZ7Hhej6LPpS8duATdKXnRYui7bDYj6ehfFGzWDZtmCmQkZhc3Zb7kgRLtHWd16QFxyP86JKL3ShZEBFX88aciJ3xyocuyhZZ8g6q"}
		}, title: "Fake Coin xpub upub5E1xjD…ZZ8g6q", robots: "noindex,follow"},
		{name: "mempool", tpl: mempoolTpl, url: "/mempool", data: func(td *TemplateData) { td.MempoolTxids = &api.MempoolTxids{MempoolSize: 7} },
			title: "Fake Coin mempool · 7 unconfirmed transactions", canonical: "/mempool"},
		{name: "mempool page 2", tpl: mempoolTpl, url: "/mempool?page=2", data: func(td *TemplateData) { td.MempoolTxids = &api.MempoolTxids{MempoolSize: 1}; td.Page = 2 },
			title: "Fake Coin mempool · 1 unconfirmed transaction · page 2", canonical: "/mempool?page=2"},
		{name: "sendtx", tpl: sendTransactionTpl, url: "/sendtx", title: "Send a raw Fake Coin transaction", canonical: "/sendtx", robots: "noindex,follow"},
		{name: "status", tpl: statusTpl, url: "/status", data: func(td *TemplateData) {
			td.Info = &api.SystemInfo{Blockbook: &api.BlockbookInfo{BestHeight: 225494, InSync: true, MempoolSize: 3}}
		}, title: "Fake Coin Explorer status", desc: []string{"best block 225,494, in sync, 3 mempool transactions"}, canonical: "/status"},
		{name: "error", tpl: errorTpl, url: "/tx/00", data: func(td *TemplateData) { td.Error = &api.APIError{Text: "Transaction not found", Public: true} },
			title: "Transaction not found · Fake Coin Explorer", desc: []string{"Transaction not found"}, robots: "noindex"},
		{name: "search without results", tpl: errorTpl, url: "/search?q=%3C%2Fscript%3E", data: func(td *TemplateData) {
			td.Error = &api.APIError{Text: "No matching records found for '</script>'", Public: true}
		},
			title: "No matching records found for '</script>' · Fake Coin Explorer", robots: "noindex,follow"},
		{name: "internal error", tpl: errorInternalTpl, url: "/block/1", data: func(td *TemplateData) { td.Error = &api.APIError{Text: "Internal server error"} },
			title: "Internal server error · Fake Coin Explorer", robots: "noindex"},
		{name: "secondary currency", tpl: blockTpl, url: "/block/225494?secondary=eur&use_secondary=true", data: block,
			title: "Fake Coin block 225,494 · 2 transactions · 2018-03-21", canonical: "/block/225494", robots: "noindex,follow", crumbs: []string{"Home", "Blocks", "Block 225,494"}},
		{name: "use_secondary only", tpl: indexTpl, url: "/?use_secondary=false", title: "Fake Coin (FAKE) Explorer · blocks, transactions and addresses", canonical: "/", robots: "noindex,follow"},
	}
	runHeadCases(t, bchain.ChainBitcoinType, "Fake Coin", "FAKE", cases)
}

func Test_setPageHead_EthereumType(t *testing.T) {
	const addr = "0x7B62EB7fe80350DC7EC945C0B73242cb9877FB1b"
	const contract = "0x4af4114F73d1c1C903aC9E0361b379D1291808A2"
	cases := []headCase{
		{name: "home", tpl: indexTpl, url: "/", title: "Ethereum (ETH) Explorer · blocks, transactions and addresses",
			desc: []string{"addresses and tokens"}, canonical: "/"},
		{name: "block", tpl: blockTpl, url: "/block/4321000", data: func(td *TemplateData) {
			td.Block = &api.Block{BlockInfo: api.BlockInfo{Hash: "0xeaa3f7b0f7d7f5d6a5f4b6f6a5b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f", Height: 4321000, Time: 1534858022}, TxCount: 1}
		}, title: "Ethereum block 4,321,000 · 1 transaction · 2018-08-21", desc: []string{"with 1 transaction. Hash 0xeaa3f7b0"},
			canonical: "/block/4321000", crumbs: []string{"Home", "Blocks", "Block 4,321,000"}},
		{name: "failed token tx", tpl: txTpl, url: "/tx/0xa9cd088aba2131000da6f38a33c20169baee476218deea6b78720700b895b101", data: func(td *TemplateData) {
			td.Tx = &api.Tx{Txid: "0xa9cd088aba2131000da6f38a33c20169baee476218deea6b78720700b895b101", Blockheight: 4321000, Confirmations: 12, Blocktime: 1534858022,
				Vin: make([]api.Vin, 1), Vout: make([]api.Vout, 1), ValueOutSat: headAmount(0), FeesSat: headAmount(21000000000000),
				TokenTransfers: make([]api.TokenTransfer, 2), EthereumSpecific: &api.EthereumSpecific{Status: bchain.TxStatusFailure}}
		}, title: "Ethereum transaction 0xa9cd088a…95b101 · Failed · 12 confirmations",
			desc:      []string{"failed, confirmed in block 4,321,000", "value 0 ETH", "fee 0.000021 ETH", "2 token transfers"},
			canonical: "/tx/0xa9cd088aba2131000da6f38a33c20169baee476218deea6b78720700b895b101", crumbs: []string{"Home", "Block 4,321,000", "Transaction 0xa9cd088a…95b101"}},
		{name: "account", tpl: addressTpl, url: "/address/" + addr, data: func(td *TemplateData) {
			td.Address = &api.Address{AddrStr: addr, BalanceSat: headAmount(123450000000000000), Txs: 5, Nonce: "3", Tokens: make(api.Tokens, 2)}
		}, title: "Ethereum address " + addr + " · balance 0.12345 ETH",
			desc:      []string{"balance 0.12345 ETH, 5 transactions, nonce 3, 2 tokens"},
			canonical: "/address/" + addr, crumbs: []string{"Home", "Address 0x7B62EB7f…77FB1b"}},
		{name: "token contract", tpl: addressTpl, url: "/address/" + contract, data: func(td *TemplateData) {
			td.Address = &api.Address{AddrStr: contract, BalanceSat: headAmount(0), Txs: 1, ContractInfo: &api.ContractInfoResult{Name: "Contract 74", Symbol: "S74", Standard: bchain.ERC20TokenStandard}}
		}, title: "Ethereum contract Contract 74 (S74) · " + contract,
			desc:      []string{"Contract 74 (S74) ERC20 contract on Ethereum.", "balance 0 ETH"},
			canonical: "/address/" + contract, crumbs: []string{"Home", "Address 0x4af4114F…1808A2"}},
		{name: "nft", tpl: nftDetailTpl, url: "/nft/" + contract + "/1", data: func(td *TemplateData) {
			td.ContractInfo = &bchain.ContractInfo{Contract: contract, Name: "Cats", Symbol: "CAT", Standard: bchain.ERC771TokenStandard}
			td.TokenId = "1"
		}, title: "Cats #1 · Ethereum NFT", desc: []string{"Token 1 of the Cats (CAT) ERC721 contract 0x4af4114F…1808A2 on Ethereum."},
			canonical: "/nft/" + contract + "/1"},
		{name: "sendtx", tpl: sendTransactionTpl, url: "/sendtx", title: "Send a raw Ethereum transaction", canonical: "/sendtx", robots: "noindex,follow"},
	}
	runHeadCases(t, bchain.ChainEthereumType, "Ethereum", "ETH", cases)
}

func Test_setPageHead_NoPublisherWithoutZelcoreProduct(t *testing.T) {
	s := headServer(bchain.ChainBitcoinType, "https://explorer.example.com")
	td := headTemplateData(bchain.ChainBitcoinType, "Fake Coin", "FAKE")
	td.ExplorerContent = explorercontent.Snapshot{}
	s.setPageHead(indexTpl, td, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(string(td.JSONLD), "publisher") || !strings.Contains(string(td.JSONLD), `"WebSite"`) {
		t.Errorf("JSON-LD %s", td.JSONLD)
	}
	if td.OGImage != "" {
		t.Errorf("og image %q without a feed", td.OGImage)
	}
	// the Zelcore product icon is the fallback image when the theme has none
	td.ExplorerContent = explorercontent.Snapshot{Products: []explorercontent.Product{{ID: "zelcore", IconURL: "https://cdn.example.com/z.png"}}}
	s.setPageHead(indexTpl, td, httptest.NewRequest("GET", "/", nil))
	if td.OGImage != "https://cdn.example.com/z.png" {
		t.Errorf("og image %q, want the product icon", td.OGImage)
	}
	if strings.Contains(string(td.JSONLD), "publisher") {
		t.Errorf("publisher without a product name or URL: %s", td.JSONLD)
	}
	// an external explorer has no search page, so no SearchAction
	td.InternalExplorer = false
	s.setPageHead(indexTpl, td, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(string(td.JSONLD), "SearchAction") {
		t.Errorf("SearchAction without the internal explorer: %s", td.JSONLD)
	}
}

func Test_pageJSONLD_EscapesScript(t *testing.T) {
	s := headServer(bchain.ChainBitcoinType, "https://explorer.example.com")
	td := headTemplateData(bchain.ChainBitcoinType, "Coin</script><script>alert(1)</script>", "C")
	td.Block = &api.Block{BlockInfo: api.BlockInfo{Height: 1}}
	s.setPageHead(blockTpl, td, httptest.NewRequest("GET", "/block/1", nil))
	if strings.Contains(strings.ToLower(string(td.JSONLD)), "</script") || strings.Contains(string(td.JSONLD), "<") {
		t.Errorf("JSON-LD can close the script element: %s", td.JSONLD)
	}
	var v any
	if err := json.Unmarshal([]byte(td.JSONLD), &v); err != nil {
		t.Errorf("JSON-LD does not parse: %v", err)
	}
}

func Test_headHelpers(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 6317172: "6,317,172", -1234: "-1,234"} {
		if got := groupDigits(n); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", n, got, want)
		}
	}
	if got := truncateRunes(strings.Repeat("é", 200), 160); utf8.RuneCountInString(got) != 160 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncateRunes kept %d runes", utf8.RuneCountInString(got))
	}
	if got := shortID("short"); got != "short" {
		t.Errorf("shortID(short) = %q", got)
	}
	for raw, want := range map[string]string{"https://zelcore.io/x?y": "https://zelcore.io", "http://zelcore.io": "", "javascript:alert(1)": "", "": ""} {
		if got := httpsOrigin(raw); got != want {
			t.Errorf("httpsOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Test_PublicServer_PageHead renders real pages of the Bitcoin-type fixture with and without a
// public URL.
func Test_PublicServer_PageHead(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	head := func(u string) string { return strings.Split(getHTML(t, ts.URL+u), "</head>")[0] }
	// a search without results is a 404 error page (see public_seo.go)
	head404 := func(u string) string {
		t.Helper()
		resp, err := http.DefaultClient.Do(newGetRequest(ts.URL + u))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404", u, resp.StatusCode)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(string(b), "</head>")[0]
	}

	block := head("/block/225494")
	checkHTML(t, "block without public URL", block, []string{
		`<title>Fake Coin block 225,494 · `,
		`<meta name="description" content="Fake Coin block 225,494 `,
		`<meta property="og:site_name" content="Fake Coin Explorer">`,
		`<meta name="twitter:card" content="summary">`,
	}, []string{`rel="canonical"`, `og:url`, `ld+json`, `name="robots"`})

	s.SetPublicURL("https://explorer.example.com/")
	block = head("/block/225494")
	checkHTML(t, "block", block, []string{
		`<link rel="canonical" href="https://explorer.example.com/block/225494">`,
		`<meta property="og:url" content="https://explorer.example.com/block/225494">`,
		`<script type="application/ld+json">{"@context":"https://schema.org","@type":"BreadcrumbList",`,
	}, []string{`name="robots"`})
	ld := strings.Split(strings.Split(block, `<script type="application/ld+json">`)[1], "</script>")[0]
	var v map[string]any
	if err := json.Unmarshal([]byte(ld), &v); err != nil {
		t.Errorf("block JSON-LD does not parse: %v: %s", err, ld)
	}

	home := head("/?secondary=eur&use_secondary=true")
	checkHTML(t, "home", home, []string{
		`<title>Fake Coin (FAKE) Explorer · blocks, transactions and addresses</title>`,
		`<meta name="robots" content="noindex,follow">`,
		`<link rel="canonical" href="https://explorer.example.com/">`,
		`"@type":"WebSite"`, `"urlTemplate":"https://explorer.example.com/search?q={search_term_string}"`,
	}, nil)

	checkHTML(t, "sendtx", head("/sendtx"), []string{`<meta name="robots" content="noindex,follow">`, `<title>Send a raw Fake Coin transaction</title>`}, nil)
	checkHTML(t, "search", head404("/search?q=%3C%2Fscript%3Exx"), []string{`<meta name="robots" content="noindex`, `<title>No matching records found for &#39;&lt;/script&gt;xx&#39; · Fake Coin Explorer</title>`}, []string{`rel="canonical"`, `</script>xx`})
	checkHTML(t, "status", head("/status"), []string{`<link rel="canonical" href="https://explorer.example.com/status">`, `<title>Fake Coin Explorer status</title>`}, nil)
	checkHTML(t, "blocks", head("/blocks"), []string{`<link rel="canonical" href="https://explorer.example.com/blocks">`}, nil)
	checkHTML(t, "mempool", head("/mempool"), []string{`<link rel="canonical" href="https://explorer.example.com/mempool">`}, nil)
}
