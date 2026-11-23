package downloader

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	testFixtureOnce sync.Once
	testFixturePath string
	testFixtureErr  error
)

func pbfFixture(t *testing.T) []byte {
	t.Helper()
	testFixtureOnce.Do(func() {
		output, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/paulmach/osm").Output()
		if err != nil {
			testFixtureErr = err
			return
		}
		testFixturePath = filepath.Join(strings.TrimSpace(string(output)), "testdata", "delaware-latest.osm.pbf")
	})
	if testFixtureErr != nil {
		t.Fatal(testFixtureErr)
	}
	content, err := os.ReadFile(testFixturePath)
	if err != nil {
		t.Fatalf("read module PBF fixture: %v", err)
	}
	return content
}

func TestDownloadProgressCurrentSnapshotAndAtomicProtection(t *testing.T) {
	content := pbfFixture(t)
	server := newFixtureServer(t, content)
	defer server.Close()
	dir := t.TempDir()
	destination := filepath.Join(dir, "iran.osm.pbf")
	country := Country{URL: server.URL + "/iran.osm.pbf", ChecksumURL: server.URL + "/iran.osm.pbf.md5", Filename: "iran.osm.pbf"}
	var updates []Progress
	result, err := Download(context.Background(), Options{
		Country: "iran", Definition: country, Destination: destination,
		Client: server.Client(), Progress: func(p Progress) { updates = append(updates, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(len(content)) || result.Skipped {
		t.Fatalf("download result: %+v", result)
	}
	if !result.ChecksumAvailable || !result.ChecksumVerified {
		t.Fatalf("authoritative test checksum was not verified: %+v", result)
	}
	if len(updates) == 0 {
		t.Fatal("download progress was not reported")
	}
	last := updates[len(updates)-1]
	if last.Downloaded != int64(len(content)) || last.Total != int64(len(content)) || !last.TotalKnown {
		t.Fatalf("final progress = %+v", last)
	}
	stored, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) {
		t.Fatal("downloaded bytes differ from server representation")
	}

	result, err = Download(context.Background(), Options{
		Country: "iran", Definition: country, Destination: destination, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !result.CurrentKnown || !result.Current {
		t.Fatalf("unchanged snapshot was not recognized: %+v", result)
	}
	if got := server.gets.Load(); got != 1 {
		t.Fatalf("GET requests = %d, want one", got)
	}
}

func TestDownloadResumesValidatedPartialAndRestartsOnChangedEntity(t *testing.T) {
	content := pbfFixture(t)
	server := newFixtureServer(t, content)
	defer server.Close()
	country := Country{URL: server.URL + "/iran.osm.pbf", Filename: "iran.osm.pbf"}

	t.Run("resume 206", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "iran.osm.pbf")
		seedPartial(t, destination+".part", content[:len(content)/2], remoteMetadata{ETag: `"fixture-v1"`, Size: int64(len(content))})
		result, err := Download(context.Background(), Options{
			Country: "iran", Definition: country, Destination: destination, Client: server.Client(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ResumedFrom != int64(len(content)/2) {
			t.Fatalf("resume offset = %d", result.ResumedFrom)
		}
		got, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(content) {
			t.Fatal("resumed file does not match the complete PBF")
		}
	})

	t.Run("If-Range receives 200", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "iran.osm.pbf")
		old := append([]byte(nil), content...)
		if err := os.WriteFile(destination, old, 0600); err != nil {
			t.Fatal(err)
		}
		seedPartial(t, destination+".part", []byte("stale"), remoteMetadata{ETag: `"old-version"`, Size: 5})
		result, err := Download(context.Background(), Options{
			Country: "iran", Definition: country, Destination: destination, Client: server.Client(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ResumedFrom != 0 {
			t.Fatalf("changed entity should restart, result=%+v", result)
		}
		got, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(content) {
			t.Fatal("HTTP 200 response was appended instead of replacing stale partial data")
		}
	})
}

func TestInvalidContentRangeRestartsFromTheBeginning(t *testing.T) {
	content := pbfFixture(t)
	var gets atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md5") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"fixture-v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			return
		}
		gets.Add(1)
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(content)-1, len(content)))
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "iran.osm.pbf")
	seedPartial(t, destination+".part", []byte("partial"), remoteMetadata{ETag: `"fixture-v1"`})
	_, err := Download(context.Background(), Options{
		Country: "iran", Definition: Country{URL: server.URL + "/iran.osm.pbf"},
		Destination: destination, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 2 {
		t.Fatalf("GET requests = %d, want invalid range and a fresh request", gets.Load())
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("invalid partial response was appended")
	}
}

func TestRangeNotSatisfiableRestartsWithoutAppending(t *testing.T) {
	content := pbfFixture(t)
	var rangeRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md5") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"fixture-v1"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			return
		}
		if r.Header.Get("Range") != "" {
			rangeRequests.Add(1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(content)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "iran.osm.pbf")
	seedPartial(t, destination+".part", content, remoteMetadata{ETag: `"fixture-v1"`, Size: int64(len(content))})
	_, err := Download(context.Background(), Options{
		Country: "iran", Definition: Country{URL: server.URL + "/iran.osm.pbf"},
		Destination: destination, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rangeRequests.Load() != 1 {
		t.Fatalf("range requests = %d, want one 416 before restart", rangeRequests.Load())
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("416 retry did not produce the complete snapshot")
	}
}

func TestInterruptedDownloadCanResumeAndBadResponseKeepsExistingFile(t *testing.T) {
	content := pbfFixture(t)
	server := newFixtureServer(t, content)
	defer server.Close()
	country := Country{URL: server.URL + "/iran.osm.pbf", Filename: "iran.osm.pbf"}
	destination := filepath.Join(t.TempDir(), "iran.osm.pbf")
	server.interruptOnce.Store(true)
	if _, err := Download(context.Background(), Options{
		Country: "iran", Definition: country, Destination: destination, Client: server.Client(),
	}); err == nil {
		t.Fatal("truncated response should fail size validation")
	}
	partialInfo, err := os.Stat(destination + ".part")
	if err != nil || partialInfo.Size() == 0 || partialInfo.Size() >= int64(len(content)) {
		t.Fatalf("expected resumable partial file, stat=%v err=%v", partialInfo, err)
	}
	partialMetadata, metadataErr := readMetadata(destination + ".part.meta")
	if metadataErr != nil {
		t.Fatalf("read interrupted transfer validators: %v", metadataErr)
	}
	if rangeValidator(partialMetadata) == "" {
		t.Fatalf("interrupted transfer has no range validator: %+v", partialMetadata)
	}
	result, err := Download(context.Background(), Options{
		Country: "iran", Definition: country, Destination: destination, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResumedFrom == 0 {
		t.Fatalf("retry did not resume: result=%+v interrupted-meta=%+v", result, partialMetadata)
	}

	// A failed forced replacement must leave the previous valid file untouched.
	before, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	bad := newBadFixtureServer()
	defer bad.Close()
	badCountry := Country{URL: bad.URL + "/iran.osm.pbf", Filename: "iran.osm.pbf"}
	if _, err = Download(context.Background(), Options{
		Country: "iran", Definition: badCountry, Destination: destination, Client: bad.Client(), Force: true,
	}); err == nil {
		t.Fatal("invalid PBF response should fail")
	}
	after, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed replacement changed the previous valid snapshot")
	}
}

func TestExistingSnapshotWithUnknownRemoteMetadataIsNotClaimedCurrent(t *testing.T) {
	content := pbfFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			return
		}
		if strings.HasSuffix(r.URL.Path, ".md5") {
			http.NotFound(w, r)
			return
		}
		w.Write(content)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "existing.osm.pbf")
	if err := os.WriteFile(destination, content, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Download(context.Background(), Options{
		Country: "iran", Definition: Country{URL: server.URL + "/iran.osm.pbf", Filename: "iran.osm.pbf"},
		Destination: destination, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || result.CurrentKnown || result.Current {
		t.Fatalf("unknown remote validators should not be reported current: %+v", result)
	}
}

func TestDownloadCancellation(t *testing.T) {
	content := pbfFixture(t)
	server := newFixtureServer(t, content)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Download(ctx, Options{
		Country: "iran", Definition: Country{URL: server.URL + "/iran.osm.pbf"},
		Destination: filepath.Join(t.TempDir(), "cancelled.pbf"), Client: server.Client(),
	})
	if err == nil {
		t.Fatal("cancelled request unexpectedly succeeded")
	}
}

func TestStrongETagChangeOverridesMatchingLastModified(t *testing.T) {
	local := remoteMetadata{ETag: `"old"`, LastModified: "Mon, 23 Nov 2026 10:00:00 GMT", Size: 100}
	remote := remoteMetadata{ETag: `"new"`, LastModified: local.LastModified, Size: 100}
	if isSameRepresentation(local, remote) {
		t.Fatal("different ETags must not be treated as the same representation")
	}
}

func TestCountryRegistryLoadingAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "countries.yaml")
	if err := os.WriteFile(path, []byte(`version: 1
countries:
  iran:
    url: https://download.geofabrik.de/asia/iran-latest.osm.pbf
    filename: iran-latest.osm.pbf
`), 0600); err != nil {
		t.Fatal(err)
	}
	countries, err := LoadCountries(path)
	if err != nil {
		t.Fatal(err)
	}
	country, err := countries.Country("iran")
	if err != nil || country.Filename != "iran-latest.osm.pbf" {
		t.Fatalf("country=%+v err=%v", country, err)
	}
	if _, err := countries.Country("unknown"); err == nil {
		t.Fatal("unknown country was accepted")
	}
	if err := os.WriteFile(path, []byte(`version: 1
countries:
  iran:
    url: http://example.org/iran.pbf
    filename: iran.pbf
    checksum_url: http://example.org/iran.pbf.md5
`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCountries(path); err == nil {
		t.Fatal("insecure source/checksum URLs should be rejected")
	}
}

func TestDownloadTransferTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("ETag", `"slow"`)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	_, err := Download(context.Background(), Options{
		Country: "iran", Definition: Country{URL: server.URL + "/iran.osm.pbf"},
		Destination: filepath.Join(t.TempDir(), "timeout.pbf"), Client: server.Client(),
		Timeout: time.Nanosecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("download error = %v, want context deadline", err)
	}
}

func TestHTTPSRedirectIsFollowed(t *testing.T) {
	content := pbfFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/iran.osm.pbf", http.StatusFound)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".md5") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"fixture-v1"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()
	result, err := Download(context.Background(), Options{
		Country: "iran",
		Definition: Country{
			URL: server.URL + "/start", ChecksumURL: server.URL + "/missing.md5",
		},
		Destination: filepath.Join(t.TempDir(), "iran.osm.pbf"),
		Client:      server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(len(content)) {
		t.Fatalf("redirected download size=%d, want %d", result.Bytes, len(content))
	}
}

type fixtureHTTPServer struct {
	*httptest.Server
	gets          atomic.Int64
	interruptOnce atomic.Bool
}

func newFixtureServer(t *testing.T, content []byte) *fixtureHTTPServer {
	t.Helper()
	sum := md5.Sum(content)
	fixture := &fixtureHTTPServer{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md5") {
			fmt.Fprintf(w, "%s  iran-latest.osm.pbf\n", hex.EncodeToString(sum[:]))
			return
		}
		w.Header().Set("ETag", `"fixture-v1"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		if r.Method == http.MethodHead {
			return
		}
		fixture.gets.Add(1)
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=") {
			if r.Header.Get("If-Range") != `"fixture-v1"` {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(content)
				return
			}
			var start int
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &start); err != nil || start < 0 || start >= len(content) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
			w.Header().Set("Content-Length", fmt.Sprint(len(content)-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[start:])
			return
		}
		if fixture.interruptOnce.Swap(false) {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = w.Write(content[:len(content)/2])
			return
		}
		_, _ = w.Write(content)
	}))
	return fixture
}

func newBadFixtureServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md5") {
			fmt.Fprintln(w, "d41d8cd98f00b204e9800998ecf8427e")
			return
		}
		w.Header().Set("ETag", `"bad"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "12")
			return
		}
		w.Write([]byte("not a PBF!!"))
	}))
}

func seedPartial(t *testing.T, path string, content []byte, metadata remoteMetadata) {
	t.Helper()
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeMetadata(path+".meta", metadata); err != nil {
		t.Fatal(err)
	}
}

// Use the standard atomic package behind small wrappers so fixture tests can
// safely share their server state with HTTP handlers.
