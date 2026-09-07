package crash

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

var fileMu sync.Mutex

// AttachLog mirrors the standard logger into logs/runtime.log. It is useful for
// GUI builds on Windows where there is no console attached.
func AttachLog(root string) func() {
	if root == "" {
		root = "."
	}
	logDir := filepath.Join(root, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	path := filepath.Join(logDir, "runtime.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Printf("SAKR TUN started")
	return func() {
		log.Printf("SAKR TUN stopped")
		_ = f.Close()
	}
}

// Recover writes any Go panic to logs/crash.log. This does not hide the crash;
// it only makes GUI builds debuggable when Windows closes the app silently.
func Recover(root string) {
	if v := recover(); v != nil {
		Write(root, "panic", v)
	}
}

// Go starts a goroutine with panic logging. Use this for goroutines owned by the
// app so background failures do not vanish without a stack trace.
func Go(root string, fn func()) {
	go func() {
		defer Recover(root)
		fn()
	}()
}

func Write(root, kind string, value any) {
	if root == "" {
		root = "."
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	debug.SetTraceback("all")
	logDir := filepath.Join(root, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	path := filepath.Join(logDir, "crash.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "\n===== %s =====\n", time.Now().Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(f, "%s: %v\n\n", kind, value)
	_, _ = f.Write(debug.Stack())
	_, _ = f.WriteString("\n")
}
