# Testing report

Latest successful checks: `gofmt -w cmd`, `go test ./...`,
`go test -race ./...`, `go vet ./...`,
`go build ./cmd/api ./cmd/migrate ./cmd/backup`, `make build`,
`git diff --check`, and `docker compose -f docker-compose.yaml config`.

Covered behavior includes Raah/Nominatim HTTP test servers, retry, API golden
fixtures, query/body precedence, malformed JSON/content-type behavior, gateway
HTTP-vs-body status behavior, handshake version aliases, CORS request headers,
coverage and geocode coalescing/TTL, R-Tree insert/update/delete/reopen, query
plan and old-schema migration, Haversine/zoom/antimeridian/poles/zero
coordinate, Mongo JSON array/NDJSON dry-run/repeat import/quarantine, and
WAL-aware backup snapshot.

The four Kubernetes YAML documents parse successfully. The local arm64 Docker
image built and passed `/healthz` and `/readyz` smoke checks. GitHub
multi-architecture publication and Kubernetes runtime were not exercised. Live
external provider tests and legacy differential fixtures are unavailable and
are not claimed as passing. Web search for provider terms failed with HTTP 402;
terms remain unverified.
