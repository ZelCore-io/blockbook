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

The initial refresh is asynchronous, followed by refreshes every five minutes. No dashboard request occurs while serving an explorer page. Requests time out after five seconds, redirects are rejected, and responses are capped at 64 KiB. There are at most eight campaigns, six articles and four products. Invalid responses preserve the last good snapshot. A cache more than 24 hours old is hidden; expired promotions disappear based on the local clock even during an outage. Disable/delete changes propagate on the next successful refresh; during outages previously cached content can remain until its scheduled end or the 24-hour limit. This is not an instant global kill switch.

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
    "targetUrl": "https://zelcore.io", "iconUrl": "https://example.com/zelcore.svg", "ctaLabel": "Get the app"
  }],
  "theme": { "color": "#0A5A8C", "iconUrl": "https://example.com/grs.svg" }
}
```

Dates must be RFC3339. `startsAt`/`endsAt` may be null or empty. Article thumbnails may be absent or empty. Supported article sources are `zelcore` and `ssp`. Links/assets must be absolute HTTPS URLs without embedded credentials. The operator configures a trusted endpoint; this is not an arbitrary public URL fetch API. The dashboard establishes article ordering, including pins. Article text is escaped by Go's `html/template`.

`products` and `theme` are optional. A feed from an older dashboard that has neither still validates; the explorer then renders the default band color, the coin's text mark and no product markup or empty headings.

## Products

The dashboard stores at most six products. Each product has an `id` (`^[a-z0-9-]{1,64}$`), a `name` (at most 40 characters), an optional `tagline` (at most 120), a `targetUrl`, an optional `iconUrl`, an optional `ctaLabel` (at most 24, default "Learn more"), an `order`, an `enabled` flag and a `coins` list. An empty `coins` list means all explorers. The dashboard filters by `enabled` and coin, sorts by `order` ascending and serves the first four; `order`, `enabled` and `coins` are not part of the public payload. The explorer rejects a feed with more than four products.

Products appear on the home page under the heading "Zelcore products", never as "Sponsored". A product without an icon shows the initials of its name. The product whose `id` is `zelcore` also feeds the "Watch in Zelcore" prompt in the aside of every inner page, and it is skipped in the footer's product links because the footer already has a fixed Zelcore link. Every other product with a name gets a footer link.

## Theme

`theme.color` sets the band color for the coin. The dashboard editor stores at most 150 themes with unique `coin` keys (`^[a-z0-9-]{1,40}$`) and accepts only `^#[0-9a-fA-F]{6}$` colors whose contrast ratio against `#FFFFFF` is at least 4.5:1, because the band carries white text. The explorer checks the color format again and rejects the whole feed if it fails, so a value like `red;background:url(//x)` never reaches the page. Without a theme the band is `#0A5A8C`. The optional `theme.iconUrl` replaces the coin's text mark in the band; it must be an absolute HTTPS URL like every other asset.

## Campaign slots

Campaigns are labelled "Sponsored" and their links are marked `rel="sponsored"`. Each page has at most one campaign slot:

- The home page shows the first `hero` campaign. If there is none, it shows the first `sidebar` campaign.
- Inner pages show the first `sidebar` campaign in the aside next to the page content. If there is none, they show the first `hero` campaign.
- If neither placement exists, the slot is left out entirely.

There is no rotation: the first matching campaign in feed order wins until the dashboard changes the feed. Images render at 16:10 with `object-fit: cover`, so dashboard images should be made at 16:10 (for example 1280 × 800). Images load from the configured remote asset hosts with no referrer; they are not proxied or tracked by this implementation. On narrow screens the aside drops below the page content. Existing page search, table layout and APIs are unchanged.

## Articles

Articles render on the home page (`/`) only, in a "News from Zelcore and SSP" section that shows the first three in the dashboard's order. Inner pages never show articles. Article text is escaped by Go's `html/template`.

## Status page

The former index tables (Blockbook and backend status, sync state, version, commit and build) moved unchanged to `/status`; the home page is now a landing page with the band figures, products, latest blocks and transactions, the campaign, a network card and articles. `/status` is served by the full public interface and linked from every footer. The Historical Fiat rates row is hidden when it has no value.

## Fonts and licensing

Blockbook stays AGPL-3.0, and the SatoshiLabs copyright notices stay in their files. Every page footer reads "Based on Blockbook by SatoshiLabs · AGPL-3.0 · Source code", where the link points to the running build's source.

The hub stylesheet uses Manrope and Roboto Mono. Both are licensed under the SIL Open Font License 1.1 (the plan's mention of Apache for one of them was wrong). They are self-hosted as woff2 files under `static/fonts/`, each next to its license file (`static/fonts/manrope/OFL.txt`, `static/fonts/roboto-mono/LICENSE`). No page loads fonts or other assets from Google Fonts or another third-party font host. The commercially licensed TT Hoves files under `static/css/TTHoves/` were removed.

## Validation and limits

Run `go test -race ./explorercontent` for standalone client and real base-template tests. Tests require no RocksDB. The full server integration remains subject to standard Blockbook CI and a later canary deployment with a real dashboard feed. No runtime banner feed is enabled in this PR. A full visual CMS, impression accounting, billing, asset uploading and the new shared explorer are outside this initial slice.
