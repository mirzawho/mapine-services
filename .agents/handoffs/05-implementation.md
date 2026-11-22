# Implementation status

Implemented:

* Go HTTP API, dynamic action route for all legacy published actions, request
  query/body precedence, response `ok`/`meta.took`/`.id`/`.took`, CORS,
  validation, root and `/public/*`, health/readiness and graceful shutdown.
* SQLite WAL schema steps, R-Tree support smoke test, triggers, category index,
  R-Tree-first nearby queries, antimeridian handling, Haversine, cache janitor.
* Raah token/detail adapter with bounded token batches, validation, dedupe,
  retry/backoff/Retry-After, rate limit and pooled HTTP; coverage TTL and
  same-cell request coalescing with stale local fallback.
* Nominatim forward/reverse with Persian query/normalization, bounded request
  concurrency, minimum interval, persistent TTL cache and coalescing.
* Handshake and static JSON; ad selection/fallback; action inventory; bounded
  Telegram notification; private auth action remains unexposed as in the
  gateway contract.
* Repeatable Mongo export importer, coordinate quarantine, database verifier,
  consistent backup command, Go CI, Docker/Compose and one-writer Kubernetes.
* Spatial/provider/cache/API/migration/backup tests and synthetic benchmarks.

Unverified: real historical API diff, live provider response/terms, production
Mongo coordinate data, GitHub multi-architecture publication, Kubernetes
admission/PVC behavior, and side-by-side legacy resource use. The local
Apple-silicon Docker image built and passed health/readiness smoke checks.
Legacy Node runtime files and npm manifests have been removed; historical
source-derived contract notes remain under `.agents/handoffs/`.
