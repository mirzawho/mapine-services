# Database design and migration

SQLite schema uses `schema_migrations`, `places`, `place_rtree`, `coverage`, and
`geocode_cache`. Places have a unique `(source, source_id, category)` identity,
preserved legacy ID, stable client ID, canonical latitude/longitude, locality, title/description/address,
score/rating, thumbnail/gallery/telephone, and source JSON for variable data.
Checks constrain geographic coordinates. Provider IDs are stored as `balad.ir`
tokens because the legacy code put them in `osm_id`; they are not asserted to
be actual OSM IDs.

R-Tree dimensions are longitude then latitude and use degenerate point bounds.
Insert/update-coordinate/delete triggers maintain synchronization. Nearby
queries force the R-Tree virtual table as the outer loop, query a bounding box
(split across the antimeridian when needed), then filter with Haversine. An
automated `EXPLAIN QUERY PLAN` test confirms virtual table constraints are
used. Startup does not rebuild the index on every run; migration version 2
performs the compatibility backfill once. Version 3 fills stable 24-character
client IDs for earlier Go rows. Generated IDs are opaque and are not actual
Mongo ObjectIds; imported Mongo `_id` values remain the public ID when present.

`coverage` persists successful and empty provider lookups by category/zoom/tile
with TTL; in-flight work is process-local and is recovered as a miss after a
restart. `geocode_cache` stores successful Nominatim results with TTL and
coalescing. A bounded daily janitor removes expired geocode entries and old
coverage markers.

`cmd/migrate` accepts Mongo export JSON arrays or NDJSON, batches transactions,
preserves legacy IDs and all known fields/raw document, is repeatable, supports
dry-run, and writes a reject/quarantine file. Automatic coordinate migration
requires scalar/GeoJSON agreement; ambiguous pairs are quarantined. The source
MongoDB is never changed. `--verify` checks SQLite integrity, coordinates,
place/R-Tree counts and coordinate agreement.

`cmd/backup` uses `VACUUM INTO` for a consistent live snapshot including WAL
commits. The importer cannot be validated against production samples that were
not supplied; inspect quarantine and representative imported locations before
cutover.
