package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type requestIDContextKey struct{}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

type poiDebugRecorder struct {
	dir string
	mu  sync.Mutex
}

type poiDebugRecord struct {
	RequestID  string            `json:"request_id,omitempty"`
	Operation  string            `json:"operation"`
	Timestamp  time.Time         `json:"timestamp"`
	Attempt    int               `json:"attempt"`
	DurationMS int64             `json:"duration_ms"`
	Request    poiDebugRequest   `json:"request"`
	Response   *poiDebugResponse `json:"response,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type poiDebugRequest struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers,omitempty"`
}

type poiDebugResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	Body       string      `json:"body"`
}

func (d *poiDebugRecorder) record(operation string, entry poiDebugRecord) error {
	if d == nil {
		return nil
	}
	filename := ""
	switch operation {
	case "search":
		filename = "poi-search.json"
	case "details":
		filename = "poi-details.json"
	default:
		return fmt.Errorf("unknown POI debug operation %q", operation)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := os.MkdirAll(d.dir, 0700); err != nil {
		return fmt.Errorf("create POI debug directory: %w", err)
	}
	for _, name := range []string{"poi-search.json", "poi-details.json"} {
		if err := ensureDebugFile(filepath.Join(d.dir, name)); err != nil {
			return err
		}
	}
	path := filepath.Join(d.dir, filename)
	records := make([]poiDebugRecord, 0, 1)
	if contents, err := os.ReadFile(path); err == nil {
		if len(contents) > 0 {
			if err := json.Unmarshal(contents, &records); err != nil {
				return fmt.Errorf("read existing POI debug file %q: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read POI debug file %q: %w", path, err)
	}
	records = append(records, entry)
	contents, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode POI debug record: %w", err)
	}
	contents = append(contents, '\n')

	tmp, err := os.CreateTemp(d.dir, "."+filename+"-*")
	if err != nil {
		return fmt.Errorf("create temporary POI debug file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write POI debug file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync POI debug file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close POI debug file: %w", err)
	}
	if err = os.Chmod(tmpPath, 0600); err != nil {
		return fmt.Errorf("secure POI debug file: %w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace POI debug file: %w", err)
	}
	return nil
}

func ensureDebugFile(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create POI debug file %q: %w", path, err)
	}
	if _, err = file.Write([]byte("[]\n")); err != nil {
		_ = file.Close()
		return fmt.Errorf("initialize POI debug file %q: %w", path, err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close POI debug file %q: %w", path, err)
	}
	return nil
}
