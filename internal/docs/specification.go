package docs

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	raahTokensPath  = "/_debug/providers/raah/tokens"
	raahDetailsPath = "/_debug/providers/raah/details"
	raahSearchPath  = "/_debug/providers/raah/search"
)

type parameterDoc struct {
	name        string
	description string
	required    bool
	query       map[string]any
	body        map[string]any
	example     any
}

type actionDoc struct {
	name             string
	tag              string
	summary          string
	description      string
	parameters       []parameterDoc
	headers          []parameterDoc
	bodyExample      map[string]any
	responseSchema   string
	responseExamples map[string]any
	validatesParams  bool
}

var documentedActions = []actionDoc{
	{
		name:    "place.find",
		tag:     "Places",
		summary: "Find nearby places",
		description: "Finds nearby public bathrooms or mosques. The request location selects a zoom-dependent search radius. " +
			"When `force` is omitted, the service refreshes from the configured POI provider by default; `force=true` or `force=1` also refreshes, while explicitly passing `force=false` or `force=0` allows fresh tile coverage to be used. If the provider fails, stored local places are returned. " +
			"`meta.offline` is false after a successful provider refresh and true when results are served from the local SQLite cache. " +
			"If both optional GPS coordinates are non-zero, `distance` is measured from them and rounded to two decimal places; otherwise it is 0. " +
			"The distance unit is `km`. A separate reverse-geocoding lookup supplies `address`; it is null when unavailable. " +
			"Use `category=public-bathroom` for bathrooms or `category=mosque` for mosques. " +
			"Zoom accepts numeric values from 1 through 21; fractional values are converted to an integer for tile and radius calculations.",
		parameters: []parameterDoc{
			{name: "lat", description: "Latitude of the search center, in decimal degrees.", required: true, query: numberQuery(-90, 90), body: numberBody(-90, 90), example: 35.6892},
			{name: "lon", description: "Longitude of the search center, in decimal degrees.", required: true, query: numberQuery(-180, 180), body: numberBody(-180, 180), example: 51.3890},
			{name: "zoom", description: "Map zoom from 1 to 21. Fractional values are truncated for tile and radius calculations.", required: true, query: numberQuery(1, 21), body: numberBody(1, 21), example: 15},
			{name: "category", description: "Place category to search.", required: true, query: enumQuery("public-bathroom", "mosque"), body: enumBody("public-bathroom", "mosque"), example: "public-bathroom"},
			{name: "gps_lat", description: "Optional GPS latitude used as the distance origin. Defaults to 0; distance is calculated only when both GPS coordinates are non-zero.", query: numberQueryDefault(-90, 90, 0), body: numberBodyDefault(-90, 90, 0), example: 35.6892},
			{name: "gps_lon", description: "Optional GPS longitude used as the distance origin. Defaults to 0; distance is calculated only when both GPS coordinates are non-zero.", query: numberQueryDefault(-180, 180, 0), body: numberBodyDefault(-180, 180, 0), example: 51.3890},
			{name: "force", description: "Optional refresh switch. Defaults to true when omitted. Use `false` or `0` to allow fresh tile coverage to be used; `true` or `1` forces a provider refresh.", query: enumQuery("true", "1", "false", "0"), body: map[string]any{"oneOf": []any{map[string]any{"type": "boolean"}, enumBody("true", "1", "false", "0")}}, example: "true"},
		},
		bodyExample:    map[string]any{"lat": 35.6892, "lon": 51.3890, "zoom": 15, "category": "public-bathroom", "gps_lat": 35.6892, "gps_lon": 51.3890, "force": true},
		responseSchema: "PlaceFindResponse",
		responseExamples: map[string]any{
			"nearbyBathrooms": example("Nearby bathrooms", map[string]any{
				"status": 200, "ok": true,
				"meta": map[string]any{"offline": false, "total": 1, "took": 12},
				"places": []any{map[string]any{
					"osm_id": "bathroom-1", "geometry": map[string]any{"lat": 35.6895, "lon": 51.3892},
					"distance": 0.25, "distance_unit": "km", "category": "public-bathroom",
					"city": nil, "province": nil, "gallery": []any{}, "telephone": nil,
					"rating": map[string]any{"score": 0, "count": 0},
				}},
				"address": nil,
			}),
			"nearbyMosques": example("Nearby mosques", map[string]any{
				"status": 200, "ok": true,
				"meta": map[string]any{"offline": false, "total": 1, "took": 12},
				"places": []any{map[string]any{
					"osm_id": "poi", "geometry": map[string]any{"lat": 35, "lon": 51},
					"distance": 0, "distance_unit": "km", "category": "mosque", "name": "mosque",
					"city": nil, "province": nil, "gallery": []any{}, "telephone": nil,
					"rating": map[string]any{"score": 4, "count": 2},
				}},
				"address": nil,
			}),
			"emptyOfflineCache": example("Empty local result", map[string]any{
				"status": 200, "ok": true, "meta": map[string]any{"offline": true, "total": 0, "took": 3},
				"places": []any{}, "address": nil,
			}),
		},
		validatesParams: true,
	},
	{
		name:        "place.search",
		tag:         "Places",
		summary:     "Search for an address",
		description: "Forward-geocodes an address using the configured Nominatim-compatible provider. The provider query is assembled as `ایران, {city}, {address}` with empty parts omitted and Persian (`fa`) results requested. `city` is optional. A successful provider response with no locations and a provider failure both return the legacy action-level `status: 502` body with HTTP 200. The `address` minimum is measured in JavaScript UTF-16 code units.",
		parameters: []parameterDoc{
			{name: "address", description: "Address text to geocode; at least 3 UTF-16 code units.", required: true, query: stringQuery(3), body: stringBody(3), example: "خیابان ولیعصر"},
			{name: "city", description: "Optional city name used to narrow the Iran geocoding query. Defaults to null when omitted.", query: stringQuery(0), body: nullableStringBody(0), example: "تهران"},
		},
		bodyExample:    map[string]any{"address": "خیابان ولیعصر", "city": "تهران"},
		responseSchema: "PlaceSearchResponse",
		responseExamples: map[string]any{
			"tehranAddress": example("Persian address search", map[string]any{
				"status": 200, "ok": true,
				"meta":      map[string]any{"total": 1, "query": "ایران, تهران, خیابان ولیعصر", "took": 18},
				"locations": []any{sampleAddress("خیابان ولیعصر، تهران", "خیابان ولیعصر", "تهران")},
			}),
			"providerFailure": example("Provider unavailable or no matches", map[string]any{"status": 502, "ok": false}),
		},
		validatesParams: true,
	},
	{
		name:        "place.reverse",
		tag:         "Places",
		summary:     "Reverse-geocode coordinates",
		description: "Reverse-geocodes decimal-degree coordinates with the configured Nominatim-compatible provider, requesting Persian (`fa`) address data at zoom 17. A successful lookup returns the legacy selected address fields under `data`. Provider failure or an unavailable provider returns an action-level `status: 400` body while the HTTP response remains 200.",
		parameters: []parameterDoc{
			{name: "lat", description: "Latitude in decimal degrees, from -90 to 90.", required: true, query: numberQuery(-90, 90), body: numberBody(-90, 90), example: 35.6892},
			{name: "lon", description: "Longitude in decimal degrees, from -180 to 180.", required: true, query: numberQuery(-180, 180), body: numberBody(-180, 180), example: 51.3890},
		},
		bodyExample:    map[string]any{"lat": 35.6892, "lon": 51.3890},
		responseSchema: "PlaceReverseResponse",
		responseExamples: map[string]any{
			"reverseTehran": example("Reverse geocode in Tehran", map[string]any{
				"status": 200, "ok": true,
				"data": map[string]any{
					"address": "تهران، خیابان ولیعصر", "neighbourhood": "ولیعصر",
					"city": "تهران", "county": "تهران", "state": "تهران", "country": "ایران",
				},
			}),
			"geocodingFailure": example("Geocoder unavailable", map[string]any{"status": 400, "ok": false}),
		},
		validatesParams: true,
	},
	{
		name:        "app.handshake",
		tag:         "Application",
		summary:     "Get application configuration",
		description: "Returns version/update metadata, page configuration, and the configured city list. The optional `x-app`, `x-version`, and `x-flavor` headers preserve the mobile client's handshake behavior. `x-version` controls legacy flag naming (`isDeprecated` / `isUpdateAvailable` for values below the legacy parser threshold, snake_case otherwise); version comparison uses the existing positional-decimal converter. `x-flavor` values `myket` and `cafebazaar` produce store-specific feedback/update links, and the configured app identifier may enable `url_share`. When Telegram credentials are configured, each handshake also queues the existing asynchronous app-open notification.",
		headers: []parameterDoc{
			{name: "x-app", description: "Optional application identifier. The configured Mapine app identifier also enables `url_share`.", query: stringQuery(0), example: "ir.khodmirza.towaleti"},
			{name: "x-version", description: "Optional client version. Controls legacy update flags and response-key casing.", query: stringQuery(0), example: "2.2.2"},
			{name: "x-flavor", description: "Optional distribution flavor. `myket` and `cafebazaar` receive store-specific links.", query: stringQuery(0), example: "cafebazaar"},
		},
		bodyExample:    map[string]any{},
		responseSchema: "AppHandshakeResponse",
		responseExamples: map[string]any{
			"currentClient": example("Current Android client handshake", map[string]any{
				"status": 200, "ok": true, "meta": map[string]any{"took": 2},
				"data": map[string]any{
					"version_latest": "2.2.2", "version_deprecated": "2.0.0",
					"url_map":        "https://map.doting.ir/osm_bright/{z}/{x}/{y}.png",
					"url_form_place": "https://survey.porsline.ir/s/kZcggify",
					"url_form_ad":    "https://survey.porsline.ir/s/bFw0ufy7",
					"is_deprecated":  false, "is_update_available": false,
					"url_feedback":    "bazaar://details?id=ir.khodmirza.towaleti&version=2.2.2",
					"url_update":      "bazaar://details?id=ir.khodmirza.towaleti",
					"url_share":       "https://khodmirza.ir/towaleti",
					"url_place_share": "https://www.google.com/maps/search/?api=1&query=LAT,LON",
					"page": map[string]any{
						"terms":   map[string]any{"title": "شرایط استفاده", "content": "برای استفاده از این برنامه، شما موافقت می‌کنید که:\n• در صورت ارسال بیش از ۲۰ درخواست در یک دقیقه، دسترسی شما به صورت خودکار مسدود خواهد شد\n• اطلاعات موقعیت مکانی شما فقط برای یافتن نزدیک‌ترین مکان استفاده می‌شود و هیچ ذخیره سازی ای انجام نمی شود\n• از ارسال اطلاعات نادرست یا گمراه‌کننده خودداری کنید\n• حق مسدود کردن کاربران متخلف برای ما محفوظ است"},
						"privacy": map[string]any{"title": "حریم خصوصی", "content": "ما به حریم خصوصی شما احترام می‌گذاریم.\n• موقعیت مکانی شما برای نمایش نزدیک‌ترین مکان استفاده می‌شود\n• موقعیت مکانی شما برای پیدا کردن تبلیغ مخصوص شهر استفاده می‌شود\n• از جایی که اپلیکیشن را نصب کرده اید و نسخه ای که دارید استفاده می‌کنید، برای بهبود خدمات استفاده می‌شود\n• اطلاعات موقعیت مکانی شما در سرورهای ما ذخیره نمی‌شود\n• ما هیچ اطلاعات شخصی را با اشخاص ثالث به اشتراک نمی‌گذاریم\n• تمام ارتباطات به صورت رمزنگاری شده انجام می‌شود"},
						"about":   map[string]any{"title": "درباره ما", "content": "هدف ما ارائه راه‌حلی آسان برای یافتن مکان در نزدیکی شماست.\n• ما متعهد به بهبود مداوم خدمات خود هستیم\n• تیم ما با دقت تمام برای ارائه اطلاعات دقیق و به‌روز تلاش می‌کند\n• از پیشنهادات و انتقادات شما برای بهبود خدمات استقبال می‌کنیم\n• هدف ما ساخت اپلیکیشنی کاربردی و مفید است"},
					},
					"cities": []any{map[string]any{"name": "تهران", "lat": 35.6892, "lon": 51.389, "zoom": 10}},
				},
			}),
		},
	},
	{
		name:        "ad.one",
		tag:         "Advertisements",
		summary:     "Get an advertisement",
		description: "Selects an advertisement from the configured `app/ad.json` list. `type` defaults to `any`; `any` selects an ad whose `cities` array is empty. `type=city` requires a non-empty `city`, prefers matching ads, and falls back to a generic ad with an empty `cities` array. The ad object is returned unchanged; the only field used for selection is `cities`. The checked-in ad list is empty, so requests currently short-circuit with `NO_AD`. If ads are configured, incompatible type/city combinations return action-level `INVALID_TYPE` with HTTP 200.",
		parameters: []parameterDoc{
			{name: "type", description: "Selection mode. Defaults to `any` when omitted.", query: enumQueryDefault("any", "any", "city"), body: enumBodyDefault("any", "any", "city"), example: "city"},
			{name: "city", description: "Optional city name (null when omitted). Required in practice for `type=city`; `type=any` with a city is an incompatible combination.", query: stringQuery(0), body: nullableStringBody(0), example: "تهران"},
		},
		bodyExample:    map[string]any{"type": "city", "city": "تهران"},
		responseSchema: "AdvertisementResponse",
		responseExamples: map[string]any{
			"emptyAds": example("Current configuration has no ads", map[string]any{"status": 404, "ok": false, "message": "NO_AD"}),
			"selectedAd": example("Minimal selected city ad; payload is passed through", map[string]any{
				"status": 200, "ok": true, "meta": map[string]any{"took": 1},
				"ad": map[string]any{"cities": []any{"تهران"}},
			}),
			"notFound": example("No matching or generic ad", map[string]any{"status": 404, "ok": false, "message": "AD_NOT_FOUND", "type": "city"}),
		},
		validatesParams: true,
	},
	{
		name:           "http.info",
		tag:            "System",
		summary:        "Get service information",
		description:    "Returns the fixed service namespace and this process's configured node identifier. No authentication is required. Extra query or JSON body values are ignored by this action.",
		bodyExample:    map[string]any{},
		responseSchema: "SystemInfoResponse",
		responseExamples: map[string]any{
			"serviceInfo": example("Service information", map[string]any{"status": 200, "ok": true, "info": map[string]any{"namespace": "mapine", "node": "mapine"}}),
		},
	},
	{
		name:           "http.actions",
		tag:            "System",
		summary:        "List action metadata",
		description:    "Returns the Go service's configured action inventory and parameter metadata. Inventory entries are descriptive and do not by themselves guarantee a matching public `/v1/call/` route; this reference documents only actions dispatched by the public gateway. No authentication is required. Extra query or JSON body values are ignored by this action.",
		bodyExample:    map[string]any{},
		responseSchema: "SystemActionsResponse",
	},
}

var documentedHTTPMethods = []string{"get", "post"}

// Specification returns the OpenAPI description for the existing public action
// gateway. It is deliberately descriptive: routing and validation remain in the
// action handlers.
func Specification() map[string]any {
	paths := make(map[string]any, len(documentedActions))
	for _, action := range documentedActions {
		pathItem := make(map[string]any, len(documentedHTTPMethods))
		for _, method := range documentedHTTPMethods {
			pathItem[method] = actionOperation(action, method)
		}
		paths["/v1/call/"+action.name] = pathItem
	}
	paths[raahTokensPath] = raahTokensOperation()
	paths[raahDetailsPath] = raahDetailsOperation()
	paths[raahSearchPath] = raahSearchOperation()

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "Mapine API",
			"version":     "1.0.0",
			"description": "Mapine location services API. The backend uses action-based HTTP endpoints and preserves the existing mobile request/response contract. Published actions are unauthenticated. Query parameters and JSON body properties are merged, with JSON body properties taking precedence. Scalar's Test Request sends a real request to this service only after the user explicitly clicks Execute; place lookups and geocoding may call configured external providers, and handshakes may queue the configured app-open notification.",
		},
		"servers": []any{map[string]any{"url": ".", "description": "Same origin as this documentation (supports a reverse-proxy path prefix)."}},
		"tags": []any{
			map[string]any{"name": "Places", "description": "Nearby place discovery and Persian address geocoding."},
			map[string]any{"name": "Application", "description": "Mobile application handshake and configuration."},
			map[string]any{"name": "Advertisements", "description": "City-aware advertisement selection."},
			map[string]any{"name": "System", "description": "Public service information and action inventory."},
			map[string]any{"name": "Raah / Balad Provider", "description": "Debug-mode diagnostic tools for the configured Raah/Balad POI provider. Disabled by default."},
		},
		"paths": paths,
		"components": map[string]any{
			"schemas": schemaComponents(),
		},
		"x-mapine-gateway": map[string]any{
			"route":          "/v1/call/:action",
			"methods":        []string{"GET", "POST"},
			"parameterMerge": "Query string values are read first. For application/json or +json request bodies, object properties (or array indices) overwrite query values.",
			"notes": []string{
				"GET action parameters are documented as query parameters. POST action parameters are documented in the JSON request body; POST query parameters are omitted from the reference, though the gateway continues to accept them for backward compatibility.",
				"JSON request bodies are limited to 2 MiB.",
				"Action-level status values remain in the JSON body while HTTP status is 200. Gateway validation and malformed JSON use HTTP 400.",
				"No authentication is required or enforced by the currently dispatched public actions.",
				"When DEBUG=true, JSON responses from /v1/call include request_id matching the .id response header. place.find also captures Raah search and details HTTP requests/responses in DEBUG_DIR.",
				"Raah diagnostic routes are not upstream Raah contracts. They return 404 unless DEBUG=true; no authentication is required. Only the fixed HTTPS Raah search and POI hosts are allowed.",
				"Coordinate conventions: Mapine accepts `lat`/`lon` as latitude/longitude; the Raah search adapter sends `camera` as longitude,latitude; Raah details use GeoJSON `[longitude,latitude]`, normalized once to `geometry.lat`/`geometry.lon`; SQLite stores latitude/longitude and its R-Tree indexes X=longitude, Y=latitude.",
			},
		},
	}
}

func raahTokensOperation() map[string]any {
	return map[string]any{
		"get": map[string]any{
			"tags":        []string{"Raah / Balad Provider"},
			"summary":     "Discover Raah POI tokens",
			"operationId": "raahDiscoverTokens",
			"description": "Local Mapine diagnostic endpoint (not an upstream Raah URL). Mapine accepts standard `lat` and `lon` parameters; at the provider boundary the Raah adapter constructs `camera={lon},{lat}`. For example, lat=35.6892 and lon=51.389 are sent as camera=51.389,35.6892. The adapter also adds `format=json`, `slug={category}`, and `zoom={zoom}`. The raw provider JSON and its `poi-tokens` array are returned. The endpoint is disabled unless `DEBUG=true`, requires no authentication, and only starts when provider URLs use the trusted HTTPS Raah hosts. Uses the provider timeout, response cap, and `POI_MIN_INTERVAL` rate spacing.",
			"security":    []any{},
			"parameters": []any{
				diagnosticQueryParameter("lat", "number", true, -90, 90, 35.6892),
				diagnosticQueryParameter("lon", "number", true, -180, 180, 51.389),
				diagnosticQueryParameter("zoom", "integer", true, 1, 21, 15),
				map[string]any{"name": "category", "in": "query", "required": true, "description": "Application POI slug.", "schema": map[string]any{"type": "string", "enum": []string{"public-bathroom", "mosque"}}, "example": "public-bathroom"},
			},
			"responses": map[string]any{
				"200": diagnosticResponse("Token discovery response", refSchema("RaahTokensDiagnosticResponse"), "sourceDerivedTokens"),
				"400": diagnosticErrorResponse("Invalid or missing coordinates, zoom, or category."),
				"404": diagnosticErrorResponse("Diagnostics are disabled in this environment."),
				"502": diagnosticErrorResponse("Raah HTTP failure, timeout, malformed JSON, missing `poi-tokens`, or more than 500 tokens. The wrapper includes the upstream status and response body when available."),
			},
		},
	}
}

func raahDetailsOperation() map[string]any {
	return map[string]any{
		"post": map[string]any{
			"tags":        []string{"Raah / Balad Provider"},
			"summary":     "Fetch Raah POI details",
			"operationId": "raahFetchDetails",
			"description": "Local Mapine diagnostic endpoint (not an upstream Raah contract). Accepts up to 50 provider tokens and calls the existing Go adapter's `GET https://poi.raah.ir/web/v4/preview-bulk/{comma-separated-tokens}`. The upstream response is returned raw for inspection. Tokens must come from a trusted source; commas and path separators are rejected. This endpoint does not persist POIs. Disabled unless `DEBUG=true`; no authentication is required. Configured provider timeout, 8 MiB upstream response cap, and shared `POI_MIN_INTERVAL` apply.",
			"security":    []any{},
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{"application/json": map[string]any{
					"schema":   refSchema("RaahDetailsDiagnosticRequest"),
					"examples": map[string]any{"tokenPlaceholders": example("Synthetic placeholders; replace with real tokens returned by discovery", map[string]any{"tokens": []string{"TOKEN_1", "TOKEN_2"}})},
				}},
			},
			"responses": map[string]any{
				"200": diagnosticResponse("Raw POI details response", refSchema("RaahDetailsDiagnosticResponse"), "sourceDerivedDetails"),
				"400": diagnosticErrorResponse("Malformed JSON, invalid token, empty list, or more than 50 tokens."),
				"404": diagnosticErrorResponse("Diagnostics are disabled in this environment."),
				"413": diagnosticErrorResponse("Diagnostic request body exceeds 16 KiB."),
				"502": diagnosticErrorResponse("Raah HTTP failure, timeout, malformed JSON, or missing `items`; raw response is included when available."),
			},
		},
	}
}

func raahSearchOperation() map[string]any {
	return map[string]any{
		"get": map[string]any{
			"tags":        []string{"Raah / Balad Provider"},
			"summary":     "Run the complete Raah POI workflow",
			"operationId": "raahCompleteSearch",
			"description": "Local read-only diagnostic that runs token discovery, then the provider's 50-token-batched details requests, and returns each raw upstream response beside the current Go `place.find` normalization preview. It does not call geocoding, write coverage, or persist data. A successful empty token array returns HTTP 200 with no details requests. Provider failures return HTTP 502 with partial stage data where available. Disabled unless `DEBUG=true`; no authentication is required. Uses the provider timeout, response cap, retry policy, and `POI_MIN_INTERVAL`.",
			"security":    []any{},
			"parameters": []any{
				diagnosticQueryParameter("lat", "number", true, -90, 90, 35.6892),
				diagnosticQueryParameter("lon", "number", true, -180, 180, 51.389),
				diagnosticQueryParameter("zoom", "integer", true, 1, 21, 15),
				map[string]any{"name": "category", "in": "query", "required": true, "description": "Application POI slug.", "schema": map[string]any{"type": "string", "enum": []string{"public-bathroom", "mosque"}}, "example": "public-bathroom"},
			},
			"responses": map[string]any{
				"200": diagnosticResponse("Token discovery, detail responses, and normalized preview", refSchema("RaahSearchDiagnosticResponse"), "sourceDerivedSearch"),
				"400": diagnosticErrorResponse("Invalid or missing coordinates, zoom, or category."),
				"404": diagnosticErrorResponse("Diagnostics are disabled in this environment."),
				"502": diagnosticErrorResponse("Raah HTTP failure, timeout, malformed JSON, missing expected fields, or invalid POI items. Partial raw data is returned when available."),
			},
		},
	}
}

func diagnosticQueryParameter(name, typ string, required bool, min, max, exampleValue any) map[string]any {
	return map[string]any{
		"name": name, "in": "query", "required": required,
		"schema":  map[string]any{"type": typ, "minimum": min, "maximum": max},
		"example": exampleValue,
	}
}

func diagnosticResponse(description string, schema any, exampleName string) map[string]any {
	examples := map[string]any{
		"sourceDerivedTokens":  example("Synthetic source-derived shape; not a live capture. Mapine lat=35.6892 and lon=51.389 become Raah camera=51.389,35.6892.", map[string]any{"ok": true, "upstream": map[string]any{"method": "GET", "url": "https://search.raah.ir/v4/bundle-search/?camera=51.389%2C35.6892&format=json&slug=public-bathroom&zoom=15", "status_code": 200}, "raw_response": map[string]any{"poi-tokens": []string{"fixture-token-1"}}, "poi_tokens": []string{"fixture-token-1"}}),
		"sourceDerivedDetails": example("Synthetic source-derived shape; token is a placeholder, not a live capture", map[string]any{"ok": true, "upstream": map[string]any{"method": "GET", "url": "https://poi.raah.ir/web/v4/preview-bulk/TOKEN_1", "status_code": 200}, "raw_response": map[string]any{"items": []any{map[string]any{"token": "TOKEN_1", "geometry": map[string]any{"coordinates": []float64{51.389, 35.6892}}, "name": "Fixture POI"}}}, "upstream_requests": []any{map[string]any{"url": "https://poi.raah.ir/web/v4/preview-bulk/TOKEN_1", "status_code": 200, "raw_response": map[string]any{"items": []any{map[string]any{"token": "TOKEN_1", "geometry": map[string]any{"coordinates": []float64{51.389, 35.6892}}, "name": "Fixture POI"}}}}}}),
		"sourceDerivedSearch":  example("Synthetic source-derived shapes; not a live capture. Mapine lat=35.6892 and lon=51.389 become Raah camera=51.389,35.6892.", map[string]any{"ok": true, "token_discovery": map[string]any{"method": "GET", "url": "https://search.raah.ir/v4/bundle-search/?camera=51.389%2C35.6892&format=json&slug=public-bathroom&zoom=15", "status_code": 200, "raw_response": map[string]any{"poi-tokens": []string{"fixture-token-1"}}, "poi_tokens": []string{"fixture-token-1"}}, "detail_requests": []any{}, "normalized_places": []any{}, "persistence": map[string]any{"performed": false}}),
	}
	return map[string]any{
		"description": description,
		"content": map[string]any{"application/json": map[string]any{
			"schema": schema, "examples": map[string]any{exampleName: examples[exampleName]},
		}},
	}
}

func diagnosticErrorResponse(description string) map[string]any {
	return map[string]any{
		"description": description,
		"content": map[string]any{"application/json": map[string]any{
			"schema":  refSchema("RaahDiagnosticError"),
			"example": map[string]any{"ok": false, "error": "DIAGNOSTIC_ERROR"},
		}},
	}
}

func actionOperation(action actionDoc, method string) map[string]any {
	parameters := make([]any, 0, len(action.parameters)+len(action.headers))
	if method == "get" {
		for _, p := range action.parameters {
			parameters = append(parameters, operationParameter(p, "query"))
		}
	}
	for _, p := range action.headers {
		parameters = append(parameters, operationParameter(p, "header"))
	}

	description := action.description + "\n\n"
	if method == "get" {
		description += "GET action parameters are supplied as query parameters. Repeated scalar query parameters become arrays and fail the corresponding scalar validation. Unknown parameters are ignored unless explicitly described."
	} else {
		description += "Supply action parameters in the JSON request body; query parameters are intentionally not listed for POST. The gateway still accepts query values for backward compatibility, and JSON body properties override query values with the same name."
		description += " This operation accepts a JSON object or array body with `application/json` or a `+json` content type."
	}

	responses := map[string]any{
		"200": responseForAction(action, false),
		"400": gatewayBadRequest(action.validatesParams, false),
		"413": requestTooLarge(false),
	}

	operation := map[string]any{
		"tags":        []string{action.tag},
		"summary":     action.summary,
		"operationId": operationID(action.name, method),
		"description": description,
		"parameters":  parameters,
		"responses":   responses,
		"security":    []any{},
	}
	if method == "post" {
		operation["requestBody"] = actionRequestBody(action)
	}
	return operation
}

func operationID(action, method string) string {
	parts := strings.Split(action, ".")
	var id strings.Builder
	for _, part := range parts {
		if id.Len() == 0 {
			id.WriteString(part)
			continue
		}
		id.WriteString(strings.ToUpper(part[:1]))
		id.WriteString(part[1:])
	}
	id.WriteString(strings.ToUpper(method[:1]))
	id.WriteString(method[1:])
	return id.String()
}

func operationParameter(p parameterDoc, in string) map[string]any {
	schema := p.query
	if in == "header" {
		schema = map[string]any{"type": "string"}
	}
	parameter := map[string]any{
		"name":        p.name,
		"in":          in,
		"required":    p.required,
		"description": p.description,
		"schema":      schema,
	}
	if p.example != nil {
		parameter["example"] = p.example
	}
	return parameter
}

func actionRequestBody(action actionDoc) map[string]any {
	properties := make(map[string]any, len(action.parameters))
	for _, p := range action.parameters {
		properties[p.name] = p.body
	}
	description := "Optional JSON object. POST parameters are documented in the body. At runtime, named properties are merged with query parameters, with body values taking precedence. The gateway also accepts a JSON array and maps its entries to numeric string keys; actions with named parameters will normally fail validation for that form. Maximum JSON body size is 2 MiB."
	if len(action.parameters) == 0 {
		description = "Optional JSON object or array. This action has no named request parameters, so supplied values are ignored. Maximum JSON body size is 2 MiB."
	}
	schema := map[string]any{
		"oneOf": []any{
			map[string]any{
				"type":                 "object",
				"description":          "Named action parameters; only documented fields are consumed by this action.",
				"properties":           properties,
				"additionalProperties": true,
				"example":              action.bodyExample,
			},
			map[string]any{
				"type":        "array",
				"description": "The gateway converts array indices to string keys; documented named action parameters are then absent unless also supplied in the query.",
				"items":       map[string]any{},
			},
		},
	}
	return map[string]any{
		"required":    false,
		"description": description,
		"content": map[string]any{
			"application/json": map[string]any{"schema": schema, "example": action.bodyExample},
		},
	}
}

func responseForAction(action actionDoc, head bool) map[string]any {
	response := map[string]any{
		"description": "The action result. Legacy action-level status values are in the JSON body; a completed action normally uses HTTP 200.",
		"headers":     metadataHeaders(),
	}
	if !head {
		response["content"] = map[string]any{
			"application/json": map[string]any{"schema": refSchema(action.responseSchema)},
		}
		if len(action.responseExamples) > 0 {
			content := response["content"].(map[string]any)["application/json"].(map[string]any)
			content["examples"] = action.responseExamples
		}
	} else {
		response["description"] = "The action is executed and metadata headers are returned; HTTP HEAD omits the response body."
	}
	return response
}

func gatewayBadRequest(validates, head bool) map[string]any {
	description := "Gateway-level HTTP 400 for invalid JSON, a non-object/non-array JSON value, or invalid action parameters."
	if !validates {
		description = "Gateway-level HTTP 400 for invalid JSON or a non-object/non-array JSON value. This action has no parameter validation."
	}
	response := map[string]any{"description": description, "headers": metadataHeaders()}
	if !head {
		bodySchema := any(refSchema("RequestError"))
		examples := map[string]any{
			"invalidJson": example("Malformed JSON body", map[string]any{"ok": false, "message": "INVALID_JSON"}),
		}
		if validates {
			bodySchema = map[string]any{"oneOf": []any{refSchema("ValidationError"), refSchema("RequestError")}}
			examples["validation"] = example("Invalid or missing action parameter", map[string]any{
				"ok": false, "message": "VALIDATION_FAILED", "error": "BAD_PARAMS",
				"params": []any{map[string]any{
					"message": "The 'address' field length must be at least 3 characters long.",
					"param":   "address", "type": "stringMin",
				}},
			})
		}
		response["content"] = map[string]any{"application/json": map[string]any{"schema": bodySchema, "examples": examples}}
	}
	return response
}

func requestTooLarge(head bool) map[string]any {
	response := map[string]any{
		"description": "The JSON request body exceeds the gateway's 2 MiB limit.",
		"headers":     metadataHeaders(),
	}
	if !head {
		response["content"] = map[string]any{
			"application/json": map[string]any{
				"schema":  refSchema("RequestError"),
				"example": map[string]any{"ok": false, "message": "REQUEST_TOO_LARGE"},
			},
		}
	}
	return response
}

func metadataHeaders() map[string]any {
	return map[string]any{
		".id": map[string]any{
			"description": "Request identifier generated by the HTTP middleware.",
			"schema":      map[string]any{"type": "string", "format": "uuid"},
		},
		".took": map[string]any{
			"description": "Elapsed request time in milliseconds.",
			"schema":      map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		},
	}
}

func schemaComponents() map[string]any {
	stringOrNull := map[string]any{"type": []string{"string", "null"}}
	cityConfig := map[string]any{
		"type":     "object",
		"required": []string{"name", "lat", "lon", "zoom"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"lat":  map[string]any{"type": "number", "minimum": -90, "maximum": 90},
			"lon":  map[string]any{"type": "number", "minimum": -180, "maximum": 180},
			"zoom": map[string]any{"type": "number"},
		},
		"additionalProperties": true,
	}
	pageSection := map[string]any{
		"type": "object", "required": []string{"title", "content"},
		"properties":           map[string]any{"title": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}},
		"additionalProperties": true,
	}
	return map[string]any{
		"ApiMeta": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"offline": map[string]any{"type": "boolean", "description": "For place.find, true means results came from the local SQLite cache."},
				"total":   map[string]any{"type": "integer", "minimum": 0},
				"took":    map[string]any{"type": "integer", "minimum": 0, "description": "Elapsed request time in milliseconds, added by middleware."},
				"query":   map[string]any{"type": "string", "description": "Normalized provider query for address search."},
			},
			"additionalProperties": true,
		},
		"Coordinate": objectSchema([]string{"lat", "lon"}, map[string]any{
			"lat": map[string]any{"type": "number", "minimum": -90, "maximum": 90},
			"lon": map[string]any{"type": "number", "minimum": -180, "maximum": 180},
		}),
		"Address": map[string]any{
			"type":     "object",
			"required": []string{"coordinates", "address", "house_number", "road", "neighbourhood", "city", "county", "state", "country", "country_code", "postcode", "place_type", "importance", "place_rank", "boundingbox"},
			"properties": map[string]any{
				"coordinates":  refSchema("GeocodedCoordinate"),
				"address":      map[string]any{"type": "string", "description": "Localized display address after the postcode is removed where present."},
				"house_number": stringOrNull, "road": stringOrNull, "neighbourhood": stringOrNull,
				"city": stringOrNull, "county": stringOrNull, "state": stringOrNull, "country": stringOrNull,
				"country_code": stringOrNull, "postcode": stringOrNull,
				"place_type": map[string]any{}, "importance": map[string]any{}, "place_rank": map[string]any{},
				"boundingbox": map[string]any{"oneOf": []any{refSchema("BoundingBox"), map[string]any{"type": "null"}}},
			},
			"additionalProperties": true,
		},
		"GeocodedCoordinate": objectSchema([]string{"lat", "lng"}, map[string]any{
			"lat": map[string]any{"type": "number", "minimum": -90, "maximum": 90},
			"lng": map[string]any{"type": "number", "minimum": -180, "maximum": 180},
		}),
		"BoundingBox": objectSchema([]string{"south", "north", "west", "east"}, map[string]any{
			"south": map[string]any{"type": "number"}, "north": map[string]any{"type": "number"},
			"west": map[string]any{"type": "number"}, "east": map[string]any{"type": "number"},
		}),
		"PlaceGeometry": refSchema("Coordinate"),
		"PlaceRating": objectSchema([]string{"score", "count"}, map[string]any{
			"score": map[string]any{"type": "number"},
			"count": map[string]any{"type": "number", "minimum": 0},
		}),
		"ProviderPlace": map[string]any{
			"type":     "object",
			"required": []string{"osm_id", "geometry", "distance", "distance_unit", "category", "city", "province", "gallery", "telephone", "rating"},
			"properties": map[string]any{
				"osm_id":        map[string]any{"type": "string"},
				"geometry":      refSchema("PlaceGeometry"),
				"distance":      map[string]any{"type": "number", "minimum": 0},
				"distance_unit": map[string]any{"type": "string", "const": "km"},
				"category":      map[string]any{"type": "string", "enum": []string{"public-bathroom", "mosque"}},
				"name":          map[string]any{},
				"address":       map[string]any{},
				"city":          map[string]any{"type": "null"},
				"province":      map[string]any{"type": "null"},
				"gallery":       map[string]any{"type": "array", "items": map[string]any{}},
				"telephone":     map[string]any{},
				"thumbnail":     map[string]any{},
				"rating":        refSchema("PlaceRating"),
			},
			"additionalProperties": true,
		},
		"CachedPlace": map[string]any{
			"type":     "object",
			"required": []string{"id", "geometry", "distance", "distance_unit", "category", "name"},
			"properties": map[string]any{
				"id":            map[string]any{"type": "string"},
				"osm_id":        map[string]any{"type": "string", "description": "Present for cached Balad places with a provider source ID."},
				"geometry":      refSchema("PlaceGeometry"),
				"distance":      map[string]any{"type": "number", "minimum": 0},
				"distance_unit": map[string]any{"type": "string", "const": "km"},
				"category":      map[string]any{"type": "string", "enum": []string{"public-bathroom", "mosque"}},
				"name":          map[string]any{"type": []string{"string", "null"}},
				"address":       map[string]any{},
			},
			"additionalProperties": true,
		},
		"Place": map[string]any{"oneOf": []any{refSchema("ProviderPlace"), refSchema("CachedPlace")}},
		"AddressDetails": objectSchema([]string{"address", "neighbourhood", "city", "county", "state", "country"}, map[string]any{
			"address":       map[string]any{"type": "string"},
			"neighbourhood": stringOrNull,
			"city":          stringOrNull, "county": stringOrNull, "state": stringOrNull, "country": stringOrNull,
		}),
		"PlaceFindSuccess": objectSchema([]string{"status", "ok", "meta", "places", "address"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 200},
			"ok":     map[string]any{"type": "boolean", "const": true},
			"meta": map[string]any{"allOf": []any{
				refSchema("ApiMeta"),
				objectSchema([]string{"offline", "total", "took"}, map[string]any{
					"offline": map[string]any{"type": "boolean"},
					"total":   map[string]any{"type": "integer", "minimum": 0},
					"took":    map[string]any{"type": "integer", "minimum": 0},
				}),
			}},
			"places":  map[string]any{"type": "array", "items": refSchema("Place")},
			"address": map[string]any{"oneOf": []any{refSchema("Address"), map[string]any{"type": "null"}}},
		}),
		"PlaceFindFailure": objectSchema([]string{"status", "ok"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 500},
			"ok":     map[string]any{"type": "boolean", "const": false},
		}),
		"PlaceFindResponse": map[string]any{"oneOf": []any{refSchema("PlaceFindSuccess"), refSchema("PlaceFindFailure")}},
		"PlaceSearchSuccess": objectSchema([]string{"status", "ok", "meta", "locations"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 200},
			"ok":     map[string]any{"type": "boolean", "const": true},
			"meta": map[string]any{"allOf": []any{
				refSchema("ApiMeta"),
				objectSchema([]string{"total", "query", "took"}, map[string]any{
					"total": map[string]any{"type": "integer", "minimum": 0},
					"query": map[string]any{"type": "string"},
					"took":  map[string]any{"type": "integer", "minimum": 0},
				}),
			}},
			"locations": map[string]any{"type": "array", "minItems": 1, "items": refSchema("Address")},
		}),
		"PlaceSearchFailure": objectSchema([]string{"status", "ok"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 502},
			"ok":     map[string]any{"type": "boolean", "const": false},
		}),
		"PlaceSearchResponse": map[string]any{"oneOf": []any{refSchema("PlaceSearchSuccess"), refSchema("PlaceSearchFailure")}},
		"PlaceReverseSuccess": objectSchema([]string{"status", "ok", "data"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 200},
			"ok":     map[string]any{"type": "boolean", "const": true},
			"data":   refSchema("AddressDetails"),
		}),
		"PlaceReverseFailure": objectSchema([]string{"status", "ok"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 400},
			"ok":     map[string]any{"type": "boolean", "const": false},
		}),
		"PlaceReverseResponse": map[string]any{"oneOf": []any{refSchema("PlaceReverseSuccess"), refSchema("PlaceReverseFailure")}},
		"PageSection":          pageSection,
		"City":                 cityConfig,
		"HandshakeData": map[string]any{
			"type":     "object",
			"required": []string{"version_latest", "version_deprecated", "url_map", "url_form_place", "url_form_ad", "page", "cities", "url_feedback", "url_update", "url_place_share"},
			"properties": map[string]any{
				"version_latest":      map[string]any{"type": "string"},
				"version_deprecated":  map[string]any{"type": "string"},
				"url_map":             map[string]any{"type": "string", "format": "uri-template"},
				"url_form_place":      map[string]any{"type": "string", "format": "uri"},
				"url_form_ad":         map[string]any{"type": "string", "format": "uri"},
				"is_deprecated":       map[string]any{"type": "boolean"},
				"is_update_available": map[string]any{"type": "boolean"},
				"isDeprecated":        map[string]any{"type": "boolean"},
				"isUpdateAvailable":   map[string]any{"type": "boolean"},
				"url_feedback":        stringOrNull,
				"url_update":          stringOrNull,
				"url_share":           map[string]any{"type": "string", "format": "uri"},
				"url_place_share":     map[string]any{"type": "string", "format": "uri"},
				"page": objectSchema([]string{"terms", "privacy", "about"}, map[string]any{
					"terms": refSchema("PageSection"), "privacy": refSchema("PageSection"), "about": refSchema("PageSection"),
				}),
				"cities": map[string]any{"type": "array", "items": refSchema("City")},
			},
			"additionalProperties": true,
		},
		"AppHandshakeResponse": objectSchema([]string{"status", "ok", "meta", "data"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 200},
			"ok":     map[string]any{"type": "boolean", "const": true},
			"meta": map[string]any{"allOf": []any{
				refSchema("ApiMeta"),
				objectSchema([]string{"took"}, map[string]any{"took": map[string]any{"type": "integer", "minimum": 0}}),
			}},
			"data": refSchema("HandshakeData"),
		}),
		"Advertisement": map[string]any{
			"type":     "object",
			"required": []string{"cities"},
			"properties": map[string]any{
				"cities": map[string]any{"type": "array", "items": map[string]any{}, "description": "Empty for a generic ad, or containing city identifiers for city-specific ads."},
			},
			"additionalProperties": true,
			"description":          "Advertisement payload passed through from app/ad.json. Only the cities array is inspected by the selector; no other ad fields are imposed by the API implementation.",
		},
		"AdvertisementSuccess": objectSchema([]string{"status", "ok", "meta", "ad"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 200},
			"ok":     map[string]any{"type": "boolean", "const": true},
			"meta": map[string]any{"allOf": []any{
				refSchema("ApiMeta"),
				objectSchema([]string{"took"}, map[string]any{"took": map[string]any{"type": "integer", "minimum": 0}}),
			}},
			"ad": refSchema("Advertisement"),
		}),
		"AdvertisementFailure": objectSchema([]string{"status", "ok"}, map[string]any{
			"status":  map[string]any{"type": "integer", "enum": []int{400, 404, 500}},
			"ok":      map[string]any{"type": "boolean", "const": false},
			"message": map[string]any{"type": "string", "enum": []string{"NO_AD", "AD_NOT_FOUND", "INVALID_TYPE"}},
			"type":    map[string]any{"type": "string", "enum": []string{"any", "city"}},
		}),
		"AdvertisementResponse": map[string]any{"oneOf": []any{refSchema("AdvertisementSuccess"), refSchema("AdvertisementFailure")}},
		"SystemInfoResponse": objectSchema([]string{"status", "ok", "info"}, map[string]any{
			"status": map[string]any{"type": "integer", "const": 200},
			"ok":     map[string]any{"type": "boolean", "const": true},
			"info": objectSchema([]string{"namespace", "node"}, map[string]any{
				"namespace": map[string]any{"type": "string", "const": "mapine"},
				"node":      map[string]any{"type": "string", "description": "Configured NODE_ID or the process hostname."},
			}),
		}),
		"ActionParameterRule": map[string]any{"type": "object", "additionalProperties": true},
		"ActionInventoryEntry": objectSchema([]string{"action", "service", "auth", "cache", "visibility", "nodes", "params"}, map[string]any{
			"action":     map[string]any{"type": "string", "description": "Action identifier in the current service inventory."},
			"service":    map[string]any{"type": "string"},
			"auth":       map[string]any{"type": "boolean"},
			"cache":      map[string]any{"type": "boolean"},
			"visibility": map[string]any{"type": "string"},
			"nodes":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"params":     map[string]any{"type": "object", "additionalProperties": refSchema("ActionParameterRule")},
		}),
		"SystemActionsResponse": objectSchema([]string{"status", "ok", "actions"}, map[string]any{
			"status":  map[string]any{"type": "integer", "const": 200},
			"ok":      map[string]any{"type": "boolean", "const": true},
			"actions": map[string]any{"type": "array", "items": refSchema("ActionInventoryEntry")},
		}),
		"RaahPOIToken": map[string]any{"type": "string", "description": "Opaque provider token. Placeholder values in examples are not real provider identifiers."},
		"RaahRawResponse": map[string]any{
			"description": "Raw upstream response body. Successful JSON is embedded as JSON; non-JSON error bodies are returned as text.",
			"oneOf": []any{
				map[string]any{"type": "object", "additionalProperties": true},
				map[string]any{"type": "array", "items": map[string]any{}},
				map[string]any{"type": "string"},
				map[string]any{"type": "number"},
				map[string]any{"type": "boolean"},
				map[string]any{"type": "null"},
			},
		},
		"RaahTokenDiscoveryResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"poi-tokens": map[string]any{"type": "array", "items": refSchema("RaahPOIToken"), "description": "The only response property explicitly read by the legacy helper."},
			},
			"additionalProperties": true,
			"description":          "Source-derived response expectation; the provider's full schema is not known from the client implementation.",
		},
		"RaahGeometry": objectSchema([]string{"coordinates"}, map[string]any{
			"coordinates": map[string]any{"type": "array", "minItems": 2, "items": map[string]any{"type": "number"}, "description": "Verified Raah/GeoJSON order is [longitude, latitude]. The Go adapter converts this once into Mapine's canonical Latitude/Longitude fields."},
		}),
		"RaahRating": map[string]any{
			"type": "object", "properties": map[string]any{
				"score": map[string]any{"type": "number"}, "count": map[string]any{"type": "number"},
			}, "additionalProperties": true,
			"description": "Source-derived properties read by Mapine; types are not validated by the legacy client.",
		},
		"RaahImage": map[string]any{
			"type": "object", "properties": map[string]any{"full": map[string]any{}},
			"additionalProperties": true,
			"description":          "The transformation reads `full`; other image fields are not constrained by the source.",
		},
		"RaahPOIItem": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"token": refSchema("RaahPOIToken"), "geometry": refSchema("RaahGeometry"),
				"name": map[string]any{}, "address": map[string]any{}, "thumbnail": map[string]any{},
				"images":    map[string]any{"type": "array", "items": refSchema("RaahImage")},
				"telephone": map[string]any{"description": "The legacy transform checks `.length`; exact provider type is not established."},
				"rating":    refSchema("RaahRating"),
			},
			"required":             []string{"token", "geometry"},
			"additionalProperties": true,
			"description":          "Fields accessed by the source-derived transformation. Presence and types beyond the required accesses are not guaranteed by source evidence.",
		},
		"RaahPOIDetailsResponse": objectSchema([]string{"items"}, map[string]any{
			"items": map[string]any{"type": "array", "items": refSchema("RaahPOIItem")},
		}),
		"RaahNormalizedPlace": map[string]any{
			"type":     "object",
			"required": []string{"osm_id", "geometry", "distance", "distance_unit", "category", "city", "province", "gallery", "telephone", "rating"},
			"properties": map[string]any{
				"osm_id":   map[string]any{"type": "string", "description": "Compatibility field carrying the provider token; it is not an OpenStreetMap identifier."},
				"geometry": objectSchema([]string{"lat", "lon"}, map[string]any{"lat": map[string]any{"type": "number"}, "lon": map[string]any{"type": "number"}}),
				"distance": map[string]any{"type": "number"}, "distance_unit": map[string]any{"type": "string", "const": "km"},
				"category": map[string]any{"type": "string"}, "city": map[string]any{"type": []string{"string", "null"}},
				"province": map[string]any{"type": []string{"string", "null"}}, "name": map[string]any{},
				"address": map[string]any{}, "thumbnail": map[string]any{},
				"gallery":   map[string]any{"type": "array", "items": map[string]any{}},
				"telephone": map[string]any{}, "rating": refSchema("RaahRating"),
			},
			"additionalProperties": true,
			"description":          "Current Go `place.find` fresh-result shape produced by the non-persisting diagnostic preview. Optional name/address/thumbnail properties are omitted when unavailable.",
		},
		"RaahDiagnosticUpstream": objectSchema([]string{"method", "url", "status_code"}, map[string]any{
			"method":      map[string]any{"type": "string", "const": "GET"},
			"url":         map[string]any{"type": "string", "format": "uri"},
			"status_code": map[string]any{"type": "integer", "minimum": 0},
		}),
		"RaahDetailsDiagnosticRequest": objectSchema([]string{"tokens"}, map[string]any{
			"tokens": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Opaque real token from token discovery; commas, path separators, whitespace padding, and control characters are rejected."}},
		}),
		"RaahDetailsBatch": objectSchema([]string{"url", "status_code"}, map[string]any{
			"url": map[string]any{"type": "string", "format": "uri"}, "status_code": map[string]any{"type": "integer", "minimum": 0},
			"raw_response": refSchema("RaahRawResponse"),
		}),
		"RaahTokensDiagnosticResponse": map[string]any{
			"type": "object", "required": []string{"ok", "upstream", "poi_tokens"},
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"}, "upstream": refSchema("RaahDiagnosticUpstream"),
				"raw_response": refSchema("RaahTokenDiscoveryResponse"),
				"poi_tokens":   map[string]any{"type": "array", "items": refSchema("RaahPOIToken")},
				"error":        map[string]any{"type": "string"},
			}, "additionalProperties": true,
		},
		"RaahDetailsDiagnosticResponse": map[string]any{
			"type": "object", "required": []string{"ok", "upstream_requests"},
			"properties": map[string]any{
				"ok":           map[string]any{"type": "boolean"},
				"upstream":     refSchema("RaahDiagnosticUpstream"),
				"raw_response": refSchema("RaahPOIDetailsResponse"),
				"upstream_requests": map[string]any{"type": "array", "items": objectSchema([]string{"url", "status_code", "raw_response"}, map[string]any{
					"url": map[string]any{"type": "string", "format": "uri"}, "status_code": map[string]any{"type": "integer"},
					"raw_response": refSchema("RaahRawResponse"),
				})},
				"error": map[string]any{"type": "string"},
			}, "additionalProperties": true,
		},
		"RaahSearchDiagnosticResponse": map[string]any{
			"type": "object", "required": []string{"ok", "token_discovery", "detail_requests", "normalized_places", "persistence"},
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"},
				"token_discovery": objectSchema([]string{"method", "url", "status_code", "poi_tokens"}, map[string]any{
					"method": map[string]any{"type": "string", "const": "GET"}, "url": map[string]any{"type": "string", "format": "uri"},
					"status_code": map[string]any{"type": "integer", "minimum": 0}, "raw_response": refSchema("RaahRawResponse"),
					"poi_tokens": map[string]any{"type": "array", "items": refSchema("RaahPOIToken")},
				}),
				"detail_requests":    map[string]any{"type": "array", "items": refSchema("RaahDetailsBatch")},
				"normalized_places":  map[string]any{"type": "array", "items": refSchema("RaahNormalizedPlace")},
				"persistence":        objectSchema([]string{"performed"}, map[string]any{"performed": map[string]any{"type": "boolean", "const": false}}),
				"normalization_note": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"},
			}, "additionalProperties": true,
		},
		"RaahDiagnosticError": map[string]any{
			"type": "object", "required": []string{"ok", "error"},
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean", "const": false}, "error": map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"}, "raw_response": refSchema("RaahRawResponse"),
				"upstream": refSchema("RaahDiagnosticUpstream"),
			}, "additionalProperties": true,
		},
		"ValidationIssue": objectSchema([]string{"message", "param", "type"}, map[string]any{
			"message": map[string]any{"type": "string"},
			"param":   map[string]any{"type": "string"},
			"type":    map[string]any{"type": "string", "enum": []string{"required", "number", "numberMin", "numberMax", "string", "stringMin", "enumValue"}},
		}),
		"ValidationError": objectSchema([]string{"ok", "message", "error", "params"}, map[string]any{
			"ok":      map[string]any{"type": "boolean", "const": false},
			"message": map[string]any{"type": "string", "const": "VALIDATION_FAILED"},
			"error":   map[string]any{"type": "string", "const": "BAD_PARAMS"},
			"params":  map[string]any{"type": "array", "items": refSchema("ValidationIssue")},
		}),
		"RequestError": objectSchema([]string{"ok", "message"}, map[string]any{
			"ok":      map[string]any{"type": "boolean", "const": false},
			"message": map[string]any{"type": "string", "enum": []string{"INVALID_JSON", "REQUEST_TOO_LARGE"}},
		}),
	}
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": true}
}

func refSchema(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func numberQuery(min, max float64) map[string]any {
	return map[string]any{"type": "number", "minimum": min, "maximum": max}
}

func numberQueryDefault(min, max, defaultValue float64) map[string]any {
	schema := numberQuery(min, max)
	schema["default"] = defaultValue
	return schema
}

func numberBody(min, max float64) map[string]any {
	return map[string]any{"oneOf": []any{
		map[string]any{"type": "number", "minimum": min, "maximum": max},
		map[string]any{"type": "string", "pattern": "^-?(?:[0-9]+(?:\\.[0-9]*)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?$"},
	}}
}

func numberBodyDefault(min, max, defaultValue float64) map[string]any {
	return map[string]any{"oneOf": []any{
		map[string]any{"type": "number", "minimum": min, "maximum": max, "default": defaultValue},
		map[string]any{"type": "string", "pattern": "^-?(?:[0-9]+(?:\\.[0-9]*)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?$"},
	}}
}

func stringQuery(minLength int) map[string]any {
	schema := map[string]any{"type": "string"}
	if minLength > 0 {
		schema["minLength"] = minLength
	}
	return schema
}

func stringBody(minLength int) map[string]any {
	return stringQuery(minLength)
}

func nullableStringBody(minLength int) map[string]any {
	schema := map[string]any{"type": []string{"string", "null"}}
	if minLength > 0 {
		schema["minLength"] = minLength
	}
	return schema
}

func enumQuery(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func enumQueryDefault(defaultValue string, values ...string) map[string]any {
	schema := enumQuery(values...)
	schema["default"] = defaultValue
	return schema
}

func enumBody(values ...string) map[string]any {
	return enumQuery(values...)
}

func enumBodyDefault(defaultValue string, values ...string) map[string]any {
	return enumQueryDefault(defaultValue, values...)
}

func example(summary string, value any) map[string]any {
	return map[string]any{"summary": summary, "value": value}
}

func sampleAddress(display, road, city string) map[string]any {
	return map[string]any{
		"coordinates": map[string]any{"lat": 35.6892, "lng": 51.389},
		"address":     display, "house_number": nil, "road": road, "neighbourhood": "ولیعصر",
		"city": city, "county": "تهران", "state": "تهران", "country": "ایران",
		"country_code": "ir", "postcode": nil, "place_type": "road", "importance": 0.08,
		"place_rank":  26,
		"boundingbox": map[string]any{"south": 35.68, "north": 35.70, "west": 51.38, "east": 51.40},
	}
}

func init() {
	// Keep development failures obvious if operation IDs or JSON-compatible
	// example values drift as documentation metadata is edited.
	ids := map[string]struct{}{}
	for _, action := range documentedActions {
		for _, method := range documentedHTTPMethods {
			id := operationID(action.name, method)
			if _, exists := ids[id]; exists {
				panic(fmt.Sprintf("duplicate OpenAPI operationId %q", id))
			}
			ids[id] = struct{}{}
		}
	}
	if _, err := json.Marshal(Specification()); err != nil {
		panic(fmt.Sprintf("invalid OpenAPI specification value: %v", err))
	}
}
