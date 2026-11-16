package testenv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPgBinariesClearsIncomplete(t *testing.T) {
	root := t.TempDir()
	// a run killed mid-unpack leaves files but no marker
	stale := filepath.Join(root, "postgres-17.5.0", "bin", "pg_ctl")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, []byte("half"), 0o755)

	dir, release, err := pgBinaries(root, "17.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "postgres-17.5.0") {
		t.Errorf("dir = %q", dir)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("incomplete unpack left in place: %v", err)
	}
	release(false)
	if _, err := os.Stat(filepath.Join(dir, pgBinariesMarker)); !os.IsNotExist(err) {
		t.Error("a failed start marked the binaries complete")
	}
}

func TestPgBinariesReused(t *testing.T) {
	root := t.TempDir()
	dir, release, err := pgBinaries(root, "17.5.0")
	if err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "bin", "pg_ctl")
	os.MkdirAll(filepath.Dir(keep), 0o755)
	os.WriteFile(keep, []byte("x"), 0o755)
	release(true)

	again, release2, err := pgBinaries(root, "17.5.0")
	if err != nil {
		t.Fatal(err)
	}
	release2(true)
	if again != dir {
		t.Errorf("second dir = %q, want %q", again, dir)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("complete binaries removed: %v", err)
	}
}

// Another test binary unpacking holds the lock; a second one waits for it
// instead of unpacking into the same directory.
func TestPgBinariesWaitsForUnpack(t *testing.T) {
	root := t.TempDir()
	_, release, err := pgBinaries(root, "17.5.0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		_, r, err := pgBinaries(root, "17.5.0")
		if err == nil {
			r(true)
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("second caller didn't wait (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	release(true)
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second caller still waiting after release")
	}
}
