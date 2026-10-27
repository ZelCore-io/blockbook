package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

// maxDescriptionLen is the longest meta description, in characters, that search engines show.
const maxDescriptionLen = 160

// robotsNoindexFollow keeps a page out of the index while its links are still followed.
const robotsNoindexFollow = "noindex,follow"

// errorPageRobots is the robots value of an error page: never indexed, and a search page (the
// no-results page of /search) keeps its links followable.
func errorPageRobots(r *http.Request) string {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/search") {
		return robotsNoindexFollow
	}
	return "noindex"
}

// breadcrumb is one BreadcrumbList item; path is relative to the public URL.
type breadcrumb struct {
	name string
	path string
}

// setPageHead fills the per-page <head> fields of td (title, description, canonical link, robots,
// Open Graph image and JSON-LD) from the page data the template receives. It runs for every
// rendered page, error pages included, just before the template is executed. Everything comes
// from the coin config and the page data, so it works for any coin and chain type. A field a
// page does not have is left out.
func (s *PublicServer) setPageHead(t tpl, td *TemplateData, r *http.Request) {
	defer func() {
		// a missing field must never cost the page: the head then falls back to the defaults
		if e := recover(); e != nil {
			glog.Error("setPageHead recovered from panic: ", e)
		}
	}()
	site := td.CoinLabel + " Explorer"
	var title, description, canonicalPath, robots string
	var crumbs []breadcrumb
	home := breadcrumb{"Home", "/"}
	switch t {
	case indexTpl:
		title = coinWithTicker(td) + " Explorer · blocks, transactions and addresses"
		what := "blocks, transactions and addresses"
		if td.ChainType == bchain.ChainEthereumType {
			what = "blocks, transactions, addresses and tokens"
		}
		description = "Search the " + coinWithTicker(td) + " blockchain: " + what + ", the mempool and the network status, with a public API."
		canonicalPath = "/"
	case blocksTpl:
		title = "Latest " + td.CoinLabel + " blocks"
		if td.Page > 1 {
			title = td.CoinLabel + " blocks · page " + strconv.Itoa(td.Page)
		}
		description = "Latest blocks of the " + td.CoinLabel + " blockchain with height, time, number of transactions and size."
		canonicalPath = withPage("/blocks", td.Page)
	case blockTpl:
		if b := td.Block; b != nil {
			height := groupDigits(int64(b.Height))
			title = td.CoinLabel + " block " + height + " · " + plural(b.TxCount, "transaction")
			description = td.CoinLabel + " block " + height
			if b.Time > 0 {
				title += " · " + time.Unix(b.Time, 0).UTC().Format("2006-01-02")
				description += " mined " + time.Unix(b.Time, 0).UTC().Format("2006-01-02 15:04") + " UTC"
			}
			description += " with " + plural(b.TxCount, "transaction")
			if b.Size > 0 {
				description += ", " + groupDigits(int64(b.Size)) + " bytes"
			}
			description += ". Hash " + b.Hash
			canonicalPath = withPage("/block/"+strconv.FormatUint(uint64(b.Height), 10), td.Page)
			crumbs = []breadcrumb{home, {"Blocks", "/blocks"}, {"Block " + height, "/block/" + strconv.FormatUint(uint64(b.Height), 10)}}
		}
	case txTpl:
		if tx := td.Tx; tx != nil {
			id := shortID(tx.Txid)
			status := plural(int(tx.Confirmations), "confirmation")
			if tx.Confirmations == 0 {
				status = "Unconfirmed"
			}
			failed := tx.EthereumSpecific != nil && tx.EthereumSpecific.Status == bchain.TxStatusFailure
			if failed {
				status = "Failed · " + status
			}
			title = td.CoinLabel + " transaction " + id + " · " + status
			var parts []string
			if failed {
				parts = append(parts, "failed")
			}
			if tx.Blockheight > 0 && tx.Confirmations > 0 {
				p := "confirmed in block " + groupDigits(int64(tx.Blockheight))
				if tx.Blocktime > 0 {
					p += " on " + time.Unix(tx.Blocktime, 0).UTC().Format("2006-01-02")
				}
				parts = append(parts, p)
			} else {
				parts = append(parts, "unconfirmed")
			}
			if td.ChainType != bchain.ChainEthereumType {
				parts = append(parts, plural(len(tx.Vin), "input"), plural(len(tx.Vout), "output"))
			}
			if tx.ValueOutSat != nil {
				parts = append(parts, "value "+s.headAmount(tx.ValueOutSat, td))
			}
			if tx.FeesSat != nil {
				parts = append(parts, "fee "+s.headAmount(tx.FeesSat, td))
			}
			if n := len(tx.TokenTransfers); n > 0 {
				parts = append(parts, plural(n, "token transfer"))
			}
			description = td.CoinLabel + " transaction " + id + ": " + strings.Join(parts, ", ") + "."
			canonicalPath = "/tx/" + url.PathEscape(tx.Txid)
			if tx.Blockheight > 0 && tx.Confirmations > 0 {
				h := strconv.Itoa(tx.Blockheight)
				crumbs = []breadcrumb{home, {"Block " + groupDigits(int64(tx.Blockheight)), "/block/" + h}, {"Transaction " + id, canonicalPath}}
			} else {
				crumbs = []breadcrumb{home, {"Mempool", "/mempool"}, {"Transaction " + id, canonicalPath}}
			}
		}
	case addressTpl:
		if a := td.Address; a != nil {
			addr := a.AddrStr
			id := shortID(addr)
			ci := a.ContractInfo
			if ci != nil && ci.Name != "" {
				title = td.CoinLabel + " contract " + nameWithSymbol(ci.Name, ci.Symbol) + " · " + addr
			} else {
				title = td.CoinLabel + " address " + addr + " · balance " + s.headAmount(a.BalanceSat, td)
			}
			parts := []string{"balance " + s.headAmount(a.BalanceSat, td), plural(a.Txs, "transaction")}
			if td.ChainType == bchain.ChainEthereumType {
				if a.Nonce != "" {
					parts = append(parts, "nonce "+a.Nonce)
				}
				if n := len(a.Tokens); n > 0 {
					parts = append(parts, plural(n, "token"))
				}
			} else {
				if a.TotalReceivedSat != nil {
					parts = append(parts, "received "+s.headAmount(a.TotalReceivedSat, td))
				}
				if a.TotalSentSat != nil {
					parts = append(parts, "sent "+s.headAmount(a.TotalSentSat, td))
				}
			}
			description = td.CoinLabel + " address " + id + ": " + strings.Join(parts, ", ") + "."
			if ci != nil && ci.Name != "" {
				kind := "contract"
				if ci.Standard != "" {
					kind = string(ci.Standard) + " contract"
				}
				description = nameWithSymbol(ci.Name, ci.Symbol) + " " + kind + " on " + td.CoinLabel + ". " + description
			}
			canonicalPath = withPage("/address/"+url.PathEscape(addr), td.Page)
			crumbs = []breadcrumb{home, {"Address " + id, "/address/" + url.PathEscape(addr)}}
		}
	case xpubTpl:
		title = td.CoinLabel + " xpub"
		if td.Address != nil {
			title += " " + shortID(td.Address.AddrStr)
		}
		description = "Balance, addresses and transactions of an extended public key on " + td.CoinLabel + "."
		// an xpub page is private to its owner: no canonical, so the key is not repeated in og:url
		robots = robotsNoindexFollow
	case mempoolTpl:
		title = td.CoinLabel + " mempool"
		if m := td.MempoolTxids; m != nil {
			title += " · " + plural(m.MempoolSize, "unconfirmed transaction")
		}
		if td.Page > 1 {
			title += " · page " + strconv.Itoa(td.Page)
		}
		description = "Unconfirmed " + td.CoinLabel + " transactions waiting to be included in a block."
		canonicalPath = withPage("/mempool", td.Page)
	case sendTransactionTpl:
		title = "Send a raw " + td.CoinLabel + " transaction"
		description = "Broadcast a signed raw " + td.CoinLabel + " transaction, in hex, to the network."
		canonicalPath = "/sendtx"
		robots = robotsNoindexFollow
	case statusTpl:
		title = site + " status"
		description = "Status of the " + td.CoinLabel + " explorer and its backend"
		if td.Info != nil && td.Info.Blockbook != nil {
			bb := td.Info.Blockbook
			sync := "in sync"
			if !bb.InSync {
				sync = "synchronizing"
			}
			description += ": best block " + groupDigits(int64(bb.BestHeight)) + ", " + sync + ", " + plural(bb.MempoolSize, "mempool transaction")
		}
		description += "."
		canonicalPath = "/status"
	case nftDetailTpl:
		if ci := td.ContractInfo; ci != nil {
			name := ci.Name
			if name == "" {
				name = shortID(ci.Contract)
			}
			title = name + " #" + shortID(td.TokenId) + " · " + td.CoinLabel + " NFT"
			kind := "contract"
			if ci.Standard != "" {
				kind = string(ci.Standard) + " contract"
			}
			description = "Token " + shortID(td.TokenId) + " of the " + nameWithSymbol(name, ci.Symbol) + " " + kind + " " + shortID(ci.Contract) + " on " + td.CoinLabel + "."
			canonicalPath = "/nft/" + url.PathEscape(ci.Contract) + "/" + url.PathEscape(td.TokenId)
		}
	case errorTpl, errorInternalTpl:
		text := "Error"
		if td.Error != nil && strings.TrimSpace(td.Error.Text) != "" {
			text = truncateRunes(strings.TrimSpace(td.Error.Text), 70)
		}
		title = text + " · " + site
		description = site + ": " + text
		if td.Robots == "" {
			robots = errorPageRobots(r)
		}
	}
	if title == "" {
		title = site
	}
	if description == "" {
		description = site
	}
	// currency-switch variants (?secondary=XXX&use_secondary=true) repeat every page 47 times
	if q := r.URL.Query(); robots == "" && (q.Has("secondary") || q.Has("use_secondary")) {
		robots = robotsNoindexFollow
	}
	td.PageTitle = title
	td.PageDescription = truncateRunes(description, maxDescriptionLen)
	if robots != "" {
		td.Robots = robots
	}
	if s.publicURL != "" && canonicalPath != "" {
		td.Canonical = s.publicURL + canonicalPath
	}
	td.OGImage = headImage(td)
	td.JSONLD = s.pageJSONLD(t, td, crumbs)
}

// pageJSONLD is the page's schema.org JSON-LD: WebSite (with SearchAction and the operator as
// publisher when the feed carries it) on the home page, BreadcrumbList on block, tx and address
// pages. It needs absolute URLs, so there is none without a public URL.
func (s *PublicServer) pageJSONLD(t tpl, td *TemplateData, crumbs []breadcrumb) template.JS {
	if s.publicURL == "" {
		return ""
	}
	var v any
	switch {
	case t == indexTpl:
		site := map[string]any{
			"@context": "https://schema.org",
			"@type":    "WebSite",
			"name":     td.CoinLabel + " Explorer",
			"url":      s.publicURL + "/",
		}
		if td.InternalExplorer {
			site["potentialAction"] = map[string]any{
				"@type": "SearchAction",
				"target": map[string]any{
					"@type":       "EntryPoint",
					"urlTemplate": s.publicURL + "/search?q={search_term_string}",
				},
				"query-input": "required name=search_term_string",
			}
		}
		if p := td.ExplorerContent.ZelcoreProduct(); p != nil {
			if origin := httpsOrigin(p.TargetURL); p.Name != "" && origin != "" {
				org := map[string]any{"@type": "Organization", "name": p.Name, "url": origin}
				if httpsOrigin(p.IconURL) != "" {
					org["logo"] = p.IconURL
				}
				site["publisher"] = org
			}
		}
		v = site
	case len(crumbs) > 0:
		items := make([]map[string]any, len(crumbs))
		for i, c := range crumbs {
			items[i] = map[string]any{"@type": "ListItem", "position": i + 1, "name": c.name, "item": s.publicURL + c.path}
		}
		v = map[string]any{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": items}
	default:
		return ""
	}
	// json.Marshal escapes <, > and & (and U+2028/2029), so data can never close the <script> element
	b, err := json.Marshal(v)
	if err != nil {
		glog.Error("pageJSONLD: ", err)
		return ""
	}
	return template.JS(b)
}

// headImage is the social preview image: the coin mark from the feed theme, else the Zelcore
// product icon, else none.
func headImage(td *TemplateData) string {
	if icon := td.ExplorerContent.ThemeIcon(); httpsOrigin(icon) != "" {
		return icon
	}
	if p := td.ExplorerContent.ZelcoreProduct(); p != nil && httpsOrigin(p.IconURL) != "" {
		return p.IconURL
	}
	return ""
}

// httpsOrigin returns scheme://host of an absolute https URL, or "" for anything else.
func httpsOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return "https://" + u.Host
}

// headAmount formats an amount for a title or description: "1.5 BTC". A missing amount is 0.
func (s *PublicServer) headAmount(a *api.Amount, td *TemplateData) string {
	v := s.formatAmount(a)
	if td.CoinShortcut != "" {
		v += " " + td.CoinShortcut
	}
	return v
}

// coinWithTicker is "Label (SHORTCUT)", or just the label when the shortcut adds nothing.
func coinWithTicker(td *TemplateData) string {
	if td.CoinShortcut == "" || strings.EqualFold(td.CoinShortcut, td.CoinLabel) {
		return td.CoinLabel
	}
	return td.CoinLabel + " (" + td.CoinShortcut + ")"
}

func nameWithSymbol(name, symbol string) string {
	if symbol == "" || strings.EqualFold(symbol, name) {
		return name
	}
	return name + " (" + symbol + ")"
}

// withPage adds ?page=N to a canonical path when the page is not the first one.
func withPage(path string, page int) string {
	if page > 1 {
		return path + "?page=" + strconv.Itoa(page)
	}
	return path
}

// shortID shortens a long identifier to its first 10 and last 6 characters.
func shortID(id string) string {
	r := []rune(id)
	if len(r) <= 20 {
		return id
	}
	return string(r[:10]) + "…" + string(r[len(r)-6:])
}

// plural is "1 transaction" or "1,234 transactions".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return groupDigits(int64(n)) + " " + noun + "s"
}

// groupDigits formats an integer with comma thousands separators.
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// truncateRunes cuts s to at most max characters, ending with an ellipsis when cut.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ,.:·") + "…"
}
