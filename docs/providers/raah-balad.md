# Raah / Balad POI integration

This reference covers only the Raah/Balad point-of-interest (POI) flow used by
Mapine. The old Node implementation is the primary source for the legacy
behavior; the current Go provider is separately described where it differs.
The external domains and paths below are observed in source code, not a claim
that Raah publishes or supports a stable public API.

## Source and confidence

The checkout's `origin` is
[`https://github.com/mirzawho/mapine-services`](https://github.com/mirzawho/mapine-services)
and its checked-out `HEAD` matched the remote `HEAD` when inspected. The
relevant committed Node sources are:

* `shared/functions.js`: `getPOITokens`, `getPlacesFromPOITokens`,
  `search`, and `calculateDistance`.
* `services/place.service.js`: published `place.find`, local `find`,
  `formatWithDistance`, and asynchronous `place.save`.
* `shared/mongoose.model.js`: the Mongoose `place` model and its location
  indexes.

The request construction, field access, and transformations below are verified
from those sources. A user-provided live response during debugging confirmed
that the token endpoint uses `camera=longitude,latitude` and that the example
Raah detail geometry is `[longitude,latitude]`; it did not establish the full
provider schema, token syntax, service limits, or current provider terms. The
repository's `*.mock.json` files remain synthetic mock-server fixtures; their
tokens are not real.

## Legacy two-request flow

The old `place.find` action first runs a MongoDB nearby query and a reverse
geocode request concurrently. If the local result is empty, or `force` is
truthy, it calls `functions.search(...)`.

```text
place.find(lat, lon, zoom, category)
  ├─ MongoDB local nearby lookup
  ├─ reverse geocode (separate provider; not part of this reference)
  └─ if local results are empty or force is truthy:
       functions.search(...)
         ├─ GET https://search.raah.ir/v4/bundle-search/
         │    └─ read response.data["poi-tokens"]
         ├─ join tokens with commas
         ├─ GET https://poi.raah.ir/web/v4/preview-bulk/{tokens}
         │    └─ read response.data.items
         ├─ transform each item into a legacy place document
         └─ return places to place.find
               ├─ map provider `id` to public `osm_id`
               ├─ emit `place.save` asynchronously
               └─ return the public `place.find` response
```

### 1. Token discovery

The legacy helper makes:

```http
GET https://search.raah.ir/v4/bundle-search/?camera={lon},{lat}&format=json&slug={category}&zoom={zoom}
```

The source reads only `response.data["poi-tokens"]`; it does not validate or
otherwise use other response fields.

| Query key | Source-derived value |
|---|---|
| `camera` | Provider coordinate order: longitude,latitude. The current Go adapter receives Mapine latitude/longitude and formats `{lon},{lat}` at the provider boundary. |
| `format` | `json` |
| `slug` | The application category (`public-bathroom` or `mosque`) |
| `zoom` | The supplied zoom value |

`getPOITokens()` catches its own Axios failures and returns `[]`. A successful
HTTP response without a usable `poi-tokens` property is not separately
validated there.

### 2. Detail lookup

For non-empty tokens, the legacy helper makes:

```http
GET https://poi.raah.ir/web/v4/preview-bulk/{token-1,token-2,...}
```

It joins the token strings with commas and returns `response.data.items`.
The source does not define an empty-token behavior in this helper; `search()`
avoids calling it when its token list has length zero.

### Coordinate-order caveat

The public Mapine API and all internal Go `Coordinate`/`SearchRequest` values use
standard names (`Latitude`, then `Longitude`). Raah's `camera` query uses the
opposite order, `longitude,latitude`. The Go adapter owns this conversion
centrally, so diagnostic calls and production `place.find` use the same rule.
For example, Mapine `lat=35.6892&lon=51.389` becomes
`camera=51.389,35.6892`. The adapter uses shortest round-trip decimal
formatting rather than forcing a fixed six-decimal precision.

The raw Raah detail example is GeoJSON-style: `geometry.coordinates` is
`[longitude, latitude]`. The Go adapter converts this once with
`CoordinateFromLonLat(lon, lat)` into internal `Coordinate{Latitude, Longitude}`.
For example, raw `[51.372901, 35.693915]` becomes Mapine
`geometry.lat=35.693915`, `geometry.lon=51.372901`.

The legacy JavaScript transformer assigned
   `geometry.coordinates[0]` to a variable named `lat` and index `1` to `lon`.
   It then emitted those values as `geometry.lat` and `geometry.lon`; that
   historical behavior was reversed for Raah's verified response order.

There are further inversions in the legacy persistence path: `place.save`
assigns `lat = item.geometry.lon` and `lon = item.geometry.lat`, then stores
GeoJSON coordinates as `[lon, lat]`. The local Mongo lookup later maps stored
`item.lon` to response `geometry.lat` and `item.lat` to `geometry.lon`. These
inconsistencies are documented rather than silently corrected.

The current Go `place.find` path computes a tile center from the public
`lat`/`lon` and sends that standard-order center in `SearchRequest`. The
diagnostic token and complete-search operations pass their supplied standard
`lat`/`lon` values into that same provider boundary. The adapter swaps only
when constructing Raah's `camera`; internal coordinates, Nominatim requests,
SQLite storage, and R-Tree indexing are unchanged.

## Response stages and transformation

Do not treat the following representations as interchangeable:

1. **Token discovery raw response:** the body returned by `search.raah.ir`.
   Source-derived consumed field: `poi-tokens`, expected by the client to be a
   string array. Other fields are unknown.
2. **POI details raw response:** the body returned by `poi.raah.ir`. The client
   reads `items`; unknown top-level fields are not used by the transform.
3. **Legacy normalized Moleculer document:** each detail item becomes an
   `id/site/schema/url/data/version/createdAt/updatedAt` document.
4. **Current Go `ProviderPlace` and `freshPlace` preview:** the Go adapter's
   parsed fields mapped to the existing public place field names. It is not the
   legacy Moleculer document.
5. **Public `place.find` response:** the application envelope adds
   `status`, `meta`, `places`, and `address`; places are distance-formatted and
   sorted. This remains the existing client contract.
6. **Persistence:** the old `place.save` event and the current Go SQLite
   persistence each store their own subset and do not store the legacy
   Moleculer document verbatim.

### Source-derived legacy mapping

| Provider item/value | Legacy transformed value | Notes |
|---|---|---|
| `token` | `id`; then public/persisted `osm_id` | Historical field name only; token is **not** an OSM ID. |
| constant | `site: "balad.ir"` | Generated by Mapine. |
| constant | `schema: "map:place"` | Generated by Mapine. |
| `geometry.coordinates[0]`, `[1]` | Legacy `data.geometry.lat`, `.lon` | Legacy code mislabeled GeoJSON `[longitude, latitude]`; current Go fixes this at parsing. |
| those same coordinates | Balad bundle URL path | Generated `https://balad.ir/bundles#$16/{lat}/{lon}`. |
| coordinates passed twice | `data.distance` | Haversine distance from a point to itself, therefore zero. |
| constant | `data.distance_unit: "km"` | Generated by Mapine. |
| action category | `data.category` | Generated from the request, not the detail item. |
| `name`, `address`, `thumbnail` | corresponding `data.*` values | Direct pass-through; undefined values may disappear during JSON serialization. |
| constant | `data.city: null`, `data.province: null` | Generated by legacy code. |
| `images[].full` | `data.gallery[]` | Other image properties are discarded. Missing images yield `[]`. |
| `telephone` | `data.telephone` or `null` | Empty/missing values become null; legacy checks `.length`. |
| `rating.score`, `.count` | `data.rating.score`, `.count` | Missing rating or fields default to zero in the legacy transform. |
| constant/current time | `version: 1`, `createdAt`, `updatedAt` | Generated by legacy code. |

The current Go adapter interprets the verified Raah pair as
`coordinates[0] -> longitude`, `coordinates[1] -> latitude`. It retains the raw
item JSON in `ProviderPlace.Raw`. The public Go `freshPlace` maps the token to
the legacy-compatible `osm_id` key and includes geometry, distance,
`distance_unit`, category, city/province, gallery, telephone, rating, and
available name/address/thumbnail values. It does **not** emit the legacy
`site`, `schema`, Balad URL, version, or timestamps.

Current Go SQLite persistence stores source `balad.ir`, token as `source_id`,
category, normalized fields, and raw detail item JSON in `source_json`; it does
not persist the legacy document shape.

The Go persistence is synchronous: `place.find` waits for the provider result,
upserts normalized rows in a SQLite transaction, then marks tile coverage.
Places store `latitude=Coordinate.Latitude` and
`longitude=Coordinate.Longitude`. The R-Tree triggers index X from longitude
and Y from latitude; nearby lookup uses longitude bounds before latitude bounds
and then applies a Haversine distance filter. Fresh and cached responses use
the same `distanceKM` calculation, in kilometers, rounded to two decimals.

On startup, a one-time repair checks only `source='balad.ir'` rows whose raw
`source_json.geometry.coordinates` is `[lon,lat]` and whose stored columns
exactly match the old reversed parser assignment. If any match, Mapine creates
a consistent SQLite backup beside the database, swaps those rows in a
transaction (allowing the R-Tree update trigger to run), verifies index
consistency, and logs the repair count. Other providers and rows without
matching raw evidence are left untouched.

### Legacy error behavior

* Token discovery transport errors become `[]` inside `getPOITokens`. The
  calling `search()` then returns `{status: 400, message: "EMPTY_TOKENS"}`.
* A successful empty token array follows the same `EMPTY_TOKENS` path.
* If detail lookup throws, `search()` catches it and returns `{code: 500}`.
* An empty `items` array produces a successful empty `places` result.
* Missing/malformed `items` or missing geometry can throw while iterating and
  transform into the caught `{code: 500}` result.
* When the returned provider result is not the expected success shape,
  `place.find` falls through to its local-result response. A no-data response
  may therefore conceal the underlying provider failure.

The Go diagnostic endpoints intentionally distinguish provider errors (HTTP
502 plus an error and upstream data when available) from valid empty results
(HTTP 200 and an empty list). They do not change the existing
`/v1/call/place.find` behavior.

## Scalar diagnostics

Open `/docs`; the **Raah / Balad Provider** tag contains:

* `GET /_debug/providers/raah/tokens`: directly request tokens and inspect the
  raw discovery response.
* `POST /_debug/providers/raah/details`: paste one to 50 real tokens and
  inspect the raw `items` response.
* `GET /_debug/providers/raah/search`: run both upstream stages and compare the
  raw data with the current Go `freshPlace` preview.

These are **local Mapine diagnostic routes**, not upstream Raah contracts.
Scalar uses same-origin requests; it does not call either Raah host from the
browser, avoiding external CORS dependence. Each request passes through the
existing configured Go provider (HTTP timeout, up-to-8-MiB upstream read cap,
retries, token batching, and `POI_MIN_INTERVAL`). Token lists are limited to 50
for the standalone detail operation; complete search uses the provider's
existing 50-token batches and 500-token total limit.

### Enable only in an authorized environment

Diagnostics are disabled by default and return 404. Set `DEBUG=true` and restart
the service to enable them. No token or authorization header is required:

```env
DEBUG=true
```

For example, locally:

```sh
export DEBUG=true
go run ./cmd/api
```

The configured POI URLs must use HTTPS and the exact trusted hosts
`search.raah.ir` and `poi.raah.ir`; arbitrary hosts and URL forwarding are not
accepted. The routes have no authentication, so do not expose a debug-enabled
service to untrusted networks.

### Manual cURL

Token discovery:

```sh
curl 'http://localhost:3000/_debug/providers/raah/tokens?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom'
```

Copy an actual token from `poi_tokens`, then request details:

```sh
curl -X POST 'http://localhost:3000/_debug/providers/raah/details' \
  -H 'Content-Type: application/json' \
  --data '{"tokens":["PASTE_A_REAL_RETURNED_TOKEN"]}'
```

Run the full provider workflow:

```sh
curl 'http://localhost:3000/_debug/providers/raah/search?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom'
```

The detail cURL value above is a placeholder, not a captured or valid provider
token. Diagnostics do not write to SQLite, emit `place.save`, change the
coverage cache, call reverse geocoding, or accept a user-supplied upstream URL.

## Known limitations

* No live request was made to Raah/Balad for this change. The mock fixtures are
  not evidence of provider behavior.
* Coordinate ordering and the provider's complete response schema remain
  unverified. The tools expose the raw response so an authorized tester can
  establish those facts.
* Provider rate limits, terms, availability guarantees, and permission for
  caching/redistribution have not been verified. The Go provider's configured
  minimum interval is an implementation setting, not a provider-published
  quota.
* The diagnostic token route accepts integer zooms (1–21), matching the Go
  provider interface; the legacy Node action validator accepted numeric zoom
  values and forwarded the value without an integer check.
