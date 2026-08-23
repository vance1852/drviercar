package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/httpapi"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/testsupport"
)

type recordingWriter struct {
	mu      sync.Mutex
	content bytes.Buffer
}

func (w *recordingWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.content.Write(payload)
}

func (w *recordingWriter) records(t *testing.T) []map[string]any {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(w.content.String()), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
			t.Fatalf("access log line is not valid JSON: %q: %v", trimmed, err)
		}
		if record["msg"] == "http request" {
			records = append(records, record)
		}
	}
	return records
}

// TestAccessLogScopeDoesNotLeakBetweenRequests drives two calls through the HTTP
// stack and checks that the access log of one request never carries fields that
// belong to another request.
func TestAccessLogScopeDoesNotLeakBetweenRequests(t *testing.T) {
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	defer func() { _ = harness.Close() }()
	if _, err := harness.SeedActors(context.Background()); err != nil {
		t.Fatalf("seed actors: %v", err)
	}

	sink := &recordingWriter{}
	router := httpapi.NewRouter(httpapi.Dependencies{
		Auth:           harness.Auth,
		Fleet:          harness.Fleet,
		DataLoop:       harness.DataLoop,
		Store:          harness.Store,
		Clock:          harness.Clock,
		Logger:         logging.New(sink, logging.LevelInfo),
		RequestTimeout: 5 * time.Second,
	})
	server := httptest.NewServer(router.Handler())
	defer server.Close()

	filtered, err := server.Client().Get(server.URL + "/api/v1/version?city=jiading")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_ = filtered.Body.Close()
	plain, err := server.Client().Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	_ = plain.Body.Close()

	records := sink.records(t)
	if len(records) != 2 {
		t.Fatalf("two access log records expected, got %d", len(records))
	}
	first, second := records[0], records[1]
	if first["path"] != "/api/v1/version" {
		t.Fatalf("the first record must describe its own route, got %v", first["path"])
	}
	if first["query"] != "city=jiading" {
		t.Fatalf("the first record must carry its own query, got %v", first["query"])
	}
	if second["path"] != "/healthz" {
		t.Fatalf("the second record must describe its own route, got %v", second["path"])
	}
	if _, leaked := second["query"]; leaked {
		t.Fatalf("a request without a query string must not inherit another request's query: %v", second["query"])
	}

	sink.mu.Lock()
	sink.content.Reset()
	sink.mu.Unlock()

	routes := []string{"/healthz", "/readyz", "/api/v1/version", "/healthz", "/readyz", "/api/v1/version"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, route := range routes {
		wg.Add(1)
		go func(route string) {
			defer wg.Done()
			<-start
			response, requestErr := server.Client().Get(server.URL + route)
			if requestErr != nil {
				return
			}
			_ = response.Body.Close()
		}(route)
	}
	close(start)
	wg.Wait()

	concurrent := sink.records(t)
	if len(concurrent) != len(routes) {
		t.Fatalf("every concurrent request must produce one record, got %d of %d",
			len(concurrent), len(routes))
	}
	allowed := map[string]bool{"/healthz": true, "/readyz": true, "/api/v1/version": true}
	for _, record := range concurrent {
		path, ok := record["path"].(string)
		if !ok || !allowed[path] {
			t.Fatalf("a concurrent access log record carries an unexpected route: %v", record["path"])
		}
		if record["method"] != http.MethodGet {
			t.Fatalf("a concurrent access log record carries an unexpected method: %v", record["method"])
		}
		if _, leaked := record["query"]; leaked {
			t.Fatalf("no concurrent request used a query string, but a record carries one: %v", record["query"])
		}
	}
}
