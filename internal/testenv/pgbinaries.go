package testenv

import (
	"os"
	"path/filepath"
	"syscall"
)

// pgBinariesMarker is written once a start from the directory has
// succeeded, so a directory a killed run left half unpacked isn't used.
const pgBinariesMarker = ".complete"

// pgBinaries returns the directory embedded Postgres keeps its binaries in:
// one per version under root, shared by every test binary, so the archive
// is unpacked once per machine instead of once per package (the unpacking
// runs in Go and takes about 20s under -race). When the directory isn't
// marked complete, pgBinaries empties it and returns holding an exclusive
// lock, so other test binaries wait while embedded-postgres unpacks into it;
// release(true) marks it complete, and either release unlocks it.
func pgBinaries(root, version string) (dir string, release func(ok bool), err error) {
	dir = filepath.Join(root, "postgres-"+version)
	marker := filepath.Join(dir, pgBinariesMarker)
	if _, err := os.Stat(marker); err == nil {
		return dir, func(bool) {}, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	lock, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return "", nil, err
	}
	unlock := func() {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}
	// another test binary may have finished while this one waited
	if _, err := os.Stat(marker); err == nil {
		unlock()
		return dir, func(bool) {}, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		unlock()
		return "", nil, err
	}
	return dir, func(ok bool) {
		if ok {
			os.WriteFile(marker, nil, 0o644)
		}
		unlock()
	}, nil
}

// pgBinariesRoot is where pgBinaries keeps its directories: the user's
// cache directory, or the temp directory when there is none.
func pgBinariesRoot() string {
	if c, err := os.UserCacheDir(); err == nil {
		return filepath.Join(c, "lierc-api-tests")
	}
	return filepath.Join(os.TempDir(), "lierc-api-tests")
}
