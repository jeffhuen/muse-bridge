package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTemp(t *testing.T, maxBytes int64, backups int) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	w, err := Open(path, maxBytes, backups)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w, path
}

func TestInvalidArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	if _, err := Open(path, 0, 1); err == nil {
		t.Fatal("maxBytes=0 accepted")
	}
	if _, err := Open(path, 100, -1); err == nil {
		t.Fatal("backups=-1 accepted")
	}
}

func TestAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 1024, 1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old\nnew\n" {
		t.Fatalf("content=%q", got)
	}
}

func TestRotatesAndKeepsBackups(t *testing.T) {
	w, path := openTemp(t, 100, 2)
	for i := 0; i < 7; i++ {
		fmt.Fprintf(w, "line-%02d-12345678901234567890\n", i) // 29 bytes each
	}
	dir := filepath.Dir(path)
	for _, name := range []string{"test.log", "test.log.1", "test.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "test.log.3")); !os.IsNotExist(err) {
		t.Fatal("test.log.3 exists, want only 2 backups")
	}
	active, _ := os.ReadFile(path)
	if !strings.Contains(string(active), "line-06") {
		t.Fatalf("active file lacks newest line: %q", active)
	}
	oldest, _ := os.ReadFile(filepath.Join(dir, "test.log.2"))
	if !strings.Contains(string(oldest), "line-00") {
		t.Fatalf("oldest backup lacks early line: %q", oldest)
	}
}

func TestTotalSizeStaysCapped(t *testing.T) {
	w, path := openTemp(t, 100, 2)
	chunk := []byte(strings.Repeat("x", 50))
	for i := 0; i < 100; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	var total int64
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		info, _ := e.Info()
		total += info.Size()
	}
	// 3 files × (100 cap + one 50-byte overhang at most).
	if total > 3*(100+50) {
		t.Fatalf("total=%d, want ≤450", total)
	}
}

func TestConcurrentWrites(t *testing.T) {
	w, _ := openTemp(t, 1024, 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				fmt.Fprintf(w, "g%d-%d\n", i, j)
			}
		}(i)
	}
	wg.Wait()
}
