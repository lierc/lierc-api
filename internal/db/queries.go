package db

import (
	"embed"
	"regexp"
)

// sql holds the channel read queries, shared with the Perl API
// (lib/API/Queries.pm reads the same files).
//
//go:embed sql/*.sql
var sqlFiles embed.FS

var commentLine = regexp.MustCompile(`(?m)^[ \t]*--[^\n]*\n?`)

// Query is the SQL of sql/<name>.sql with whole-line "--" comments
// dropped, as API::Queries does, so the placeholders they mention are
// never counted. It panics for an unknown name: the names are constants.
func Query(name string) string {
	b, err := sqlFiles.ReadFile("sql/" + name + ".sql")
	if err != nil {
		panic("unknown query " + name)
	}
	return commentLine.ReplaceAllString(string(b), "")
}
