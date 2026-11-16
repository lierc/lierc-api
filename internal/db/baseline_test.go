package db_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func withoutLZ4(s string) string { return strings.ReplaceAll(s, " compression lz4", "") }

// baseline.sql marks a database that already has 0001_initial's schema
// (production, made by hand) as having had migration 1: afterwards Migrate
// applies nothing, and schema_migrations is the table Migrate would make.
func TestProductionBaseline(t *testing.T) {
	raw, err := os.ReadFile("baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The test database has a generated name and the test connects as
	// postgres, so it runs what follows the guard.
	_, body, ok := strings.Cut(string(raw), "-- end of guard\n")
	if !ok {
		t.Fatal(`baseline.sql has no "-- end of guard" line`)
	}
	initial, err := os.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}

	d := testenv.EmptyPostgres(t)
	if _, err := d.Pool.Exec(ctx, withoutLZ4(string(initial))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, body); err != nil {
		t.Fatal(err)
	}
	var l logRec
	if err := d.Migrate(ctx, db.MigrateOptions{Rewrite: withoutLZ4, Logf: l.Logf}); err != nil {
		t.Fatal(err)
	}
	if n := l.count("applying"); n != 0 {
		t.Errorf("Migrate applied something after the baseline: %q", l.lines)
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "initial"}) {
		t.Errorf("recorded %v", got)
	}

	fresh := testenv.EmptyPostgres(t)
	if err := fresh.Migrate(ctx, db.MigrateOptions{Rewrite: withoutLZ4}); err != nil {
		t.Fatal(err)
	}
	if a, b := columns(t, d), columns(t, fresh); a != b {
		t.Errorf("baseline's schema_migrations:\n%s\nMigrate's:\n%s", a, b)
	}
}

// columns describes schema_migrations's columns and primary key.
func columns(t *testing.T, d *db.DB) string {
	t.Helper()
	var s string
	err := d.Pool.QueryRow(ctx, `SELECT string_agg(format('%s %s %s %s', column_name, data_type, is_nullable,
		coalesce(column_default, '-')), E'\n' ORDER BY ordinal_position)
		|| E'\n' || (SELECT pg_get_constraintdef(oid) FROM pg_constraint
		             WHERE conrelid = 'schema_migrations'::regclass AND contype = 'p')
		FROM information_schema.columns WHERE table_name = 'schema_migrations'`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// productionOnly matches the dump's lines a test database can't take or
// doesn't need: extensions the embedded Postgres lacks, owners (no lierc
// role), lz4 (not built in), transaction_timeout (Postgres 17+) and the
// empty search_path, which would stay set on the pool's connection.
var productionOnly = regexp.MustCompile(`(?m)^(CREATE EXTENSION|COMMENT ON EXTENSION|ALTER SCHEMA|SET transaction_timeout|SELECT pg_catalog.set_config\('search_path'|.*OWNER TO |.*SET COMPRESSION lz4).*$`)

// baselineBody is what follows baseline.sql's guard.
func baselineBody(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(raw), "-- end of guard\n")
	if !ok {
		t.Fatal(`baseline.sql has no "-- end of guard" line`)
	}
	return body
}

// Production's schema (testdata/production-schema.sql, its pg_dump) after
// baseline.sql is exactly what Migrate makes on an empty database, so later
// migrations meet the schema they were written against.
func TestBaselineMatchesProduction(t *testing.T) {
	dump, err := os.ReadFile("testdata/production-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	prod := testenv.EmptyPostgres(t)
	if _, err := prod.Pool.Exec(ctx, productionOnly.ReplaceAllString(string(dump), "")); err != nil {
		t.Fatal(err)
	}
	if _, err := prod.Pool.Exec(ctx, baselineBody(t)); err != nil {
		t.Fatal(err)
	}
	var l logRec
	if err := prod.Migrate(ctx, db.MigrateOptions{Rewrite: withoutLZ4, Logf: l.Logf}); err != nil {
		t.Fatal(err)
	}
	if n := l.count("applying"); n != 0 {
		t.Errorf("Migrate applied something after the baseline: %q", l.lines)
	}

	fresh := testenv.EmptyPostgres(t)
	if err := fresh.Migrate(ctx, db.MigrateOptions{Rewrite: withoutLZ4}); err != nil {
		t.Fatal(err)
	}
	if a, b := schema(t, prod), schema(t, fresh); a != b {
		t.Errorf("production after the baseline differs from Migrate's schema:\n%s", lineDiff(a, b))
	}
}

// schema describes the public schema's columns, constraints and indexes,
// one sorted line each.
func schema(t *testing.T, d *db.DB) string {
	t.Helper()
	var s string
	err := d.Pool.QueryRow(ctx, `SELECT string_agg(line, E'\n' ORDER BY line) FROM (
		SELECT format('column %s.%s %s%s default=%s identity=%s', c.relname, a.attname,
		       format_type(a.atttypid, a.atttypmod), CASE WHEN a.attnotnull THEN ' not null' ELSE '' END,
		       coalesce(pg_get_expr(ad.adbin, ad.adrelid), '-'), a.attidentity) AS line
		  FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		 WHERE n.nspname = 'public' AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
		UNION ALL
		SELECT format('constraint %s %s %s', conrelid::regclass, conname, pg_get_constraintdef(oid))
		  FROM pg_constraint WHERE connamespace = 'public'::regnamespace
		UNION ALL
		SELECT format('index %s', indexdef) FROM pg_indexes WHERE schemaname = 'public'
	) l`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// lineDiff lists the lines only in a ("-") and only in b ("+").
func lineDiff(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for _, l := range strings.Split(a, "\n") {
		if !mb[l] {
			out = append(out, "- "+l)
		}
	}
	for _, l := range strings.Split(b, "\n") {
		if !ma[l] {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}
