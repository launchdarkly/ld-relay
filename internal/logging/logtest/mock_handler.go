// Package logtest provides test utilities for slog-based logging.
package logtest

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Entry represents a captured log record.
type Entry struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// MockHandler is a slog.Handler that captures log records for test assertions.
// It is safe for concurrent use. Entries are shared across handlers created via
// WithAttrs/WithGroup so that the original handler can observe all log records.
type MockHandler struct {
	mu      *sync.Mutex
	entries *[]Entry
	level   *slog.LevelVar
	attrs   []slog.Attr
	group   string
}

// NewMockHandler creates a new MockHandler that captures all log records at Debug level and above.
func NewMockHandler() *MockHandler {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	h := &MockHandler{
		mu:      &sync.Mutex{},
		entries: &[]Entry{},
		level:   lv,
	}
	return h
}

// NewMockLogger returns a *slog.Logger backed by a MockHandler, plus the handler for inspection.
func NewMockLogger() (*slog.Logger, *MockHandler) {
	h := NewMockHandler()
	return slog.New(h), h
}

func (h *MockHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}


func (h *MockHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs[key] = a.Value.Any()
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	*h.entries = append(*h.entries, Entry{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   attrs,
	})
	return nil
}

func (h *MockHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &MockHandler{
		mu:      h.mu,
		entries: h.entries,
		level:   h.level,
		attrs:   newAttrs,
		group:   h.group,
	}
}

func (h *MockHandler) WithGroup(name string) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &MockHandler{
		mu:      h.mu,
		entries: h.entries,
		level:   h.level,
		attrs:   h.attrs,
		group:   g,
	}
}

// Entries returns a copy of all captured log entries.
func (h *MockHandler) Entries() []Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]Entry, len(*h.entries))
	copy(result, *h.entries)
	return result
}

// EntriesForLevel returns captured entries at the specified level.
func (h *MockHandler) EntriesForLevel(level slog.Level) []Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	var result []Entry
	for _, e := range *h.entries {
		if e.Level == level {
			result = append(result, e)
		}
	}
	return result
}

// Messages returns all captured messages at the specified level.
func (h *MockHandler) Messages(level slog.Level) []string {
	entries := h.EntriesForLevel(level)
	msgs := make([]string, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	return msgs
}

// AllMessages returns all captured messages across all levels.
func (h *MockHandler) AllMessages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	msgs := make([]string, len(*h.entries))
	for i, e := range *h.entries {
		msgs[i] = e.Message
	}
	return msgs
}

// HasMessage returns true if any captured entry at the given level contains the substring.
func (h *MockHandler) HasMessage(level slog.Level, substr string) bool {
	for _, msg := range h.Messages(level) {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// SetLevel sets the minimum log level for the handler.
func (h *MockHandler) SetLevel(level slog.Level) {
	h.level.Set(level)
}

// Reset clears all captured entries.
func (h *MockHandler) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.entries = nil
}
