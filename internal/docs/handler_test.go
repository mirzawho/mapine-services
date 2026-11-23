package docs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDocumentationRoutesServeScalarAndOpenAPI31(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux)

	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET /docs status=%d body=%s", page.Code, page.Body)
	}
	if got := page.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("GET /docs content-type=%q", got)
	}
	for _, want := range []string{
		"<title>Mapine API</title>",
		`rel="icon"`,
		`src="docs/assets/scalar-1.82.1.js"`,
		`src="docs/assets/init.js"`,
	} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("GET /docs missing %q", want)
		}
	}
	if strings.Contains(page.Body.String(), "cdn.") {
		t.Fatal("docs HTML must load its frontend from the application origin")
	}

	favicon := httptest.NewRecorder()
	mux.ServeHTTP(favicon, httptest.NewRequest(http.MethodGet, "/docs/assets/favicon.svg", nil))
	if favicon.Code != http.StatusOK || !strings.HasPrefix(favicon.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("self-hosted favicon status=%d content-type=%q", favicon.Code, favicon.Header().Get("Content-Type"))
	}

	specResponse := httptest.NewRecorder()
	mux.ServeHTTP(specResponse, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if specResponse.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json status=%d body=%s", specResponse.Code, specResponse.Body)
	}
	if got := specResponse.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("GET /openapi.json content-type=%q", got)
	}
	var document map[string]any
	if err := json.Unmarshal(specResponse.Body.Bytes(), &document); err != nil {
		t.Fatalf("GET /openapi.json is not JSON: %v", err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("OpenAPI version=%v", document["openapi"])
	}
}

func TestScalarAssetsAreSelfHostedAndPinned(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux)

	bundle := httptest.NewRecorder()
	mux.ServeHTTP(bundle, httptest.NewRequest(http.MethodGet, "/docs/assets/scalar-1.82.1.js", nil))
	if bundle.Code != http.StatusOK {
		t.Fatalf("Scalar asset status=%d", bundle.Code)
	}
	if bundle.Body.Len() < 1_000_000 || !strings.Contains(bundle.Body.String(), "window.Scalar") {
		t.Fatalf("expected the pinned standalone Scalar browser build, got %d bytes", bundle.Body.Len())
	}
	if got := bundle.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("versioned asset cache-control=%q", got)
	}

	init := httptest.NewRecorder()
	mux.ServeHTTP(init, httptest.NewRequest(http.MethodGet, "/docs/assets/init.js", nil))
	if init.Code != http.StatusOK {
		t.Fatalf("Scalar initializer status=%d", init.Code)
	}
	for _, want := range []string{
		"Scalar.createApiReference",
		`url: "openapi.json"`,
		"apiBasePath",
		"withDefaultFonts: false",
		"hideDarkModeToggle: false",
		"telemetry: false",
		"agent: { disabled: true }",
	} {
		if !strings.Contains(init.Body.String(), want) {
			t.Errorf("Scalar initializer missing %q", want)
		}
	}
	if strings.Contains(init.Body.String(), "proxy.scalar.com") || strings.Contains(init.Body.String(), "cdn.jsdelivr.net") {
		t.Fatal("Scalar must use same-origin assets/spec and no hosted proxy")
	}
}

func TestOpenAPISpecListsOnlyPublicDispatchedActionsAndGetPostMethods(t *testing.T) {
	document := Specification()
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths has type %T", document["paths"])
	}
	wantPaths := []string{
		"/v1/call/place.find",
		"/v1/call/place.search",
		"/v1/call/place.reverse",
		"/v1/call/app.handshake",
		"/v1/call/ad.one",
	}
	if len(paths) != len(wantPaths)+3 {
		t.Fatalf("documented %d paths, want %d public plus 3 diagnostics: %v", len(paths), len(wantPaths), paths)
	}
	for _, path := range wantPaths {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing exact action path %q", path)
		}
		if !reflect.DeepEqual(sortedKeys(pathItem), []string{"get", "post"}) {
			t.Errorf("%s has methods %v", path, sortedKeys(pathItem))
		}
		for method, rawOperation := range pathItem {
			operation := rawOperation.(map[string]any)
			if operation["security"] == nil || len(operation["security"].([]any)) != 0 {
				t.Errorf("%s %s must not require authentication", method, path)
			}
			if operation["operationId"] == nil {
				t.Errorf("%s %s has no operationId", method, path)
			}
		}
	}
	for path := range paths {
		if strings.Contains(path, "auth") || strings.Contains(path, "$") {
			t.Errorf("non-public/internal action was documented: %s", path)
		}
	}
	if _, ok := document["security"]; ok {
		t.Fatal("the OpenAPI root must not impose authentication")
	}
}

func TestRaahDiagnosticsAreDocumentedAsProtectedOptInHandlers(t *testing.T) {
	document := Specification()
	paths := document["paths"].(map[string]any)
	for _, tc := range []struct {
		path, method string
	}{
		{raahTokensPath, "get"},
		{raahDetailsPath, "post"},
		{raahSearchPath, "get"},
	} {
		pathItem, ok := paths[tc.path].(map[string]any)
		if !ok || len(pathItem) != 1 {
			t.Fatalf("missing diagnostic operation %s", tc.path)
		}
		operation, ok := pathItem[tc.method].(map[string]any)
		if !ok {
			t.Fatalf("missing %s %s", tc.method, tc.path)
		}
		if operation["tags"].([]string)[0] != "Raah / Balad Provider" {
			t.Errorf("%s has tags %v", tc.path, operation["tags"])
		}
		security, hasSecurity := operation["security"].([]any)
		if !hasSecurity || len(security) != 0 {
			t.Errorf("%s should explicitly declare that no authentication is required", tc.path)
		}
		if operation["responses"].(map[string]any)["404"] == nil {
			t.Errorf("%s must document disabled response", tc.path)
		}
		if !strings.Contains(operation["description"].(string), "DEBUG=true") {
			t.Errorf("%s does not explain opt-in availability", tc.path)
		}
	}
	details := paths[raahDetailsPath].(map[string]any)["post"].(map[string]any)
	if details["requestBody"] == nil {
		t.Fatal("details diagnostic must document its token request body")
	}
}

func TestOpenAPISpecDescribesParametersBodiesHeadersAndResponseMetadata(t *testing.T) {
	document := Specification()
	paths := document["paths"].(map[string]any)

	find := paths["/v1/call/place.find"].(map[string]any)
	get := find["get"].(map[string]any)
	parameters := get["parameters"].([]any)
	required := map[string]bool{}
	for _, raw := range parameters {
		parameter := raw.(map[string]any)
		if parameter["in"] == "query" && parameter["required"] == true {
			required[parameter["name"].(string)] = true
		}
	}
	for _, name := range []string{"lat", "lon", "zoom", "category"} {
		if !required[name] {
			t.Errorf("place.find missing required query parameter %q", name)
		}
	}
	for _, name := range []string{"gps_lat", "gps_lon", "force"} {
		if required[name] {
			t.Errorf("place.find optional query parameter %q marked required", name)
		}
	}
	post := find["post"].(map[string]any)
	if post["requestBody"] == nil {
		t.Fatal("POST operation is missing its JSON request body")
	}
	for _, raw := range post["parameters"].([]any) {
		parameter := raw.(map[string]any)
		if parameter["in"] == "query" {
			t.Errorf("POST must not document query parameter %q", parameter["name"])
		}
	}
	requestBody := post["requestBody"].(map[string]any)
	content := requestBody["content"].(map[string]any)["application/json"].(map[string]any)
	schema := content["schema"].(map[string]any)
	objectSchema := schema["oneOf"].([]any)[0].(map[string]any)
	bodyProperties := objectSchema["properties"].(map[string]any)
	for _, name := range []string{"lat", "lon", "zoom", "category", "gps_lat", "gps_lon", "force"} {
		if _, ok := bodyProperties[name]; !ok {
			t.Errorf("POST body is missing action parameter %q", name)
		}
	}
	bodyExample := content["example"].(map[string]any)
	if bodyExample["force"] != true {
		t.Error("place.find POST example should demonstrate force in the JSON body")
	}

	handshake := paths["/v1/call/app.handshake"].(map[string]any)["get"].(map[string]any)
	headers := map[string]bool{}
	for _, raw := range handshake["parameters"].([]any) {
		parameter := raw.(map[string]any)
		if parameter["in"] == "header" {
			headers[parameter["name"].(string)] = true
		}
	}
	for _, name := range []string{"x-app", "x-version", "x-flavor"} {
		if !headers[name] {
			t.Errorf("handshake missing header %q", name)
		}
	}

	responses := post["responses"].(map[string]any)
	success := responses["200"].(map[string]any)
	for _, header := range []string{".id", ".took"} {
		if _, ok := success["headers"].(map[string]any)[header]; !ok {
			t.Errorf("response header %q is not documented", header)
		}
	}
	if len(find["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["examples"].(map[string]any)) < 3 {
		t.Fatal("place.find must include bathroom, mosque, and empty-result response examples")
	}
}

func TestOpenAPIExamplesMarshalAndReferencesResolve(t *testing.T) {
	document := Specification()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal spec and examples: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal generated OpenAPI JSON: %v", err)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	assertReferencesResolve(t, roundTrip, components)
}

func assertReferencesResolve(t *testing.T, value any, schemas map[string]any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		if target, ok := value["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if strings.HasPrefix(target, prefix) {
				if _, found := schemas[strings.TrimPrefix(target, prefix)]; !found {
					t.Errorf("unresolved OpenAPI schema reference %q", target)
				}
			} else {
				t.Errorf("unexpected OpenAPI reference %q", target)
			}
		}
		for _, child := range value {
			assertReferencesResolve(t, child, schemas)
		}
	case []any:
		for _, child := range value {
			assertReferencesResolve(t, child, schemas)
		}
	}
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
