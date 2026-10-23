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

The initial refresh is asynchronous, followed by refreshes every five minutes. No dashboard request occurs while serving an explorer page. Requests time out after five seconds, redirects are rejected, and responses are capped at 64 KiB. There are at most eight campaigns and six articles. Invalid responses preserve the last good snapshot. A cache more than 24 hours old is hidden; expired promotions disappear based on the local clock even during an outage. Disable/delete changes propagate on the next successful refresh; during outages previously cached content can remain until its scheduled end or the 24-hour limit. This is not an instant global kill switch.

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
  }]
}
```

Dates must be RFC3339. `startsAt`/`endsAt` may be null or empty. Article thumbnails may be absent or empty. Supported article sources are `zelcore` and `ssp`. Links/assets must be absolute HTTPS URLs without embedded credentials. The operator configures a trusted endpoint; this is not an arbitrary public URL fetch API. The dashboard establishes article ordering, including pins. Article text is escaped by Go's `html/template`.

`hero` promotions appear before the page's blockchain data. `sidebar` is a contract placement key rendered as responsive secondary cards **below** blockchain data in the current full-width explorer; it does not introduce a narrow sidebar into address/transaction tables. Articles follow those cards. Marketing links are marked sponsored. Images load from configured remote asset hosts with no referrer; they are not proxied or tracked by this implementation. Existing page search, table layout and APIs are unchanged.

## Validation and limits

Run `go test -race ./explorercontent` for standalone client and real base-template tests. Tests require no RocksDB. The full server integration remains subject to standard Blockbook CI and a later canary deployment with a real dashboard feed. No runtime banner feed is enabled in this PR. A full visual CMS, impression accounting, billing, asset uploading and the new shared explorer are outside this initial slice.
