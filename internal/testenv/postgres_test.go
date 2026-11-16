package testenv

import (
	"context"
	"testing"
)

// The template database is built by the migrations. log_archive.messages
// asks for lz4, which production and the dev stack's Postgres have; the
// embedded Postgres the tests run is built without it, so the template
// leaves the compression method out.
func TestTemplateIsMigrated(t *testing.T) {
	d := Postgres(t)
	ctx := context.Background()
	var versions []int
	rows, err := d.Pool.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(versions) == 0 || versions[0] != 1 {
		t.Errorf("schema_migrations versions = %v, want them to start at 1", versions)
	}
	var compression string
	err = d.Pool.QueryRow(ctx, `SELECT attcompression::text FROM pg_attribute
		WHERE attrelid = 'log_archives'::regclass AND attname = 'messages'`).Scan(&compression)
	if err != nil {
		t.Fatal(err)
	}
	if compression != "" {
		t.Errorf("log_archives.messages compression = %q, want none", compression)
	}
}
