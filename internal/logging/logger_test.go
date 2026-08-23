package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/vance1852/drviercar/internal/logging"
)

func TestWithDoesNotMutateParentFields(t *testing.T) {
	var buf bytes.Buffer
	parent := logging.New(&buf, logging.LevelInfo)

	first := parent.With(map[string]any{"route": "/a", "query": "x=1"})
	first.Info(context.Background(), "first", nil)

	// A second scoped logger with a different route and no query must not
	// inherit the query value stored by the first scope, nor must it overwrite
	// the first scope's route.
	second := parent.With(map[string]any{"route": "/b"})
	second.Info(context.Background(), "second", nil)

	// The parent must be untouched too.
	parent.Info(context.Background(), "parent", nil)

	records := records(t, &buf)
	if got := records[0]["route"]; got != "/a" {
		t.Fatalf("first record route = %v, want /a", got)
	}
	if got, ok := records[0]["query"]; !ok || got != "x=1" {
		t.Fatalf("first record query = %v, want x=1", got)
	}
	if got := records[1]["route"]; got != "/b" {
		t.Fatalf("second record route = %v, want /b", got)
	}
	if _, ok := records[1]["query"]; ok {
		t.Fatalf("second record must not carry query, got %v", records[1]["query"])
	}
	if _, ok := records[2]["route"]; ok {
		t.Fatalf("parent record must not carry route, got %v", records[2]["route"])
	}
}

func TestWithConcurrentRequestsDoNotCorruptEachOther(t *testing.T) {
	var buf bytes.Buffer
	parent := logging.New(&buf, logging.LevelInfo)

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			ctx := logging.WithRequestID(context.Background(), "req")
			scoped := parent.With(map[string]any{
				"route": "/r",
				"query": "n=" + itoa(n),
			})
			scoped.Info(ctx, "http request", nil)
		}(i)
	}
	wg.Wait()

	records := records(t, &buf)
	if len(records) != goroutines {
		t.Fatalf("expected %d records, got %d", goroutines, len(records))
	}
	seen := make(map[string]bool, goroutines)
	for _, r := range records {
		if r["route"] != "/r" {
			t.Fatalf("record route = %v, want /r", r["route"])
		}
		q, ok := r["query"].(string)
		if !ok || !strings.HasPrefix(q, "n=") {
			t.Fatalf("record query = %v, want n=<n>", r["query"])
		}
		if seen[q] {
			t.Fatalf("duplicate query %q — requests bled into each other", q)
		}
		seen[q] = true
	}
	if len(seen) != goroutines {
		t.Fatalf("only %d unique queries, want %d", len(seen), goroutines)
	}
}

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, bytes.Count(buf.Bytes(), []byte{'\n'}))
	dec := json.NewDecoder(buf)
	for {
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode record: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var sign string
	if n < 0 {
		sign, n = "-", -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return sign + string(digits)
}
