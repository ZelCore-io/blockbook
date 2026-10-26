# Dashboard content in the existing explorer

## Scope and architecture

This implements the approved stage using Blockbook's existing Go templates. It does not create the deferred TypeScript explorer. The existing Zelcore dashboard owns campaigns and published Zelcore/SSP article selection. Blockbook consumes the version 1 public JSON contract and renders structured, escaped data; neither raw HTML nor dashboard credentials enter the explorer.

The dashboard and consumer PRs are independently deployable. This feature is disabled by default. Branding is a separate PR; no RocksDB schema or index change is required for either presentation change.

## Implementation plan and completion

- [x] Add a standalone standard-library content client with bounded HTTPS fetches, version/schema validation, schedule enforcement and a maximum 24-hour snapshot age.
- [x] Cover successful refresh, restart recovery, invalid responses, outages, schedule boundaries, size/count limits, redirect rejection and mutation isolation with tests.
- [x] Wire an optional background client into the public server lifecycle and cancel it on shutdown.
- [x] Render promotions and article cards in the existing template, with escaping, safe outbound links, lazy images and no-referrer image requests.
- [x] Test both disabled rendering and hostile text escaping.
- [ ] Run the full native Blockbook suite in CI with RocksDB and other C dependencies; local environment lacks the exported CGO paths.

## Operator configuration

After the dashboard PR is deployed, add these flags to the existing Blockbook service command (example hostname must be replaced with the actual dashboard origin):

```
-explorercontenturl=https://dashboard.example/api/public/explorer/v1/config?coin=groestlcoin
-explorercontentcache=/opt/coins/data/groestlcoin/blockbook/explorer-content.json
```

Use the same lowercase coin identifier in dashboard campaign targets and the URL query. An empty campaign coin list means all explorers. The dashboard performs the coin filtering. Each node must use its own correct coin URL; the consumer does not infer coin IDs or trust a query supplied by a public visitor.

If the cache flag is omitted, the file defaults to `explorer-content.json` in the parent of `-datadir`. Its parent directory must already exist and be writable by the service account. It is separate from RocksDB. Cache files are written through a 0600 temporary file and atomic rename. The saved endpoint must match the configured endpoint before a restart cache is used. Cache persistence errors are logged; valid newly fetched content remains available in memory.

The initial refresh is asynchronous, followed by refreshes every five minutes. No dashboard request occurs while serving an explorer page. Requests time out after five seconds, redirects are rejected, and responses are capped at 64 KiB. There are at most eight campaigns, six articles and six products; up to 24 explorer links are kept and the rest are dropped without failing the feed. Invalid responses preserve the last good snapshot. A cache more than 24 hours old is hidden; expired promotions disappear based on the local clock even during an outage. Disable/delete changes propagate on the next successful refresh; during outages previously cached content can remain until its scheduled end or the 24-hour limit. This is not an instant global kill switch.

A malformed endpoint disables content with a warning rather than stopping blockchain indexing. Removing the URL flag disables the feature immediately on restart without deleting the cache. No Ansible or deployment changes are included.

## Public version 1 contract

```json
{
  "version": 1,
  "generatedAt": "2026-10-23T12:00:00Z",
  "campaigns": [{
    "id": "flux", "placement": "hero", "coins": ["groestlcoin"],
    "title": "Explore Flux", "description": "Decentralized cloud infrastructure",
    "imageUrl": "https://example.com/flux.webp", "targetUrl": "https://runonflux.com",
    "startsAt": null, "endsAt": null
  }],
  "articles": [{
    "id": "zelcore:article-id", "source": "zelcore", "title": "Latest news",
    "url": "https://zelcore.io/example", "thumbnailUrl": "https://example.com/article.webp",
    "publishedAt": "2026-10-23T10:00:00Z", "pinned": false
  }],
  "products": [{
    "id": "zelcore", "name": "Zelcore", "tagline": "Multi-asset wallet for 140+ blockchains",
    "targetUrl": "https://zelcore.io", "iconUrl": "https://example.com/zelcore.svg", "ctaLabel": "Get the app",
    "color": "#1B63EF"
  }],
  "theme": { "color": "#2B61D1", "iconUrl": "https://example.com/grs.svg" },
  "explorers": [
    {"coin": "bitcoin", "name": "Bitcoin", "url": "https://blockbook.btc.zelcore.io/", "iconUrl": null}
  ]
}
```

Dates must be RFC3339. `startsAt`/`endsAt` may be null or empty. Article thumbnails may be absent or empty. Supported article sources are `zelcore` and `ssp`. Links/assets must be absolute HTTPS URLs without embedded credentials. The operator configures a trusted endpoint; this is not an arbitrary public URL fetch API. The dashboard establishes article ordering, including pins. Article text is escaped by Go's `html/template`.

`products`, `theme` and `explorers` are optional. A feed from an older dashboard that has none of them still validates; the explorer then renders the default band color, the coin's text mark and no product markup, explorer links or empty headings.

## Products

The dashboard stores at most six products. Each product has an `id` (`^[a-z0-9-]{1,64}$`), a `name` (at most 40 characters), an optional `tagline` (at most 120), a `targetUrl`, an optional `iconUrl`, an optional `ctaLabel` (at most 24, default "Learn more"), an `order`, an `enabled` flag and a `coins` list. An empty `coins` list means all explorers. The dashboard filters by `enabled` and coin, sorts by `order` ascending and serves up to six; `order`, `enabled` and `coins` are not part of the public payload. The explorer rejects a feed with more than six products. An older dashboard serves at most four and no colors; that feed still renders as before, with every tile in the default color.

Products appear on the home page under the heading "Flux ecosystem products", never as "Sponsored". Named products fill the strip in feed order: the first four form the main row, and products five and six appear below them under "More from Flux". The "More from Flux" heading is rendered only when there is a fifth product. A product without a name is skipped and does not take a slot. A product without an icon shows the initials of its name.

`color` is optional and may be absent or null. When present it must match `^#[0-9a-fA-F]{6}$`; an invalid color is dropped when the feed or the restart cache is loaded, and the product stays. The tile shows a 4 px top bar in the product color, or Flux Blue Wave `#2B61D1` when there is none, passed to the stylesheet as the `--tile-accent` custom property in a `style` attribute. Only a validated hex reaches that attribute. The call to action is a filled button with white text when white on the color reaches a WCAG contrast of at least 4.5:1 (Zelcore `#1B63EF` 5.15:1, Flux `#2B61D1` 5.63:1); otherwise it is outlined in the color with black text (SSP `#F59E0B` 2.15:1, CumulusVPN `#0FB9AE` 2.45:1, FluxOS `#36C9A5` 2.09:1). There is no contrast rule on the dashboard for product colors, because the explorer picks the readable treatment.

The product whose `id` is `flux` supplies the Flux symbol: its `iconUrl` is shown next to "Powered by Flux" in the band and the footer. In the band the symbol sits on a white circular chip, like the coin mark, so a blue-disc symbol stays visible on Blue Wave; the logo itself is not recoloured. Without that product or icon the link is text only. Logo files are not committed to this AGPL repository; they are hosted by the dashboard and arrive as HTTPS URLs. The product whose `id` is `zelcore` also feeds the "Watch in Zelcore" prompt in the aside of every inner page, and it is skipped in the footer's product links because the footer already has a fixed Zelcore link. Every other product with a name gets a footer link.

## More explorers

`explorers` lists the other Zelcore explorers. The dashboard stores at most 40 in the explorer settings (`coin` slug, `name`, `url`, optional `iconUrl`, `enabled`, `order`, unique `coin`) and serves the enabled ones without the coin given in `?coin=`, sorted by `order` then `name`, at most 24, with only `coin`, `name`, `url` and `iconUrl`. An older dashboard omits the field.

The explorer never rejects a feed because of this field. A missing, null or malformed `explorers` value (an object, string or number) is an empty list. Each entry is checked on its own when the feed or the restart cache is loaded, and dropped unless `coin` matches `^[a-z0-9-]{1,64}$`, `name` is non-blank and at most 40 characters (runes), `url` is an absolute HTTPS URL without credentials and `iconUrl` is absent, null, empty or such a URL. A second entry with the same `coin` is dropped, and only the first 24 valid entries are kept. A `javascript:` or plain `http:` URL therefore never reaches a page. Other characters in a URL, such as a quote, are percent-encoded by `html/template` in the attribute.

The explorer's own coin is left out twice, both times by the exact coin slug: the dashboard skips the `?coin=` coin when it serves the list, and the client drops any entry whose `coin` equals the `coin` query value of the configured `-explorercontenturl`. The explorer does not guess its coin from the coin label, shortcut or name, so the `-explorercontenturl` must carry the right `?coin=` value (it already must, for campaign and product targeting). The template helper `MoreExplorers` re-checks every entry, so a snapshot built without the feed's validation still renders only valid links.

The home page shows a "More explorers" section after the Blocks/Transactions panel (and its campaign and Network aside) and before the news. Each explorer is a pill button at least 44 px tall with the icon, or the first letter of the name in white on Flux Blue Wave, and the name in black on white; the buttons wrap onto new rows on narrow screens. Every page's footer has a compact "More explorers" row of white links above the usual footer links. Both are omitted when the list is empty. Links open in a new tab with `rel="noopener noreferrer"`; icons load lazily with no referrer and an empty `alt`, because the name is next to them. Explorer links are not promotions and are never labelled "Sponsored".

## Theme

`theme.color` sets the band color for the coin. The dashboard editor stores at most 150 themes with unique `coin` keys (`^[a-z0-9-]{1,40}$`) and accepts only `^#[0-9a-fA-F]{6}$` colors whose contrast ratio against `#FFFFFF` is at least 4.5:1, because the band carries white text. The explorer checks the color format again and rejects the whole feed if it fails, so a value like `red;background:url(//x)` never reaches the page. Without a theme the band is Flux Blue Wave `#2B61D1`. The optional `theme.iconUrl` replaces the coin's text mark in the band; it must be an absolute HTTPS URL like every other asset.

## Campaign slots

Campaigns are labelled "Sponsored" and their links are marked `rel="sponsored"`. Each page has at most one campaign slot:

- The home page shows the first `hero` campaign. If there is none, it shows the first `sidebar` campaign.
- Inner pages show the first `sidebar` campaign in the aside next to the page content. If there is none, they show the first `hero` campaign.
- If neither placement exists, the slot is left out entirely.

There is no rotation: the first matching campaign in feed order wins until the dashboard changes the feed. Images render at 16:10 with `object-fit: cover`, so dashboard images should be made at 16:10 (for example 1280 × 800). Images load from the configured remote asset hosts with no referrer; they are not proxied or tracked by this implementation. On narrow screens the aside drops below the page content. Existing page search, table layout and APIs are unchanged.

## Articles

Articles render on the home page (`/`) only, in a "News from Zelcore and SSP" section that shows the first three in the dashboard's order. Inner pages never show articles. Article text is escaped by Go's `html/template`.

## Status page

The former index tables (Blockbook and backend status, sync state, version, commit and build) moved unchanged to `/status`; the home page is now a landing page with the band figures, products, latest blocks and transactions, the campaign, a network card and articles. `/status` is served from startup, including during initial sync, and linked from every footer. The Historical Fiat rates row is hidden when it has no value.

## Upstream merges

Three templates diverge from upstream Blockbook in ways a `git merge` will not flag, so port upstream changes to them by hand:

- `static/templates/address.html`: UTXO addresses render through the new `addressHub` block. The bitcoin-type `{{else}}` branches inside `addressAccount` no longer run, so an upstream fix there merges cleanly into dead code; port it to `addressHub`.
- `static/templates/index.html` → `static/templates/status.html`: the old index body (the status tables) now lives in `status.html`, and `index.html` is the landing page. Upstream edits to the status tables conflict in `index.html` and belong in `status.html`.
- `static/templates/block.html`: shared by UTXO, EVM and Tron chains and now wrapped in hub cards. Check upstream block-page changes against all three chain types.

## Fonts and licensing

Blockbook stays AGPL-3.0, and the SatoshiLabs copyright notices stay in their files. Every page footer reads "Based on Blockbook by SatoshiLabs · AGPL-3.0 · Source code", where the link points to the running build's source.

The hub stylesheet follows the Flux Brand Book: Montserrat for headings (600, 700, 800) and Open Sans for body text (400, 600, 700), plus Roboto Mono (400, 500) for hashes and amounts. All three are licensed under the SIL Open Font License 1.1. They are self-hosted as latin woff2 files from the `@fontsource` 5.3.0 npm packages under `static/fonts/`, each next to its license file (`static/fonts/montserrat/OFL.txt`, `static/fonts/open-sans/OFL.txt`, `static/fonts/roboto-mono/LICENSE`). Manrope, used earlier in this redesign, was removed. No page loads fonts or other assets from Google Fonts or another third-party font host. The commercially licensed TT Hoves files under `static/css/TTHoves/` were removed.

Colors come only from official palettes: Flux Blue Wave `#2B61D1` with the shades `#4F7AD4`, `#86A1DA`, `#2D2D2D` and `#717171`, black and white, plus each product's own color from the feed. The footer is `#2D2D2D` with `#86A1DA` text (5.33:1) and white links. There is no success or error color: statuses such as "Synced" and "Confirmed" are dark text in an outlined badge.

## Validation and limits

Run `go test -race ./explorercontent` for standalone client and real base-template tests. Tests require no RocksDB. The full server integration remains subject to standard Blockbook CI and a later canary deployment with a real dashboard feed. No runtime banner feed is enabled in this PR. A full visual CMS, impression accounting, billing, asset uploading and the new shared explorer are outside this initial slice.
