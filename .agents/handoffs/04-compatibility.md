# Compatibility characterization

The source-derived endpoint/parameter/header/response inventory is in
`01-legacy-analysis.md`. Test fixtures under `testdata/compatibility` are
explicitly provisional, not production captures. The golden test compares exact
JSON structures/types for a deterministic POI success and validation failure,
excluding `meta.took`; headers are tested separately. Handshake naming, share
link behavior, query/body precedence, timing fields, and geocoding route shapes
also have focused tests.

Executed Go tests pass, but this does **not** establish parity with a deployed
Node/Moleculer instance. Remaining uncertainty: exact validator error wording,
status and omission details for unavailable provider payloads, production Mongo
coordinate conventions, and historical response records. A staging differential run against the legacy
server for all published actions and client versions is a release gate.
