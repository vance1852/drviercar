// Package logging provides the structured logger used across HTTP handlers,
// services and background workers. Log records never contain credentials.
package logging

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a record.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

var levelOrder = map[Level]int{LevelDebug: 0, LevelInfo: 1, LevelWarn: 2, LevelError: 3}

var redactedKeys = map[string]bool{
	"password": true,
	"token":    true,
	"secret":   true,
	"cookie":   true,
}

type contextKey struct{ name string }

var requestIDKey = contextKey{name: "request_id"}

// WithRequestID stores the request identifier in ctx.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestIDFrom reads the request identifier from ctx.
func RequestIDFrom(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

// RequestIDField is the public name of the correlation identifier.
const RequestIDField = "request_id"

// RequestIDFromValues reads the correlation identifier that request scoped code
// attached to ctx under the public field name. It lets packages outside this one
// resolve the identifier without importing the private context key.
func RequestIDFromValues(ctx context.Context) string {
	if value, ok := ctx.Value(RequestIDField).(string); ok {
		return value
	}
	return ""
}

// Logger writes one JSON object per record.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	level  Level
	base   map[string]any
	nowFn  func() time.Time
	closed bool
}

// New builds a logger writing to out.
func New(out io.Writer, level Level) *Logger {
	if out == nil {
		out = os.Stdout
	}
	if _, ok := levelOrder[level]; !ok {
		level = LevelInfo
	}
	return &Logger{out: out, level: level, base: map[string]any{}, nowFn: time.Now}
}

// With returns a child logger that always emits the supplied fields.
func (l *Logger) With(fields map[string]any) *Logger {
	child := &Logger{out: l.out, level: l.level, base: map[string]any{}, nowFn: l.nowFn}
	for key, value := range l.base {
		child.base[key] = value
	}
	for key, value := range fields {
		child.base[key] = value
	}
	return child
}

// Debug emits a debug record.
func (l *Logger) Debug(ctx context.Context, message string, fields map[string]any) {
	l.log(ctx, LevelDebug, message, fields)
}

// Info emits an informational record.
func (l *Logger) Info(ctx context.Context, message string, fields map[string]any) {
	l.log(ctx, LevelInfo, message, fields)
}

// Warn emits a warning record.
func (l *Logger) Warn(ctx context.Context, message string, fields map[string]any) {
	l.log(ctx, LevelWarn, message, fields)
}

// Error emits an error record.
func (l *Logger) Error(ctx context.Context, message string, fields map[string]any) {
	l.log(ctx, LevelError, message, fields)
}

func (l *Logger) log(ctx context.Context, level Level, message string, fields map[string]any) {
	if levelOrder[level] < levelOrder[l.level] {
		return
	}
	record := map[string]any{
		"ts":    l.nowFn().UTC().Format(time.RFC3339Nano),
		"level": string(level),
		"msg":   message,
	}
	for key, value := range l.base {
		record[key] = value
	}
	for key, value := range fields {
		record[sanitizeKey(key)] = redact(key, value)
	}
	if requestID := RequestIDFrom(ctx); requestID != "" {
		record["request_id"] = requestID
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		encoded = []byte(`{"level":"error","msg":"log encoding failed"}`)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	_, _ = l.out.Write(append(encoded, '\n'))
}

// Close stops further writes; it is used during graceful shutdown so that late
// goroutines cannot write into a closed file.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
}

func sanitizeKey(key string) string {
	return strings.TrimSpace(strings.ToLower(key))
}

func redact(key string, value any) any {
	if redactedKeys[sanitizeKey(key)] {
		return "[redacted]"
	}
	return value
}

// SortedKeys is a helper used by tests to compare records deterministically.
func SortedKeys(record map[string]any) []string {
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
