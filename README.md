# Mapine Services — Go + SQLite

Mapine serves location-based mobile clients. The service uses one Go HTTP
process, SQLite WAL persistence, an SQLite R-Tree, and provider adapters. The
original API contract is inventoried in
`.agents/handoffs/01-legacy-analysis.md`.

## Compatibility status

The legacy route shape and published action names are retained:

| Action | Purpose |
| --- | --- |
| `place.find` | Nearby POIs; Raah search on coverage miss, SQLite fallback |
| `place.search` | Persian forward geocoding through Nominatim |
| `place.reverse` | Persian reverse geocoding through Nominatim |
| `app.handshake` | App/page/city configuration and update metadata |
| `ad.one` | Select ad by type/city from `app/ad.json` |
| `http.info`, `http.actions` | Runtime information and action inventory |

All use `/v1/call/:action`. Query and JSON body params are merged (body wins).
The server adds `ok`, `meta.took`, `.id`, and `.took`; validation remains a
gateway-style 400. Handshake version-key naming, store links, city/page JSON,
ad fallback behavior, POI field conventions, and Persian normalization follow
the inspected source.

**Release caveat:** production equivalence is not yet proven. No running legacy
instance, golden production captures, MongoDB sample, credentials, or verified
provider-use terms were available. Fixtures in `testdata/compatibility` are
source-derived and explicitly provisional. Coordinate behavior and generated
IDs may differ for records not migrated with preserved Mongo IDs. New cache IDs
are stable 24-character opaque strings, not Mongo ObjectIds. Run a
staging comparison against the legacy service before cutover.

## API Documentation

The Scalar API Reference is available at `http://localhost:3000/docs`; the
OpenAPI 3.1 document is served at `http://localhost:3000/openapi.json`. Scalar's
pinned `@scalar/api-reference` 1.82.1 standalone browser build is self-hosted
and embedded in the Go binary. The UI and spec use same-origin relative URLs;
there is no documentation service, Scalar account, CDN, font host, or remote
proxy dependency.

OpenAPI is a documentation contract, not a new router or validator. The actual
RPC-style routes remain `/v1/call/:action`, and all published actions are
currently unauthenticated. Each action's OpenAPI metadata is maintained in
`internal/docs/specification.go`; when changing a public handler, update the
same file's methods, parameter constraints/defaults, response schemas, and
examples, then extend the contract tests. Add only actions genuinely dispatched
by the public gateway. `http.actions` is documented as an inventory response,
not treated as proof that every inventory entry is a callable public route.

The docs page does not execute requests on load. Scalar's **Test Request**
control sends a real same-origin request only after the user clicks Execute.
Place searches and geocoding may contact configured providers; `app.handshake`
may queue the configured app-open notification. GET examples use query
parameters. POST examples put action parameters in the JSON body, without
documenting query parameters. The gateway still accepts query values for
backward compatibility; if the same value is supplied both ways, the body wins.

Run documentation tests and the pinned Redocly OpenAPI validator with:

```sh
make validate-openapi
```

This writes the generated document to `bin/openapi.json`, which is also useful
for reviewing schema changes. Scalar assets are in `internal/docs/assets/`;
the checked-in bundle version and checksum/license attribution are recorded in
`SCALAR-LICENSE.txt`.

## Run locally

Requires Go 1.27+.

```sh
cp .env.example .env
set -a; . ./.env; set +a
make test
make build
mkdir -p data
SQLITE_PATH=./data/mapine.db \
NOMINATIM_URL=https://nominatim.openstreetmap.org \
make run
```

Default listen address is `0.0.0.0:3000`. `GET /healthz` is liveness;
`GET /readyz` checks SQLite. Use a persistent, writable data directory.
`NOMINATIM_URL` is optional: without it, cached POIs still work, while
geocoding returns the legacy failure response.

## Configuration

See `.env.example`. Main settings:

* `HTTP_HOST`, `HTTP_PORT`, `SQLITE_PATH`
* `POI_PROVIDER` (`raah` or `disabled`), `POI_SEARCH_URL`, `POI_DETAILS_URL`, `PROVIDER_MAX_RETRIES`,
  `POI_MIN_INTERVAL`, `POI_CACHE_TTL`
* `NOMINATIM_URL`, `NOMINATIM_USER_AGENT`, `NOMINATIM_MIN_INTERVAL`,
  `GEOCODE_CACHE_TTL`
* `TELEGRAM_URL`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`
* `LOG_LEVEL`, `NODE_ID`, `DEBUG` (`true` enables Raah request capture),
  `DEBUG_DIR` (defaults to a `debug/` directory beside `SQLITE_PATH`)

For a no-results `place.find` investigation, set `DEBUG=true` and restart the
service, then repeat the request. The backend appends correlated request/response
records to two valid JSON array files:

* `poi-search.json`: `search.raah.ir` POI-token request and response
* `poi-details.json`: `poi.raah.ir` bulk-detail request(s) and response(s)

Both files include the same `request_id` returned in the API JSON body while
debug mode is enabled. Retries and detail batches are recorded as separate
entries. By default, with `SQLITE_PATH=/data/mapine.db`, the files are written
under `/data/debug/`; set `DEBUG_DIR` to choose another directory. Captures
include raw provider URLs, tokens, and response bodies—keep the files private
and turn `DEBUG` off after troubleshooting.

For Docker Compose, set `DEBUG=true` in the Compose `.env` file and recreate
the service. Inspect the captures with:

```sh
docker compose -f docker-compose.yaml exec mapine cat /data/debug/poi-search.json
docker compose -f docker-compose.yaml exec mapine cat /data/debug/poi-details.json
```

The read-only Raah diagnostic endpoints are enabled by setting `DEBUG=true`
and restarting the service. No token or authorization header is required.
Production provider URLs must remain the trusted HTTPS Raah hosts.
See [`docs/providers/raah-balad.md`](docs/providers/raah-balad.md) for source
analysis, coordinate caveats, endpoint contracts, and manual Scalar workflows.

The code-level `replacePosition` boolean in `cmd/api/main.go` defaults to
`true`, swapping only `place.find` response `geometry.lat`/`geometry.lon`.
Change it to `false` to disable the response swap. Request coordinates,
canonical internal coordinates, SQLite/R-Tree storage, and distance
calculations are unchanged.

Raah requests use connection reuse, timeouts, context cancellation, bounded
retries with jitter/`Retry-After`, token batching and request coalescing.
Nominatim is serialized at a configurable minimum interval (default one
request/second), and successful geocodes are cached in SQLite. These adapters
reflect the old source's Raah/Balad and Nominatim behavior. **Their current
terms permitting the requested caching/redistribution have not been verified**;
obtain authorization or configure permitted endpoints before production use.
Self-hosted Nominatim is optional and resource-intensive; no local geocoder is
bundled. A cached POI database does not provide offline forward/reverse
geocoding.

## SQLite and geography

Startup applies idempotent schema steps (recorded in `schema_migrations`),
checks R-Tree support, and configures WAL, foreign keys, and a busy timeout.
Places have a stable provider identity (`source + source_id + category`), legacy
and stable client IDs, and structured locality, rating, image, and telephone
fields. Triggers keep `place_rtree(id,min_lon,max_lon,min_lat,max_lat)` in sync.
Nearby queries
use an R-Tree bounding box and then Haversine radius filtering, including an
antimeridian split. The API does not load the full place corpus into memory.

Coverage is keyed by category, zoom, and Web-Mercator tile. Each tile maps to a
deterministic provider camera; only validated responses successfully persisted
to SQLite are marked fetched. Successful empty results are cached until TTL
expiry; failures do not mark coverage and stale local POIs are returned. In
flight requests for the same coverage key are coalesced. Since the legacy Raah
response does not document exact geographic coverage and terms are unverified,
validate this cache policy with the provider before high-volume use.

## MongoDB export migration

Export first; the old MongoDB is never modified by this command. The importer
accepts `mongoexport` JSON arrays or NDJSON, is repeatable, batches writes,
preserves the Mongo ID, and quarantines invalid/ambiguous documents:

```sh
go run ./cmd/migrate \
  --source ./places.ndjson \
  --destination ./data/mapine.db \
  --dry-run

go run ./cmd/migrate \
  --source ./places.ndjson \
  --destination ./data/mapine.db

go run ./cmd/migrate --destination ./data/mapine.db --verify
```

Default `--coordinate-order auto` trusts consistent named `lat/lon` values
when they match GeoJSON `[lon,lat]`; conflicts are quarantined. If documents
only have coordinate arrays, conventional GeoJSON order is assumed. Use
`--coordinate-order latlon` or `geojson` only after inspecting representative
documents. Review `places.ndjson.rejected.ndjson`; do not blindly import
quarantined coordinate records. Compare counts and sample locations with the
source before cutover.

## Backup and restore

Create a consistent online SQLite snapshot (includes committed WAL content):

```sh
go run ./cmd/backup \
  --database ./data/mapine.db \
  --destination ./backups/mapine-$(date +%F-%H%M%S).db
go run ./cmd/migrate --destination ./backups/mapine-YYYY-MM-DD-HHMMSS.db --verify
```

Restore by stopping the API, preserving the existing DB/WAL/SHM files, placing
the verified snapshot at `SQLITE_PATH`, ensuring ownership/permissions, then
starting and checking `/readyz`. Do not copy only the live main database file
while WAL writes are active. Keep backups off the same volume and test restores.

## Deployment

Use `make help` to list targets, `make build` for local binaries, `make run` for
the API, and `make docker-build` to build the container image. Docker Compose
starts the API with a persistent named volume:

```sh
make compose-up
# or: docker compose -f docker-compose.yaml up --build
```

Kubernetes/k3s starter manifest:

```sh
kubectl apply -f k8s.yaml
kubectl rollout status deployment/mapine
kubectl get service mapine
```

The manifest exposes a `LoadBalancer` service (the cluster must provide a load
balancer; otherwise use `kubectl port-forward service/mapine 3000:3000`). It uses
one replica, a `ReadWriteOnce` PVC, and a writable temporary volume;
do not run independent SQLite writers against a shared network filesystem or
scale the deployment horizontally. Supply Telegram credentials via Kubernetes
Secret references when enabled. GitHub Actions runs tests before publishing
multi-architecture images to GHCR on `main` and version tags.

## Tests and benchmarks

```sh
make fmt
make test
make race
make vet
make build

# Optional: synthetic 10k, 100k, 1m data; includes R-Tree and full scans.
go test ./cmd/api -run '^$' -bench BenchmarkSpatialQuery -benchtime=5x -benchmem
```

Executed on an Apple M2 / darwin arm64 with Go 1.27.3, synthetic in-memory
SQLite fixtures, five benchmark iterations:

| Dataset | R-Tree query | Full scan |
| ---: | ---: | ---: |
| 10,000 | 0.049 ms, 1.9 KB, 48 allocs | 4.22 ms, 320 KB, 30,011 allocs |
| 100,000 | 0.052 ms, 1.9 KB, 48 allocs | 42.9 ms, 3.2 MB, 300,011 allocs |
| 1,000,000 | 0.054 ms, 1.9 KB, 48 allocs | 428.7 ms, 32.0 MB, 3,000,011 allocs |

These measurements compare only the synthetic indexed spatial lookup with a
simple SQLite full scan with five iterations per benchmark; they are not
end-to-end API, disk, concurrency, memory footprint, or legacy Node-vs-Go
comparisons.

## Remaining verification

Automated tests cover provider adapters with mock HTTP servers, retry behavior,
coverage/request coalescing and limits, R-Tree insert/update/delete/reopen,
spatial math, geocode caching, provisional compatibility fixtures, Mongo-export
dry-run/repeat import/quarantine and schema migration, backup snapshots, and
race detection. Still unverified:
live provider contracts and terms, a real legacy response diff, actual Mongo
coordinate conventions, GitHub multi-architecture publication, Kubernetes
admission/runtime, provider rate limits, and production-scale on-disk load. The
local Docker image built and passed `/healthz` and `/readyz` smoke checks.
Detailed status is tracked under `.agents/handoffs/`.
