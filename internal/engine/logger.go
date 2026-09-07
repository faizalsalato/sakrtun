package engine

import (
	"fmt"
	"sync"
	"time"
)

type LogEntry struct {
	ID      int64  `json:"id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type Logger struct {
	mu      sync.Mutex
	nextID  int64
	entries []LogEntry
}

func NewLogger() *Logger { return &Logger{} }

// Add records a log entry. It is safe to call on a nil *Logger, which makes it
// convenient for optional logging in helpers that may receive no logger.
func (l *Logger) Add(level, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	entry := LogEntry{ID: l.nextID, Time: time.Now().Format("15:04:05"), Level: level, Message: fmt.Sprintf(format, args...)}
	l.entries = append(l.entries, entry)
	if len(l.entries) > 600 {
		l.entries = l.entries[len(l.entries)-600:]
	}
}

func (l *Logger) Since(id int64) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LogEntry, 0)
	for _, e := range l.entries {
		if e.ID > id {
			out = append(out, e)
		}
	}
	return out
}

// Clear removes all stored entries. The ID counter is kept monotonic so
// polling clients do not receive old entries again.
func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}
