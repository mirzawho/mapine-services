package downloader

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mirzawho/mapine-services/internal/osm/progress"
	"github.com/paulmach/osm/osmpbf"
	"gopkg.in/yaml.v3"
)

type Country struct {
	URL         string `yaml:"url"`
	Filename    string `yaml:"filename"`
	ChecksumURL string `yaml:"checksum_url"`
}

type Countries struct {
	Version   int                `yaml:"version"`
	Countries map[string]Country `yaml:"countries"`
}

type Options struct {
	Country     string
	Definition  Country
	Destination string
	Force       bool
	Client      *http.Client
	Progress    func(Progress)
	Timeout     time.Duration
}

type Progress struct {
	State       string
	Downloaded  int64
	Total       int64
	TotalKnown  bool
	SpeedBytes  float64
	Elapsed     time.Duration
	ETA         time.Duration
	ETAKnown    bool
	ResumedFrom int64
}

type Result struct {
	Path              string
	Bytes             int64
	ResumedFrom       int64
	Skipped           bool
	Current           bool
	CurrentKnown      bool
	RemoteETag        string
	RemoteModTime     string
	ChecksumAvailable bool
	ChecksumVerified  bool
}

type remoteMetadata struct {
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
	Size         int64  `json:"size"`
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n.Add(int64(n))
	return n, err
}

func LoadCountries(path string) (*Countries, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open country configuration %q: %w", path, err)
	}
	defer f.Close()
	dec := yamlDecoder(f)
	var cfg Countries
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode country configuration %q: %w", path, err)
	}
	if cfg.Version != 1 || len(cfg.Countries) == 0 {
		return nil, errors.New("country configuration must have version 1 and at least one country")
	}
	for name, country := range cfg.Countries {
		if name == "" || country.URL == "" || country.Filename == "" ||
			filepath.Base(country.Filename) != country.Filename || country.Filename == "." ||
			!strings.HasSuffix(country.Filename, ".osm.pbf") {
			return nil, fmt.Errorf("invalid country configuration for %q", name)
		}
		parsed, parseErr := url.Parse(country.URL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("country %q URL must use HTTPS", name)
		}
		if country.ChecksumURL != "" {
			checksumURL, checksumErr := url.Parse(country.ChecksumURL)
			if checksumErr != nil || checksumURL.Scheme != "https" ||
				checksumURL.Host == "" || checksumURL.User != nil {
				return nil, fmt.Errorf("country %q checksum URL must use HTTPS", name)
			}
		}
	}
	return &cfg, nil
}

// yamlDecoder is kept local so downloader configuration remains strict without
// exposing YAML implementation details through the public package API.
func yamlDecoder(r io.Reader) *yaml.Decoder {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	return dec
}

func (c *Countries) Country(name string) (Country, error) {
	country, ok := c.Countries[name]
	if !ok {
		return Country{}, fmt.Errorf("unknown country %q", name)
	}
	return country, nil
}

func Download(ctx context.Context, options Options) (Result, error) {
	if options.Destination == "" {
		return Result{}, errors.New("download destination is empty")
	}
	client := options.Client
	if client == nil {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           dialer.DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				MaxIdleConns:          4,
				IdleConnTimeout:       90 * time.Second,
			},
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if request.URL.Scheme != "https" {
					return fmt.Errorf("refusing insecure redirect to %s", request.URL.Redacted())
				}
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				return nil
			},
		}
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 4 * time.Hour
	}
	destination := options.Destination
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return Result{}, fmt.Errorf("create download directory: %w", err)
	}
	partPath := destination + ".part"
	partMetaPath := partPath + ".meta"
	metaPath := destination + ".meta"
	if partInfo, err := os.Lstat(partPath); err == nil && !partInfo.Mode().IsRegular() {
		return Result{}, fmt.Errorf("temporary download path %q is not a regular file", partPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect temporary download path %q: %w", partPath, err)
	}

	remote, headErr := headMetadata(ctx, client, options.Definition.URL)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !options.Force {
		if info, err := os.Stat(destination); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			if err := validatePBF(destination); err == nil {
				local, _ := readMetadata(metaPath)
				result := Result{Path: destination, Bytes: info.Size()}
				result.RemoteETag, result.RemoteModTime = remote.ETag, remote.LastModified
				switch {
				case headErr == nil && isSameRepresentation(local, remote):
					result.Skipped, result.Current, result.CurrentKnown = true, true, true
					return result, nil
				case headErr == nil && hasValidator(remote):
					// A validator exists and differs (or the byte length changed).
				default:
					result.Skipped = true
					return result, nil
				}
			}
		}
	}

	partSize := int64(0)
	var partMetadata remoteMetadata
	if !options.Force {
		if info, err := os.Lstat(partPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			partSize = info.Size()
			partMetadata, _ = readMetadata(partMetaPath)
		}
	}
	if partSize > 0 && rangeValidator(partMetadata) == "" {
		partSize = 0
	}

	transferCtx, cancelTransfer := context.WithTimeout(ctx, timeout)
	defer cancelTransfer()
	req, err := http.NewRequestWithContext(transferCtx, http.MethodGet, options.Definition.URL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("create download request: %w", err)
	}
	if partSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", partSize))
		req.Header.Set("If-Range", rangeValidator(partMetadata))
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", options.Country, err)
	}
	start := int64(0)
	total, totalKnown, expectedBody := int64(-1), false, int64(0)
	switch resp.StatusCode {
	case http.StatusOK:
		// An If-Range mismatch is correctly served as a complete representation.
		// Open with truncation below; never append this response to an old partial.
		partSize = 0
		if resp.ContentLength >= 0 {
			total, totalKnown = resp.ContentLength, true
		}
	case http.StatusPartialContent:
		contentStart, contentEnd, contentTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
		expectedBody = contentEnd - contentStart + 1
		if partSize == 0 || !ok || contentStart != partSize ||
			(resp.ContentLength >= 0 && resp.ContentLength != expectedBody) ||
			(partMetadata.Size > 0 && contentTotal > 0 && partMetadata.Size != contentTotal) {
			resp.Body.Close()
			if err := removePartial(partPath, partMetaPath); err != nil {
				return Result{}, fmt.Errorf("discard invalid partial download: %w", err)
			}
			return downloadFresh(transferCtx, client, options, destination, partPath, partMetaPath, metaPath)
		}
		if validatorsConflict(partMetadata, metadataFromResponse(resp)) {
			resp.Body.Close()
			if err := removePartial(partPath, partMetaPath); err != nil {
				return Result{}, fmt.Errorf("discard changed partial download: %w", err)
			}
			return downloadFresh(transferCtx, client, options, destination, partPath, partMetaPath, metaPath)
		}
		start, total = partSize, contentTotal
		totalKnown = contentTotal >= 0
	case http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		if partSize == 0 {
			return Result{}, fmt.Errorf("download %s: server rejected an un-ranged request with HTTP 416", options.Country)
		}
		if err := removePartial(partPath, partMetaPath); err != nil {
			return Result{}, fmt.Errorf("discard rejected partial download: %w", err)
		}
		return downloadFresh(transferCtx, client, options, destination, partPath, partMetaPath, metaPath)
	default:
		resp.Body.Close()
		return Result{}, fmt.Errorf("download %s: unexpected HTTP status %s", options.Country, resp.Status)
	}
	return saveResponse(transferCtx, client, resp, options, destination, partPath, partMetaPath, metaPath, start, total, totalKnown, expectedBody)
}

func downloadFresh(ctx context.Context, client *http.Client, options Options, destination, partPath, partMetaPath, metaPath string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, options.Definition.URL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("restart download %s: %w", options.Country, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return Result{}, fmt.Errorf("restart download %s: unexpected HTTP status %s", options.Country, resp.Status)
	}
	total, known := resp.ContentLength, resp.ContentLength >= 0
	return saveResponse(ctx, client, resp, options, destination, partPath, partMetaPath, metaPath, 0, total, known, 0)
}

func saveResponse(ctx context.Context, client *http.Client, resp *http.Response, options Options, destination, partPath, partMetaPath, metaPath string, start, total int64, totalKnown bool, expectedBody int64) (Result, error) {
	defer resp.Body.Close()
	metadata := metadataFromResponse(resp)
	if totalKnown {
		metadata.Size = total
	}
	if err := writeMetadata(partMetaPath, metadata); err != nil {
		return Result{}, fmt.Errorf("write resumable download metadata: %w", err)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if start > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(partPath, flags, 0640)
	if err != nil {
		return Result{}, fmt.Errorf("open temporary download file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()

	counter := &countingReader{r: resp.Body}
	started := time.Now()
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	if options.Progress != nil {
		go func() {
			defer close(progressDone)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			previousBytes := int64(0)
			previousAt := time.Now()
			rate := progress.NewEMA(0.25)
			for {
				select {
				case <-stopProgress:
					return
				case now := <-ticker.C:
					current := counter.n.Load()
					deltaTime := now.Sub(previousAt).Seconds()
					smoothed := rate.Add(float64(current-previousBytes) / deltaTime)
					downloaded := start + current
					p := Progress{
						State: "Downloading", Downloaded: downloaded, Total: total,
						TotalKnown: totalKnown, SpeedBytes: smoothed,
						Elapsed: now.Sub(started), ResumedFrom: start,
					}
					if totalKnown && now.Sub(started) >= 2*time.Second && current > 0 {
						p.ETA, p.ETAKnown = progress.ETA(total-downloaded, smoothed)
					}
					options.Progress(p)
					previousBytes, previousAt = current, now
				}
			}
		}()
	} else {
		close(progressDone)
	}

	_, copyErr := io.Copy(out, counter)
	close(stopProgress)
	<-progressDone
	elapsed := time.Since(started)
	if copyErr != nil {
		syncErr := out.Sync()
		closeErr := out.Close()
		closed = true
		if syncErr != nil {
			return Result{}, errors.Join(fmt.Errorf("write downloaded snapshot: %w", copyErr),
				fmt.Errorf("sync resumable partial download: %w", syncErr),
				wrapOptionalError("close partial download", closeErr))
		}
		return Result{}, errors.Join(fmt.Errorf("write downloaded snapshot: %w", copyErr),
			wrapOptionalError("close partial download", closeErr))
	}
	if expectedBody > 0 && counter.n.Load() != expectedBody {
		closeErr := out.Close()
		closed = true
		cleanupErr := removePartial(partPath, partMetaPath)
		return Result{}, errors.Join(
			fmt.Errorf("partial response size mismatch: received %d bytes, expected %d from Content-Range", counter.n.Load(), expectedBody),
			wrapOptionalError("close invalid partial response", closeErr),
			wrapOptionalError("remove invalid partial response", cleanupErr),
		)
	}
	if err := out.Sync(); err != nil {
		closeErr := out.Close()
		closed = true
		return Result{}, errors.Join(fmt.Errorf("sync temporary download: %w", err),
			wrapOptionalError("close temporary download", closeErr))
	}
	if err := ctx.Err(); err != nil {
		closeErr := out.Close()
		closed = true
		return Result{}, errors.Join(err, wrapOptionalError("close resumable download", closeErr))
	}
	if err := out.Close(); err != nil {
		closed = true
		return Result{}, fmt.Errorf("close temporary download: %w", err)
	}
	closed = true
	finalSize := start + counter.n.Load()
	if totalKnown && finalSize != total {
		return Result{}, fmt.Errorf("download size mismatch: received %d bytes, expected %d", finalSize, total)
	}
	if finalSize <= 0 {
		return Result{}, errors.New("downloaded snapshot is empty")
	}
	metadata.Size = finalSize
	if options.Progress != nil {
		options.Progress(Progress{
			State: "Verifying", Downloaded: finalSize, Total: total,
			TotalKnown: totalKnown, Elapsed: elapsed, ResumedFrom: start,
		})
	}
	expectedMD5, checksumFound, err := fetchChecksum(ctx, client, options.Definition)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if checksumFound {
		actual, err := checksumFile(partPath, md5.New())
		if err != nil {
			return Result{}, fmt.Errorf("calculate downloaded snapshot checksum: %w", err)
		}
		if !strings.EqualFold(actual, expectedMD5) {
			return Result{}, fmt.Errorf("download checksum mismatch: expected %s, got %s", expectedMD5, actual)
		}
	}
	if err := validatePBF(partPath); err != nil {
		return Result{}, fmt.Errorf("downloaded file is not a readable OSM PBF: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(partPath, destination); err != nil {
		return Result{}, fmt.Errorf("atomically replace snapshot %q: %w", destination, err)
	}
	// Directory fsync is best effort: rename has already atomically installed a
	// verified snapshot, and reporting failure here would falsely imply that the
	// prior destination is still in place.
	_ = syncDirectory(filepath.Dir(destination))
	if err := writeMetadata(metaPath, metadata); err != nil {
		// The completed data is already durable. Losing the validator only means
		// that a later invocation cannot prove it is current.
		_ = os.Remove(metaPath)
	}
	_ = os.Remove(partMetaPath)
	if options.Progress != nil {
		speed := float64(0)
		if elapsed > 0 {
			speed = float64(counter.n.Load()) / elapsed.Seconds()
		}
		p := Progress{
			State: "Complete", Downloaded: finalSize, Total: total,
			TotalKnown: totalKnown, Elapsed: elapsed, ResumedFrom: start,
			SpeedBytes: speed, ETA: 0, ETAKnown: totalKnown,
		}
		options.Progress(p)
	}
	return Result{
		Path: destination, Bytes: finalSize, ResumedFrom: start,
		RemoteETag: metadata.ETag, RemoteModTime: metadata.LastModified,
		ChecksumAvailable: checksumFound, ChecksumVerified: checksumFound,
	}, nil
}

func headMetadata(ctx context.Context, client *http.Client, url string) (remoteMetadata, error) {
	headCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(headCtx, http.MethodHead, url, nil)
	if err != nil {
		return remoteMetadata{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return remoteMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return remoteMetadata{}, fmt.Errorf("metadata request returned %s", resp.Status)
	}
	metadata := metadataFromResponse(resp)
	return metadata, nil
}

func metadataFromResponse(resp *http.Response) remoteMetadata {
	return remoteMetadata{
		ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
		Size: resp.ContentLength,
	}
}

func hasValidator(metadata remoteMetadata) bool {
	return metadata.ETag != "" || metadata.LastModified != ""
}

func rangeValidator(metadata remoteMetadata) string {
	if metadata.ETag != "" && !strings.HasPrefix(strings.TrimSpace(metadata.ETag), "W/") {
		return metadata.ETag
	}
	return metadata.LastModified
}

func isSameRepresentation(local, remote remoteMetadata) bool {
	if !hasValidator(local) || !hasValidator(remote) {
		return false
	}
	validatorMatches := false
	switch {
	case local.ETag != "" && remote.ETag != "":
		validatorMatches = local.ETag == remote.ETag
	case local.LastModified != "" && remote.LastModified != "":
		validatorMatches = local.LastModified == remote.LastModified
	}
	return validatorMatches && (remote.Size < 0 || local.Size == remote.Size)
}

func validatorsConflict(old, current remoteMetadata) bool {
	if old.ETag != "" && current.ETag != "" && old.ETag != current.ETag {
		return true
	}
	return old.LastModified != "" && current.LastModified != "" && old.LastModified != current.LastModified
}

var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+|\*)$`)

func parseContentRange(value string) (start, end, total int64, ok bool) {
	match := contentRangePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, 0, 0, false
	}
	parsedStart, err1 := strconv.ParseInt(match[1], 10, 64)
	parsedEnd, err2 := strconv.ParseInt(match[2], 10, 64)
	start, end = parsedStart, parsedEnd
	if err1 != nil || err2 != nil || end < start {
		return 0, 0, 0, false
	}
	if match[3] == "*" {
		return start, end, -1, true
	}
	total, err := strconv.ParseInt(match[3], 10, 64)
	return start, end, total, err == nil && total > end
}

func readMetadata(path string) (remoteMetadata, error) {
	var metadata remoteMetadata
	data, err := os.ReadFile(path)
	if err != nil {
		return metadata, err
	}
	err = json.Unmarshal(data, &metadata)
	return metadata, err
}

func writeMetadata(path string, metadata remoteMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removePartial(paths ...string) error {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func fetchChecksum(ctx context.Context, client *http.Client, country Country) (string, bool, error) {
	url := country.ChecksumURL
	if url == "" {
		url = country.URL + ".md5"
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		// A missing checksum endpoint should not make a complete PBF download
		// unusable. Integrity is still checked by size and PBF header validation.
		return "", false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("checksum request returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", false, fmt.Errorf("read checksum response: %w", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 || len(fields[0]) != md5.Size*2 {
		return "", false, errors.New("checksum response does not contain an MD5 digest")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", false, fmt.Errorf("invalid checksum response: %w", err)
	}
	return fields[0], true, nil
}

func checksumFile(path string, h hash.Hash) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validatePBF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := osmpbf.New(context.Background(), f, 1)
	defer scanner.Close()
	if _, err := scanner.Header(); err != nil {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func HumanProgress(p Progress) string {
	percent := "Unknown"
	eta := "Unknown"
	if p.TotalKnown {
		if value, ok := progress.Percent(p.Downloaded, p.Total); ok {
			percent = fmt.Sprintf("%.1f%%", value)
		}
	}
	if p.ETAKnown {
		eta = progress.Duration(p.ETA)
	}
	total := "Unknown"
	if p.TotalKnown {
		total = progress.Bytes(p.Total)
	}
	return fmt.Sprintf("%s  %s\nDownloaded: %s / %s (%s)\nSpeed: %s/s  Elapsed: %s  Remaining: %s",
		p.State, progress.Bar(p.Downloaded, p.Total, 24),
		progress.Bytes(p.Downloaded), total, percent,
		progress.Bytes(int64(p.SpeedBytes)), progress.Duration(p.Elapsed), eta)
}
