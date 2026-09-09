// Package logfile provides a size-capped log file: when a write would
// exceed maxBytes, the active file rotates (path.1, path.2, ...),
// keeping backups files. Semantics mirror Python's RotatingFileHandler
// so both daemons produce the same layout. Rotation failure never fails
// a write: logging must not crash the daemon.
package logfile

import (
	"fmt"
	"os"
	"sync"
)

// Writer is a mutex-guarded rotating log file, safe for concurrent use.
// Construct with Open; Close releases the handle (needed on Windows,
// where open files cannot be deleted).
type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

// Open creates or appends to the log file at path.
func Open(path string, maxBytes int64, backups int) (*Writer, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("logfile: maxBytes must be positive")
	}
	if backups < 0 {
		return nil, fmt.Errorf("logfile: backups must be non-negative")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{path: path, maxBytes: maxBytes, backups: backups, file: f, size: st.Size()}, nil
}

// Write appends p, rotating first when the write would exceed maxBytes.
// Writes are unbuffered: every Write is a syscall, so no flush or Sync
// discipline is needed.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close releases the active file handle.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

// rotate shifts backups (dropping the oldest) and reopens a fresh active
// file. Errors are best-effort: the previous handle stays usable when
// the reopen fails, so at worst the cap is exceeded, never data lost.
func (w *Writer) rotate() {
	w.file.Close()
	if w.backups > 0 {
		os.Remove(numbered(w.path, w.backups))
		for i := w.backups - 1; i >= 1; i-- {
			os.Rename(numbered(w.path, i), numbered(w.path, i+1))
		}
		os.Rename(w.path, numbered(w.path, 1))
	} else {
		os.Remove(w.path)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		if f, err = os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			return
		}
		st, _ := f.Stat()
		w.file, w.size = f, st.Size()
		return
	}
	w.file, w.size = f, 0
}

func numbered(path string, i int) string {
	return fmt.Sprintf("%s.%d", path, i)
}
