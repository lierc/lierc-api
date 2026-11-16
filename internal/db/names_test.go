package db_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

// embeddedMigrations lists the files in migrations/ as Migrate records them.
func embeddedMigrations(t *testing.T) []applied {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var out []applied
	for _, e := range entries {
		num, rest, _ := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		v, err := strconv.Atoi(num)
		if err != nil {
			t.Fatalf("migration file %q", e.Name())
		}
		out = append(out, applied{v, rest})
	}
	return out
}

var (
	newTables = []string{"users", "connections", "logins", "prefs", "logs", "log_archives",
		"web_push_subscriptions", "seen_markers", "private_chats"}
	oldName = regexp.MustCompile(`^(user|connection|login|pref|seen|private|web_push|log_archive|log)(_|$)`)
)

// isOld reports whether a table, index, sequence or constraint name still
// uses one of the old singular table names.
func isOld(name string) bool {
	for _, t := range newTables {
		if name == t || strings.HasPrefix(name, t+"_") {
			return false
		}
	}
	return oldName.MatchString(name)
}

// After the migrations, nothing in the schema is named after the old
// singular tables or the old "user"/connection columns.
func TestNoOldNames(t *testing.T) {
	d := testenv.Postgres(t)
	rows, err := d.Pool.Query(ctx, `
		SELECT 'relation ' || relname, relname FROM pg_class
		 WHERE relnamespace = 'public'::regnamespace AND relkind IN ('r', 'i', 'S', 'v')
		UNION ALL
		SELECT 'constraint ' || conname, conname FROM pg_constraint
		 WHERE connamespace = 'public'::regnamespace
		UNION ALL
		SELECT format('column %s.%s', c.relname, a.attname), a.attname
		  FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r'
		   AND a.attnum > 0 AND NOT a.attisdropped AND a.attname IN ('user', 'connection')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var what, name string
		if err := rows.Scan(&what, &name); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(what, "column ") || isOld(name) {
			t.Errorf("old name: %s", what)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range newTables {
		if !tableExists(t, d, name) {
			t.Errorf("no table %s", name)
		}
	}
}

func TestIsOld(t *testing.T) {
	for name, want := range map[string]bool{
		"user": true, "user_pkey": true, "log_id_seq": true, "log_archive_pkey": true, "login_user": true,
		"users_pkey": false, "logs_id_seq": false, "log_archives_pkey": false, "logins_user_id_idx": false,
		"schema_migrations_pkey": false, "seen_markers_pkey": false, "private_chats_pkey": false,
	} {
		if got := isOld(name); got != want {
			t.Errorf("isOld(%q) = %v, want %v", name, got, want)
		}
	}
}
