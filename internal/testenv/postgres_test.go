package testenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// lierc.sql compresses log_archive.messages with lz4, which production and
// the dev stack's Postgres have; the embedded Postgres the tests run is built
// without it, so the schema it loads leaves the compression method out.
func TestSchemaWithoutLZ4(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "lierc.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "compression lz4") {
		t.Fatal("lierc.sql doesn't ask for lz4")
	}
	s, err := schema()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "lz4") {
		t.Error("the test schema still asks for lz4")
	}
	if !strings.Contains(s, "messages    json not null") {
		t.Error("the test schema lost the messages column")
	}
}
