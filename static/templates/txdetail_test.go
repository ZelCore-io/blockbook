package templates

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Standalone render tests of the transaction detail partials (no server, no database).

type fakeVin struct {
	IsOwn, IsAddress bool
	Addresses        []string
	Hex, Txid        string
	Vout             int
	ValueSat         string
}

type fakeVout struct {
	IsOwn, IsAddress bool
	Addresses        []string
	Spent            bool
	SpentTxID        string
	N                int
	ValueSat         string
}

type fakeParsed struct{ Name, MethodId string }

type fakeTransfer struct {
	Type             int
	Standard         string
	From, To         string
	Value, Symbol    string
	Contract         string
	MultiTokenValues []struct{ Id, Value string }
}

type fakeEth struct {
	Status            int
	Error             string
	ParsedData        *fakeParsed
	Type              int
	CreatedContract   string
	InternalTransfers []fakeTransfer
	EffectiveGasPrice string
	GasPrice          string
}

type fakeTx struct {
	Txid                   string
	Rbf                    bool
	Blocktime              int
	Confirmations          int
	ConfirmationETABlocks  int
	ConfirmationETASeconds int
	Vin                    []fakeVin
	Vout                   []fakeVout
	FeesSat, ValueOutSat   string
	EthereumSpecific       fakeEth
	TokenTransfers         []fakeTransfer
}

type fakeTxData struct {
	Tx                    *fakeTx
	AddrStr               string
	CoinShortcut          string
	TxIOCollapseThreshold int
	FungibleTokenName     string
	NonFungibleTokenName  string
	MultiTokenName        string
}

func fakeFuncs() template.FuncMap {
	span := func(v any, _ any, class string) template.HTML {
		return template.HTML(fmt.Sprintf(`<span class="amt %s" cc="%v"><span class="prim-amt">%v</span></span>`, class, v, v))
	}
	return template.FuncMap{
		"amountSpan":          span,
		"amountSatsSpan":      span,
		"tokenAmountSpan":     func(tt fakeTransfer, d any, class string) template.HTML { return span(tt.Value, d, class) },
		"formattedAmountSpan": func(v any, _ int, sym string, d any, class string) template.HTML { return span(v, d, class) },
		"addressAliasSpan":    func(a string, _ any) template.HTML { return template.HTML(template.HTMLEscapeString(a)) },
		"isOwnAddress":        func(_ any, a string) bool { return a == "own" },
		"tokenTransfersCount": func(tx *fakeTx, std string) int {
			n := 0
			for _, tt := range tx.TokenTransfers {
				if tt.Standard == std {
					n++
				}
			}
			return n
		},
		"unixTimeSpan": func(int) template.HTML { return "1 min ago" },
		"relativeTime": func(int) string { return "1 min" },
		"feePerByte":   func(*fakeTx) string { return "" },
		"formatUint32": func(v int) string { return fmt.Sprint(v) },
		"chainExtra":   func(*fakeTx) any { return nil },
	}
}

func renderTxDetail(t *testing.T, file string, data *fakeTxData) string {
	t.Helper()
	tmpl, err := template.New("x").Funcs(fakeFuncs()).Parse(readTemplateFile(t, file) + readTemplateFile(t, "base.html"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "txdetail", data); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func btcFixture() *fakeTxData {
	long := "grs1qpagcczgezpl70y3wc6f2udun0vgy8dypreywl"
	tx := &fakeTx{Txid: "293d2a132d53eb7469ff18b9c1e9bf2844d5ce530397a803d6cda47f87beead4", Confirmations: 3, Blocktime: 1, FeesSat: "0.0001", ValueOutSat: "178.6",
		Vin: []fakeVin{
			{IsAddress: true, Addresses: []string{"FntPbL3RKsGkvPxsbcaLh47ds8xiN7AumW"}, Txid: "f53d0116acea6ac4b4f3364dfbc279f9d50bb75632e86a888b7bedb11dfa3978", ValueSat: "112.16"},
			{IsAddress: true, IsOwn: true, Addresses: []string{"FaZMhfW9n4qVWUkPKC3hQg4cJAb9qmDTFC", "FpVYnMppvnnevPwxwiTYxfceWYnUtia8pz"}, Txid: "8df00b461ac50c6113de82c8c9a1a3dd53710f5fd3db66418bc5247ced2eb936", ValueSat: "66.46"},
		},
		Vout: []fakeVout{
			{IsAddress: true, Addresses: []string{long + "f"}, ValueSat: "11.18"},
			{IsAddress: true, Addresses: []string{"FpVYnMppvnnevPwxwiTYxfceWYnUtia8pz"}, Spent: true, SpentTxID: "ab", ValueSat: "113.42", N: 1},
			{Addresses: nil, ValueSat: "0"},
		}}
	return &fakeTxData{Tx: tx, AddrStr: "FpVYnMppvnnevPwxwiTYxfceWYnUtia8pz", CoinShortcut: "GRS", TxIOCollapseThreshold: 8}
}

func ethFixture() *fakeTxData {
	tx := &fakeTx{Txid: "0x6d1d9b6a0c8b3b8b4d6c8a1f2e3d4c5b6a79881706f5e4d3c2b1a09182736455", Confirmations: 9, Blocktime: 1, FeesSat: "0.001", ValueOutSat: "1.5",
		Vin:  []fakeVin{{IsAddress: true, Addresses: []string{"0x3f5CE5FBFe3E9af3971dD833D26bA9b5C936f0bE"}}},
		Vout: []fakeVout{{IsAddress: true, Addresses: []string{"0x28C6c06298d514Db089934071355E5743bf21d60"}}},
		EthereumSpecific: fakeEth{Status: 1, CreatedContract: "0x1111111254EEB25477B68fb85Ed929f73A960582", Type: 1,
			InternalTransfers: []fakeTransfer{{From: "0xdAC17F958D2ee523a2206206994597C13D831ec7", To: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Value: "1"}}},
		TokenTransfers: []fakeTransfer{
			{Standard: "ERC20", From: "0xdAC17F958D2ee523a2206206994597C13D831ec7", To: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Value: "25 USDT", Symbol: "USDT"},
			{Standard: "ERC721", From: "0xdAC17F958D2ee523a2206206994597C13D831ec7", To: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Value: "7", Contract: "0xC", Symbol: "NFT"},
		}}
	return &fakeTxData{Tx: tx, AddrStr: "0x3f5CE5FBFe3E9af3971dD833D26bA9b5C936f0bE", CoinShortcut: "ETH", FungibleTokenName: "ERC20", NonFungibleTokenName: "ERC721", MultiTokenName: "ERC1155"}
}

func countCopyButtons(html string) (n int, values []string) {
	for _, part := range strings.Split(html, `<button type="button" class="hub-copy hub-io-copy" data-copy="`)[1:] {
		values = append(values, part[:strings.Index(part, `"`)])
	}
	return len(values), values
}

func TestTxDetailCopyButtonsBitcoinType(t *testing.T) {
	data := btcFixture()
	html := renderTxDetail(t, "txdetail.html", data)
	dumpTxDetail(t, "btc", html)
	n, values := countCopyButtons(html)
	// txid + 3 input addresses + 2 output addresses (an output without address has no text to copy)
	if n != 6 {
		t.Fatalf("copy buttons = %d (%v), want 6", n, values)
	}
	if values[0] != data.Tx.Txid {
		t.Errorf("first button copies %q, want the txid", values[0])
	}
	if !strings.Contains(html, `data-copy="FaZMhfW9n4qVWUkPKC3hQg4cJAb9qmDTFC"`) || !strings.Contains(html, `data-copy="FpVYnMppvnnevPwxwiTYxfceWYnUtia8pz"`) {
		t.Error("an address of a multi-address input is missing its copy button")
	}
	for _, want := range []string{`aria-label="Copy address"`, `aria-label="Copy transaction ID"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestTxDetailCopyButtonsEthereumType(t *testing.T) {
	data := ethFixture()
	html := renderTxDetail(t, "txdetail_ethereumtype.html", data)
	dumpTxDetail(t, "eth", html)
	n, values := countCopyButtons(html)
	// txid, from, to, created contract, internal transfer from/to, ERC20 from/to, ERC721 from/to
	if n != 10 {
		t.Fatalf("copy buttons = %d (%v), want 10", n, values)
	}
	if strings.Contains(html, `class="col-12 ellipsis`) {
		t.Error("an address row still uses the ellipsis block directly, the copy button would be clipped")
	}
}

func TestTxDetailCopyButtonsTron(t *testing.T) {
	data := ethFixture()
	html := renderTxDetail(t, "txdetail_tron.html", data)
	if n, _ := countCopyButtons(html); n < 10 {
		t.Fatalf("copy buttons = %d, want at least 10", n)
	}
	if strings.Contains(html, `class="col-12 ellipsis`) {
		t.Error("an address row still uses the ellipsis block directly")
	}
}

// dumpTxDetail writes the rendered partial for visual checks: TXDETAIL_DUMP=/some/dir go test ./static/templates
func dumpTxDetail(t *testing.T, name, html string) {
	dir := os.Getenv("TXDETAIL_DUMP")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name+".frag.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}
