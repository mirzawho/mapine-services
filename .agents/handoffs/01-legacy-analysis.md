# Legacy analysis (source inspection)

Source examined: README, package manifest/lock presence, Moleculer config,
Docker/Compose/Kubernetes, every `services/*`, `shared/*`, `app/*.json`, and
GitHub workflow. No test directory or production fixture was present.

## Published routes/actions

Gateway route is `server.all("/v1/call/:action")`, so all HTTP methods match;
the new implementation currently accepts GET/POST/PUT/PATCH/DELETE. Published
actions: `place.find`, `place.search`, `place.reverse`, `app.handshake`,
`ad.one`, `http.info`, `http.actions`. The dynamic action listing formats
visibility and schemas. `auth.authenticate` is marked Moleculer `public`, but
not `published`, so the gateway rejects it; `api.v1.token.authenticate` is
referenced but no token service exists in this checkout. `greeter` has no
actions. Internal `$` actions return 406 `BAD_ACTION`.

## Parameters and responses

* `place.find`: required numeric-convertible `lat/lon`, `zoom` 1..21,
  category enum `public-bathroom|mosque`; optional `gps_lat/gps_lon`, default 0.
  Handler also reads undeclared `force`. Cached shape is status 200, meta
  `{offline,total}` and `places` with id/osm_id/geometry/distance/
  distance_unit/category/name/address, plus `address` (null or provider object).
* `place.reverse`: required numeric `lat/lon`; outputs status 200 and `data`
  with address/neighbourhood/city/county/state/country, or status 400/500.
* `place.search`: address string min 3, optional city default null. Provider
  success returns locations (coordinates lat/lng, address, Nominatim fields,
  boundingbox); upstream failure paths return 502/500.
* `app.handshake`: no body parameters. Returns status/meta/data. Data includes
  handshake config, page, cities, legacy version-dependent camelCase/snake_case
  deprecation/update keys, feedback/update/share URLs, and map share URL.
* `ad.one`: type enum any/city default any; city optional null. Empty ad file
  returns status 404/message NO_AD. Present-day `ad.json` is empty.
* `http.info`: status plus broker namespace/node. `http.actions`: status and
  action descriptions.

Gateway merges query then body (body wins). `Authorization` is parsed as
Bearer or Basic username:password; `x-api-key` or query `apiKey` and
`x-cache` or query `xCache` set metadata. It forwards `x-flavor`, `x-version`,
`x-app`, user agent, IP. CORS is wildcard and exposes `.id`, `.took`. JSON
response adds `ok` based on body status==200 and `meta.took`; headers are
request UUID and millisecond duration. Gateway validation errors are 400 with
VALIDATION_FAILED/BAD_PARAMS and field descriptors. Unknown action 404;
private action 404; auth failures 403. The `result.code` mapping handles 301,
500, 502.

## Data and integrations

Mongo place schema has provider-ish `osm_id`, category, tags, GeoJSON location,
lat/lon, score/address/locality/title/description and timestamps; indexes
category, 2dsphere, unique location coordinates. Actual save path assigns
Balad token to `osm_id`, transforms geometry arrays inconsistently, reverse
geocodes each POI, and suppresses per-item errors. POI search calls Raah
token-search then bulk preview, expects GeoJSON coordinates and fields. Nominatim
reverse/forward use Persian `fa`, Iran/city query defaults, field normalization.
Telegram Worker notification is fired for handshake, failures suppressed.

Static configuration: app/ad.json, cities.json, handshake.json, page.json.
Startup connects to Mongo and exits on failure. Legacy deployment includes
Mongo, NATS and optional heavy Nominatim/Postgres; Docker image is Node 22.
GitHub workflow publishes GHCR image. The checked-in Compose has hardcoded
Mongo/Postgres credentials and inconsistent provider network attachment.

## Risks and unknowns

No recorded responses, user file, MongoDB documents, API keys, running legacy
instance, provider terms evidence, or production samples were available.
Coordinate semantics and legacy IDs cannot safely be migrated without sample
documents. The handshake version converter is unusual positional decimal
weighting; kept conceptually. `http.actions` runtime listing and response types
may differ in a single-process replacement. Raah usage/cache redistribution
terms remain unverified. `auth` action is not gateway exposed.
