# OpenStreetMap download and import

## Overview

`bin/osm` is an administrative Go CLI for downloading Geofabrik country
snapshots and importing matching **OSM Nodes** into the existing Mapine SQLite
database. Downloads and imports are separate operations. An import reads a
local PBF file and does not make network requests.

The importer stores source identity as `source=osm` and
`source_id=node/<OSM node ID>`. It reuses `places` and the existing
`place_rtree`; it does not create another places database or change the mobile
API schema. `public-restroom` is the CLI mapping name, while its stored Mapine
category remains `public-bathroom`.

## Installation and build

Build requirements are the Go version declared in `go.mod` and a writable
`bin/` directory. SQLite is the existing pure-Go `modernc.org/sqlite` driver.
The PBF decoder supports both cgo and pure-Go zlib builds; production Makefile
builds use `CGO_ENABLED=0`.

```sh
make build
```

This preserves the current API and administrative binaries and adds
`bin/osm`. To build only this CLI:

```sh
make build-osm
```

The default snapshot directory is `./data/osm`. Set `OSM_DATA_DIR` to change
it. The default SQLite path, `./bin/volume/data/mapine.db`, matches the API
binary's current data-volume location. Use `--db` to point to another existing
Mapine database. The CLI refuses to create a new database; initialize it with
the existing API or Mapine migration process first.

## Download

The country registry in `configs/osm/countries.yaml` currently configures Iran:

```sh
./bin/osm download iran
```

The configured Geofabrik URL is
`https://download.geofabrik.de/asia/iran-latest.osm.pbf`; by default the file
is written to `data/osm/iran-latest.osm.pbf`. `--output PATH` selects an exact
destination, and `--data-dir DIR` or `OSM_DATA_DIR` changes the snapshot
directory.

The downloader streams into a sibling `.part` file. It validates byte counts
when available, checks an authoritative Geofabrik `.md5` sidecar when served,
and checks that the file has a readable PBF header before atomically replacing
the final file. The prior snapshot remains in place if a transfer or validation
fails. A partial transfer is resumable only when it has an ETag or
Last-Modified validator; retries use `Range` and `If-Range` and validate the
returned `Content-Range`. An HTTP 200 response to a range request restarts from
zero rather than appending.

An existing snapshot is skipped only when a saved validator and remote
validator establish the same representation. If remote metadata cannot prove
that an existing file is current, the CLI explicitly says so and keeps the
file. Use `--force` to fetch a fresh copy. Live progress reports measured
downloaded bytes, known total and percentage, smoothed speed, elapsed time, and
ETA when the server provides a reliable total and enough samples have elapsed.
When output is redirected, progress is newline-based and contains no ANSI
control sequences. Connection, TLS, and response-header attempts have bounded
timeouts; the full transfer has a four-hour maximum by default. Use
`--timeout 6h` to allow a longer download.

Downloads are subject to Geofabrik's and OpenStreetMap's usage and attribution
requirements. The initial registry is intentionally Iran-only; it is not a
general world or bounding-box downloader.

## Categories and mappings

Category names and tag rules are loaded from
[`configs/osm/categories.yaml`](../configs/osm/categories.yaml); the list is not
duplicated in Go code.

```sh
./bin/osm categories
```

Current policy:

* `public-restroom` maps to database category `public-bathroom` and matches
  `amenity=toilets`. `access=private` and `access=no` are excluded. A missing
  name uses the configured Persian display fallback. `toilets=yes` is
  intentionally not treated as a standalone public restroom.
* `mosque` maps to database category `mosque` and matches either the AND rule
  `amenity=place_of_worship` plus `religion=muslim`, or
  `building=mosque`. `access=private` is excluded. These alternatives are an
  explicit but incomplete policy: they do not cover every mosque or
  prayer-related feature and may include a mosque building without an explicit
  religion tag.

Matching semantics are intentionally small and safe: `match.any` is OR between
rule groups; each group's `all` map is AND between exact key/value tags. The
optional `exclude.any` has the same group semantics and rejects a node if any
exclusion group matches. The YAML decoder rejects unknown fields and malformed
or empty rules. Add a category by editing the configuration file, not Go
source.

For a dry run:

```sh
./bin/osm import public-restroom --dry-run
```

The full import command is:

```sh
./bin/osm import public-restroom
```

Useful options include:

```sh
./bin/osm import public-restroom --country iran
./bin/osm import public-restroom --file ./data/osm/iran-latest.osm.pbf
./bin/osm import public-restroom --db ./bin/volume/data/mapine.db
./bin/osm import public-restroom --batch-size 1000 --workers 4
```

`--quiet` suppresses live progress; `--verbose` prints mapping and decoder
settings. Unknown CLI categories and source countries fail before parsing.

## Node-only coverage limits

Only OSM Node entities whose own tags match the configured rule are imported.
Ways and Relations are skipped by the PBF decoder. Nodes referenced by a Way
are not promoted to POIs, and tagged Ways or Relations are not converted into
points. The importer does not reconstruct polygons or calculate centroids.

This is intentional but means imported records do **not** represent every
real-world restroom or mosque in OSM. Many features are mapped as Ways or
Relations and will be absent. Do not interpret import counts as complete
geographic coverage.

Names prefer `name:fa`, then `name`, then the category fallback. Addresses use
the available `addr:street`, `addr:housenumber`, `addr:city`, and
`addr:postcode` tags; no reverse geocoding occurs. Coordinates remain canonical
latitude/longitude values. The existing R-Tree stores X as longitude and Y as
latitude.

## Progress and count policy

The default import performs two streaming passes:

1. **Counting** measures total Node objects, raw matching Nodes, valid matching
   Nodes, rejected coordinate/identity matches, bytes read, and a SHA-256 of
   the input file.
2. **Importing** rereads the PBF, stages valid matching Nodes in bounded
   SQLite batches, and verifies the exact file digest and counts against pass
   one before synchronizing production rows.

This makes the percentage and matching totals accurate, at the cost of reading
the PBF twice. The counting phase can show byte-based progress from the
decoder's actual consumed-byte count. The importing phase reports nodes
scanned out of the measured total and matches found out of the measured
category total. Rates use a smoothed measured throughput and ETA remains
unknown during warm-up or when a reliable total/rate is unavailable.

`Matching Nodes` counts every raw tag match, including invalid matching
records. `Rejected` counts matching Nodes with invalid identity or
non-finite/out-of-range coordinates. `Valid Matches` counts valid matches.
`Inserted`, `Updated`, and `Skipped` reconcile valid matches: `Skipped` includes
unchanged OSM rows and duplicate valid Node IDs within the input snapshot.
Unchanged rows are not rewritten. Counts describe OSM records, not all PBF
entities.

Dry-run reads and validates the source once, but performs no production or
history writes:

```sh
./bin/osm import public-restroom --dry-run
```

## SQLite and R-Tree integration

The importer validates the existing `places` table, uniqueness and required
R-Tree/triggers before processing. It records import history in
`osm_import_history` and uses `osm_import_staging` for bounded, per-batch writes.
Only after the full second pass and source checksum agree does it synchronize
staged rows inside one transaction. Existing `(source, source_id, category)`
identities are upserted in place, preserving their numeric place IDs; imported
attributes are limited to name, address, coordinates, country, and OSM tag JSON.
Other sources, categories, and application-managed fields are untouched.

The existing `places_ai` and `places_au` triggers update the existing
`place_rtree` transactionally. Before commit, the importer verifies R-Tree
entries for every staged identity. A parse, staging, source-change, database,
or verification failure rolls back the production synchronization and records
a failed/cancelled run where SQLite remains available. Staging batches are
short transactions; the final synchronization is atomic but can still contend
with another SQLite writer. WAL does not provide multiple concurrent writers.
For large imports, schedule a lower-traffic window and monitor SQLite lock
errors.

Repeated imports are safe and do not create duplicate OSM rows. **Stale records
missing from a newer snapshot are not deleted.** A future explicit snapshot
pruning mode would need a separate opt-in and complete-snapshot validation.

## Offline runtime use

Imported rows use the existing Mapine categories, so SQLite nearby lookup and
the existing R-Tree can find them through the unchanged
`/v1/call/place.find` contract. No OSM import-time network access or runtime API
schema change is introduced. This CLI does not change the existing API's
provider refresh/`force` behavior; a request that triggers the configured
legacy provider may still contact it. To rely exclusively on local results,
use the API's existing local/coverage behavior and separately configure or
disable legacy provider refresh according to the deployed API's supported
configuration. The OSM importer itself never calls Raah, Balad, Nominatim, or
another POI endpoint.

The public response does not gain an OSM-specific field as part of this work.
The stable internal OSM identity remains available in SQLite as
`source=osm, source_id=node/<id>`.

## Troubleshooting

* **Unknown country/category:** check `configs/osm/countries.yaml` or
  `configs/osm/categories.yaml`, then use `./bin/osm categories`.
* **Snapshot missing:** run `./bin/osm download iran`, or provide the local
  file with `--file`.
* **Existing snapshot currentness unknown:** validators were unavailable or no
  metadata was stored. The existing file was preserved; use `--force` to
  fetch a new copy.
* **Interrupted download:** rerun the download command. A partial resumes only
  when its stored ETag/Last-Modified validator and the HTTP range response
  agree. Otherwise it safely restarts.
* **Checksum/PBF validation error:** keep the previous final snapshot, remove
  only the invalid `.part` file if desired, and retry. Check available disk
  space and Geofabrik availability.
* **PBF scan/corruption error:** verify the file is a complete `.osm.pbf`
  extract and not an HTML error page or an OSM XML file.
* **SQLite schema mismatch:** point `--db` at the existing Mapine database and
  ensure the API's schema initialization/migrations have been run. The importer
  deliberately does not create a competing `places` schema.
* **SQLite locked/busy:** avoid simultaneous imports and schedule large final
  commits away from other writers. Check database and volume permissions.
* **No matches:** verify exact tag values and category exclusion rules. Use
  `--dry-run` to check counts without touching production rows.

Ctrl+C/SIGTERM cancels PBF scanning and batch writes. A partial staged run is
not synchronized into `places`; the previously complete OSM records remain.

## OSM attribution and license

OpenStreetMap data is © OpenStreetMap contributors and is available under the
Open Database License (ODbL). Provide attribution such as **“© OpenStreetMap
contributors”** wherever the imported map data is presented, and link to
`https://www.openstreetmap.org/copyright` where appropriate. Geofabrik is the
extract provider; follow its download and attribution guidance at
`https://download.geofabrik.de/`. Review the ODbL share-alike and database
redistribution obligations for the actual Mapine distribution and use.

Downloading a snapshot does not transfer or remove the OSM license obligations.
This guide is operational documentation, not legal advice.
