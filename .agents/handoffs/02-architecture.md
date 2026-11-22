# Architecture decisions

The production execution path is a single Go `net/http` process, a pure-Go
SQLite driver (`modernc.org/sqlite`), and one persistent SQLite file. SQLite
uses WAL, foreign keys, a 5-second busy timeout, and one application DB
connection to serialize writers. No MongoDB, NATS, Redis, Express, or Moleculer
is required to run the Go API. Legacy source remains in the repository for
contract auditing and is excluded from the Docker context.

POI and geocoder adapters are explicit interfaces/clients. POI searches are
coalesced by category/zoom/tile; geocoding calls are cached persistently and
coalesced by normalized request key. Both integrations use bounded concurrent
slots, HTTP timeouts, cancellation, and provider-appropriate throttling.
Notification delivery is bounded, asynchronous, and non-fatal.

The implementation is deliberately modular by file within `cmd/api` rather
than creating framework layers that are not needed by this single binary.
Mongo import and consistent SQLite backup are separate commands. Nominatim is
external or self-hosted; there is no claim of offline forward/reverse geocoding.

Tradeoff: request-level coverage uses deterministic Web-Mercator tiles and the
tile center as the provider camera. The Raah API's coverage semantics/terms
could not be externally verified; tune/validate the cell/freshness policy with
authorized provider access before production traffic.
