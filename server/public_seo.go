package server

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/explorercontent"
)

// Crawl support for the public explorer: real 404 pages, robots.txt, sitemap.xml, llms.txt,
// the favicon redirect and long cache headers for settled block and tx pages.

const (
	// deepPageConfirmations is how many confirmations a block or tx page needs before it is
	// treated as settled and served with deepPageCacheControl.
	deepPageConfirmations = 100
	// settled tx pages are cached for 10 minutes (a reorg or a late currency/rates change must
	// not linger), with stale-while-revalidate so a cache can serve the old copy while it
	// fetches a new one; block pages are immutable enough for a day.
	deepTxCacheControl    = "public, max-age=600, stale-while-revalidate=3600"
	deepBlockCacheControl = "public, max-age=86400"
	// sitemapBlocks is the number of latest block pages listed in sitemap.xml.
	sitemapBlocks = 100

	errorHintCheckID = "Check the hash or address and try again."
)

// aiCrawlers are the AI and answer-engine crawlers robots.txt names explicitly, so it is clear
// they are welcome; they get the same rules as every other crawler.
var aiCrawlers = []string{
	"GPTBot", "OAI-SearchBot", "ChatGPT-User",
	"ClaudeBot", "Claude-User", "Claude-SearchBot",
	"PerplexityBot", "Perplexity-User",
	"Google-Extended", "Applebot-Extended", "CCBot",
}

// setDeepPageCacheControl lets shared caches keep a block or tx page once it has more than
// deepPageConfirmations confirmations (cacheControl is deepTxCacheControl or deepBlockCacheControl), but only when the response is the same for every
// visitor: the request carries no secondary_coin cookie and no secondary/use_secondary query
// parameters, so the page shows the default currency and postHtmlTemplateHandler sends no
// Set-Cookie (main.js creates the default cookie in the browser). Vary: Cookie keeps a visitor's
// own currency choice out of the shared copy.
func setDeepPageCacheControl(w http.ResponseWriter, r *http.Request, data *TemplateData, confirmations int64, cacheControl string) {
	if confirmations <= deepPageConfirmations || data == nil || !defaultSecondaryRequest(r) {
		return
	}
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Add("Vary", "Cookie")
}

// defaultSecondaryRequest reports whether the request leaves the secondary currency at its
// default: no secondary_coin cookie and no secondary or use_secondary query parameter.
func defaultSecondaryRequest(r *http.Request) bool {
	q := r.URL.Query()
	if _, ok := q["secondary"]; ok {
		return false
	}
	if _, ok := q["use_secondary"]; ok {
		return false
	}
	if _, err := r.Cookie(secondaryCoinCookieName); err == nil {
		return false
	}
	return true
}

// notFoundPage renders the hub error page for a path no route serves; the error template is
// sent with 404 (errorTplStatus).
func (s *PublicServer) notFoundPage(r *http.Request) (tpl, *TemplateData, error) {
	data := s.newTemplateDataWithError(&api.APIError{Text: "There is no page at this address.", Public: true}, r)
	data.ErrorTitle = "Page not found"
	data.ErrorHint = "Search for a block, transaction or address instead."
	return errorTpl, data, nil
}

// basePathPrefix is the binding's path prefix without the trailing slash ("" for the root).
func (s *PublicServer) basePathPrefix() string {
	_, path := splitBinding(s.binding)
	return strings.TrimRight(path, "/")
}

// baseURL is the absolute URL of the explorer root without a trailing slash: the configured
// public_url, else the request's scheme and host (X-Forwarded-Proto is honoured, as the
// explorer usually runs behind a TLS-terminating proxy) plus the binding's path prefix.
func (s *PublicServer) baseURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); p == "http" || p == "https" {
		scheme = p
	}
	host := r.Host
	if !validHost(host) {
		host = "localhost"
	}
	return scheme + "://" + host + s.basePathPrefix()
}

// validHost accepts a host[:port] made of the characters a hostname, IPv4 or bracketed IPv6
// literal may contain, so a forged Host header cannot inject markup or paths into the output.
func validHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}

// crawlCacheControl is the Cache-Control of a body built from baseURL: public caching only when
// public_url is set; otherwise the body carries the request's own Host and X-Forwarded-Proto, so
// a shared cache must not hand it to other visitors.
func crawlCacheControl(publicURL, public string) string {
	if publicURL == "" {
		return "no-store"
	}
	return public
}

func writeCrawlText(w http.ResponseWriter, contentType, cacheControl, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", cacheControl)
	if _, err := w.Write([]byte(body)); err != nil {
		glog.Warning("write response ", err)
	}
}

// robotsTxt is the robots.txt body: everything is crawlable except the API, search, sendtx,
// xpub pages and the secondary-currency variants of every page.
func (s *PublicServer) robotsTxt() string {
	p := s.basePathPrefix()
	rules := "Allow: " + p + "/\n" +
		"Disallow: " + p + "/api/\n" +
		"Disallow: " + p + "/search\n" +
		"Disallow: " + p + "/sendtx\n" +
		"Disallow: " + p + "/xpub/\n" +
		"Disallow: " + p + "/*?*secondary=\n" +
		"Disallow: " + p + "/*?*use_secondary=\n"
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString(rules)
	b.WriteString("\n# AI and answer-engine crawlers are welcome, with the same rules.\n")
	for _, ua := range aiCrawlers {
		b.WriteString("User-agent: " + ua + "\n")
	}
	b.WriteString(rules)
	if s.publicURL != "" {
		b.WriteString("\nSitemap: " + s.publicURL + "/sitemap.xml\n")
	}
	return b.String()
}

func (s *PublicServer) robotsTxtHandler(w http.ResponseWriter, r *http.Request) {
	if !allowGetOrHead(w, r) {
		return
	}
	writeCrawlText(w, "text/plain; charset=utf-8", "public, max-age=3600", s.robotsTxt())
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// sitemap lists the stable pages and the latest sitemapBlocks block pages; tx and address
// pages are deliberately left out. Block pages only exist with the full internal explorer.
func (s *PublicServer) sitemap(base string) ([]byte, error) {
	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	withBlocks := s.internalExplorer && s.isFullInterface
	var blocks []sitemapURL
	var latest string
	if withBlocks {
		bs, err := s.api.GetBlocks(1, sitemapBlocks)
		if err != nil {
			return nil, err
		}
		for _, b := range bs.Blocks {
			var lastmod string
			if b.Time > 0 {
				lastmod = time.Unix(b.Time, 0).UTC().Format(time.RFC3339)
			}
			if latest == "" {
				latest = lastmod
			}
			blocks = append(blocks, sitemapURL{Loc: base + "/block/" + strconv.FormatUint(uint64(b.Height), 10), LastMod: lastmod})
		}
	}
	set.URLs = append(set.URLs, sitemapURL{Loc: base + "/", LastMod: latest})
	if withBlocks {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + "/blocks", LastMod: latest})
	}
	set.URLs = append(set.URLs, sitemapURL{Loc: base + "/api-docs"}, sitemapURL{Loc: base + "/status"})
	set.URLs = append(set.URLs, blocks...)
	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

func (s *PublicServer) sitemapHandler(w http.ResponseWriter, r *http.Request) {
	if !allowGetOrHead(w, r) {
		return
	}
	body, err := s.sitemap(s.baseURL(r))
	if err != nil {
		glog.Error("sitemap: ", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	writeCrawlText(w, "application/xml; charset=utf-8", crawlCacheControl(s.publicURL, "public, max-age=300"), string(body))
}

// llmsTxt describes the explorer for language models (llmstxt.org): what it is, its page
// patterns and its public API. Everything comes from the coin config and the server setup.
func (s *PublicServer) llmsTxt(base string) string {
	return s.llmsTxtFor(base, s.content.Snapshot(time.Now()))
}

// llmsTxtFor renders llms.txt for the given explorer content snapshot.
func (s *PublicServer) llmsTxtFor(base string, snap explorercontent.Snapshot) string {
	label := s.is.CoinLabel
	if label == "" {
		label = s.is.Coin
	}
	coin := s.is.Coin
	if s.is.CoinShortcut != "" {
		coin += " (" + s.is.CoinShortcut + ")"
	}
	var b strings.Builder
	b.WriteString("# " + label + " Explorer\n\n")
	b.WriteString("> Blockchain explorer for " + coin + ": blocks, transactions and addresses.")
	// operator and platform come from the explorer content feed, like the JSON-LD publisher
	if line := productLine(snap.ZelcoreProduct()); line != "" {
		b.WriteString(" Operated by " + line + ".")
	}
	if line := productLine(snap.ProductByID("flux")); line != "" {
		b.WriteString(" Powered by " + line + ".")
	}
	b.WriteString(" Based on Blockbook by SatoshiLabs, AGPL-3.0.\n\n")
	b.WriteString("Canonical host: " + base + "\n\n")
	// the page patterns are listed only when the full internal explorer serves them (the sitemap's condition)
	if s.internalExplorer && s.isFullInterface {
		b.WriteString("## Pages\n\n")
		b.WriteString("- [Home](" + base + "/): latest blocks and transactions\n")
		b.WriteString("- [Blocks](" + base + "/blocks): all blocks, newest first\n")
		b.WriteString("- Block: " + base + "/block/{height or hash}\n")
		b.WriteString("- Transaction: " + base + "/tx/{txid}\n")
		b.WriteString("- Address: " + base + "/address/{address}\n")
		b.WriteString("- [Mempool](" + base + "/mempool): unconfirmed transactions\n")
		b.WriteString("- [Status](" + base + "/status): index and backend status\n\n")
	} else {
		b.WriteString("## Pages\n\n")
		b.WriteString("- [Home](" + base + "/)\n")
		b.WriteString("- [Status](" + base + "/status): index and backend status\n\n")
	}
	b.WriteString("## API\n\n")
	b.WriteString("- [API documentation](" + base + "/api-docs) (OpenAPI: " + base + "/openapi.yaml)\n")
	b.WriteString("- Status: " + base + "/api/v2/\n")
	b.WriteString("- Block: " + base + "/api/v2/block/{height or hash}\n")
	b.WriteString("- Transaction: " + base + "/api/v2/tx/{txid}\n")
	b.WriteString("- Address: " + base + "/api/v2/address/{address}\n")
	if s.chainParser.GetChainType() == bchain.ChainBitcoinType {
		b.WriteString("- Extended public key: " + base + "/api/v2/xpub/{xpub}\n")
		b.WriteString("- Unspent outputs: " + base + "/api/v2/utxo/{address or xpub}\n")
	}
	b.WriteString("- Fee estimate: " + base + "/api/v2/estimatefee/{blocks}\n")
	if s.is.HasFiatRates {
		b.WriteString("- Fiat exchange rates: " + base + "/api/v2/tickers/\n")
	}
	b.WriteString("\nThe API is rate limited; use it for lookups, not bulk crawling.\n")
	return b.String()
}

// productLine is "Name (https://origin)" for a feed product with a name and an https target, else "".
func productLine(p *explorercontent.Product) string {
	if p == nil {
		return ""
	}
	name := strings.Join(strings.Fields(p.Name), " ")
	origin := httpsOrigin(p.TargetURL)
	if name == "" || origin == "" {
		return ""
	}
	return name + " (" + origin + ")"
}

func (s *PublicServer) llmsTxtHandler(w http.ResponseWriter, r *http.Request) {
	if !allowGetOrHead(w, r) {
		return
	}
	writeCrawlText(w, "text/plain; charset=utf-8", crawlCacheControl(s.publicURL, "public, max-age=3600"), s.llmsTxt(s.baseURL(r)))
}

// faviconTarget is where /favicon.ico redirects: the Zelcore product icon from the explorer
// content feed when it is an absolute https URL, else "".
func faviconTarget(snap explorercontent.Snapshot) string {
	p := snap.ZelcoreProduct()
	if p == nil || p.IconURL == "" {
		return ""
	}
	u, err := url.Parse(p.IconURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String()
}

// faviconHandler redirects /favicon.ico to faviconTarget; without one (no feed, stale feed, no
// icon) it falls back to the static file, which the fork does not ship, so the answer is 404.
func (s *PublicServer) faviconHandler(fallback http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if target := faviconTarget(s.content.Snapshot(time.Now())); target != "" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		fallback.ServeHTTP(w, r)
	}
}
