# Log diagnosis — `asleep-athena-metatube-sdk-go_2026-10-11T15_43_16.636Z.txt`

**TL;DR — The server is healthy; the JavBus provider is broken.** Every JavBus movie lookup
(868/868) failed with HTTP 500 because JavBus recently started requiring a `dv=1` cookie on
detail pages, and this deployment's scraper predates the fix. The DB `record not found` lines
are harmless cache misses, not the error. Upstream merged the one-line fix
([metatube-sdk-go#378](https://github.com/metatube-community/metatube-sdk-go/pull/378)) on
2026-10-11 16:06 — about 1.5 h **after** this log ends — and it has been applied to this repo.

---

## What the log contains

Window: **2026-10-11 15:09:51 → 15:37:40** (~28 min), all from one client `113.211.214.45`,
a sequential library scan plus per-item retries.

| Outcome | Count | What |
|---|---|---|
| HTTP 500 | **933 (94%)** | 868 × `/v1/movies/JavBus/<ID>?lazy=True`, 65 × `/v1/images/.../JavBus|JAV321/...` |
| HTTP 200 | 59 | JavDB movies (7/7), translate, actor search, JavDB images |
| HTTP 404 | 2 | actor `トニー大木` not found |

- **JavBus `/v1/movies/...`: 868/868 → 500 (100% failure).** 499 distinct IDs, retried 1–7× each.
- **JavDB `/v1/movies/...`: 7/7 → 200**, with successful `INSERT INTO movie_metadata` rows.
- Latency of the failing JavBus lookups: **p50 46 ms, p90 50 ms** (max 866 ms). A successful
  scrape of a comparable provider takes 100–200 ms (JavDB movie fetches: 103–177 ms).
  ~46 ms = a fast non-page response (redirect / block page), not a real fetch and not a timeout.

## What the code path does with those requests

`GET /v1/movies/JavBus/<ID>?lazy=True` → `engine/movie.go`:

1. `getMovieInfoFromDB` (`movie.go:206`, `First(info)`) — **cache miss**, which is what GORM
   logs as `record not found` (949×). This is *expected* noise on first lookup of each ID and
   is **not** the failure.
2. On miss, it scrapes the provider: `GetMovieInfoByID` → `https://www.javbus.com/ja/<ID>`.
3. The scrape returns no usable fields (no title / cover / number) → `info.IsValid()` is false →
   the engine's validation defer (`movie.go:211–215`) converts it to
   `ErrIncompleteMetadata` = **HTTP 500 "incomplete metadata"** (`provider/errors.go:16`).
   Nothing about the real reason is ever logged — hence the log looks mysterious.

## Root cause: missing `dv=1` cookie on JavBus

This checkout of `Hau04/metatube-sdk-go` is a snapshot fork (single commit, `279a440`). Diffing
`provider/javbus/javbus.go` against upstream `metatube-community/metatube-sdk-go@main` shows
**exactly one difference**:

```go
 scraper.WithCookies(baseURL, []*http.Cookie{
+    {Name: "dv", Value: "1"},   // <-- missing in this deployment
     {Name: "existmag", Value: "all"},
 })),```

Upstream commit [`a171425`](https://github.com/metatube-community/metatube-sdk-go/commit/a17142555b100ed19791e6c84f9d1da3e2f546ff)
(“Fix(provider): add JavBus dv cookie”, PR #378) merged **2026-10-11 16:06** — right after this
log window. JavBus now gates movie detail pages behind this cookie; without it the page comes
back as a redirect/interstitial rather than a movie page.

The provider is built with `scraper.WithDisableRedirects()`, which sets
`ParseHTTPErrorResponse = true` + `http.ErrUseLastResponse`: a 3xx/4xx body is silently
"parsed", matches none of the XPath selectors, and the failure surfaces as
`incomplete metadata` — perfectly matching the uniform ~46 ms 500s.

Why other things still worked:

- **JavBus search** (`/ja/search/...`) apparently doesn't require the cookie (the two
  `/v1/movies/search` calls returned 200 in 10 s). The ENGINE per-provider dump even shows
  `JavBus(176ms):<no error>` — but "no error" with `ParseHTTPErrorResponse=true` just means
  "no transport error"; it can still return 0 results.
- **JavDB** doesn't use this cookie scheme → fully working (movies, images, DB writes).
- **Translate (GoogleFree), actor search** → working.
- **`/v1/images/.../JavBus/...` 500s (61)** are collateral: `GetMovieThumbImage /
  GetMoviePrimaryImage / GetMovieBackdropImage` resolve movie info first
  (`engine/image.go:getPreferredMovieImageURLAndInfo`), which fails identically.
- **JAV321 image 500s (4)** are collateral of JAV321 being unreachable (below).

## Fixes applied

1. **JavBus** — `provider/javbus/javbus.go` now sends `{Name: "dv", Value: "1"}` (upstream #378).
   Rebuild/redeploy and the 868× JavBus 500s stop.
2. **FANZA** — three-layer fix for the `__next_f` search failure:
   - `searchMovieNext` (and the mono detail path) now visit `age_check/=/declared=yes/?rurl=<target>`
     first, which performs DMM's age-check handshake and stores whatever cookies it sets in the
     shared collector jar (fixes stale/broken `age_check_done` handling whatever the cookie shape).
   - Interstitial pages are now detected and reported explicitly:
     `not-available-in-your-region` → `ErrRegionNotAvailable`, age-check wall → clear error —
     instead of the cryptic `var 'self' does not contain key '__next_f'`.
   - `searchparse` gained a regex fallback that extracts `self.__next_f.push([1,"…"])` flight
     payloads directly from raw script text (no JS evaluation), covering pages whose inline
     scripts can't run under the otto runtime. Verified against the package's real-page fixture.
3. **JAV321** — `provider/jav321/jav321.go`:
   - Request timeout is now honored and **clamped to 5s** (was hardcoded 10s and ignored config),
     so a dead origin can no longer stall every `movies/search` for 10s.
   - Added a 2-minute circuit breaker: after a timeout/5xx the provider is skipped instantly
     (`jav321 temporarily disabled after failure: …`) instead of re-hanging on every request.
   - Note: at log time jav321.com's origin was down for everyone (Cloudflare 522); when the
     site is healthy again the provider works with no config needed.
4. **GORM noise** — `database/database.go` now sets `IgnoreRecordNotFoundError: true`; the 949
   `movie.go:206 record not found` cache-miss lines stop polluting the log.
5. **Silent failures** — `engine/movie.go` / `engine/actor.go` now log every failed
   `GetMovieInfo` / `GetActorInfo` with `provider`, `id` and cause (the old log gave zero
   indication of *why* requests 500'd), and unexpected actor-search provider errors are logged
   instead of swallowed.

## Not fixable in code: IP/geo blocks (MGS, SOD, DUGA)

Verified from multiple vantage points: **SOD** (`ec.sod.co.jp`) returns nginx `403 Forbidden`,
**MGS** (`mgstage.com`) returns 403/500 to datacenter IPs, **DUGA** throttles some IPs with 503
while working fine from clean IPs. These sites block the server's egress IP outright; only a
residential/JP egress fixes them. The engine already supports per-provider proxies — no rebuild
needed, just environment variables:

```sh
MT_PROVIDER_MGS__PROXY=socks5://user:pass@jp-proxy:1080
MT_PROVIDER_SOD__PROXY=http://user:pass@jp-proxy:3128
MT_PROVIDER_DUGA__PROXY=http://user:pass@proxy:3128
MT_PROVIDER_FANZA__PROXY=socks5://user:pass@jp-proxy:1080   # FANZA needs a JP egress
# per-type variants also work: MT_MOVIE_PROVIDER_<NAME>__<KEY>
# other keys: priority (float), timeout (duration, e.g. 3s)
```

FANZA likewise serves `not-available-in-your-region` to non-JP IPs; with the fix above this is
now reported as a clear error, and a JP proxy makes search work.

## Other observations from the log (explained)

- **Actor `トニー大木` → 404 in ~2ms** is expected behavior, not a bug: actor search is backed by
  Gfriends (in-memory Filetree lookup, hence the 2ms responses after the first), AV-League and
  ThePornDB. Male performers aren't in those datasets. Adding a JavDB-backed actor search would
  be a feature request. Unexpected provider errors during actor search are now logged (fix 5).
- The client retried failing IDs 2–7× (499 IDs → 868 requests), amplifying the outage; this stops
  on its own once the providers respond.

## Secondary issues visible in the log (not the main outage)

From the two `[ENGINE] ... Search keyword ...` dumps (per-provider search diagnostics):

| Provider | Symptom | Impact |
|---|---|---|
| **FANZA** | `var 'self' does not contain key '__next_f'` | provider parser broken — FANZA moved to a Next.js flight payload; `searchMovieAll` can never return FANZA hits until the parser is updated |
| **JAV321** | `Post "https://www.jav321.com/search": context deadline exceeded` (10 s) | every `movies/search` (all-providers) takes a full 10 s waiting on it |
| **MGS, SOD** | `Forbidden` (403) | IP-blocked from this server (datacenter IP); expected |
| **DUGA** | `Service Unavailable` (503) | upstream flake/block |

Other noise/observations:

- The 949 GORM `record not found` lines at `movie.go:206` are lazy-cache misses — expected, but
  GORM's logger makes them look like the error. Consider silencing `gorm.ErrRecordNotFound`
  in the GORM logger to keep future logs readable.
- The client retried failing IDs 2–7× (499 IDs → 868 requests), amplifying the outage. Once
  JavBus works again this stops on its own.
- Actor `トニー大木` → 404 twice in ~2 ms: cached miss / no match in the providers consulted;
  unrelated to the outage.

## Verification steps after deploy

1. `curl '<base>/v1/movies/JavBus/IPZZ-654?lazy=True'` → expect 200 with title/cover populated.
2. `curl '<base>/v1/images/primary/JavBus/IPZZ-654?...'` → expect an image.
3. `curl '<base>/v1/movies/search?q=SNOS-384&fallback=True'` → expect 200 in ≲2s (was 10s), and
   FANZA either contributing results or a clear `[ENGINE] ... FANZA(...):<not-available-in-your-region>`
   line — never `__next_f` again.
4. Watch logs: `record not found` spam is gone; failed lookups now log
   `[ENGINE] GetMovieInfo failed: provider=... id=... err=...` with the real cause.
