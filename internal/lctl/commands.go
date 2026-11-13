package lctl

// Commands are lctl's top-level commands, in the order help lists them.
// These use the database directly.
var Commands = []Command{
	{Name: "revive", Summary: "Start saved connections liercd isn't running, for recently active users", Run: revive, NeedsDB: true},
	{Name: "delete-orphan-logs", Summary: "Find or delete log rows of deleted connections and users", Run: deleteOrphanLogs, NeedsDB: true},
	{Name: "archive", Summary: "Move old log rows into compressed archive blocks; dry run unless --move", Run: archiveCmd, NeedsDB: true},
}

// AdminCommands are `lctl admin` commands, in the order `lctl admin help`
// lists them. They go through the API's admin server and liercd.
var AdminCommands = []Command{
	{Name: "user-info", Summary: "Show a user: email, last login, password storage, connections", Run: userInfo},
	{Name: "inactive-users", Summary: "List users who haven't logged in within --since (e.g. 90d)", Run: inactiveUsers},
	{Name: "stop-inactive", Summary: "Stop the running liercd connections of users who haven't logged in within --since; dry run unless --stop", Run: stopInactive},
	{Name: "connection-info", Summary: "Show a connection: owner, saved config, validity, and its live state in liercd", Run: connectionInfo},
	{Name: "connection-list", Summary: "List connections, one line each: id, instance, owner, host:port, live state; --instance main|next filters", Run: connectionList},
	{Name: "delete-users", Summary: "Delete users and all their data; dry run unless --delete", Run: deleteUsers},
	{Name: "delete-connections", Summary: "Delete connections and their data, keeping the users; dry run unless --delete", Run: deleteConnections},
	{Name: "logins", Summary: "List a user's logins: id, started, last used, IP, browser", Run: logins},
	{Name: "end-login", Summary: "End one of a user's logins by id, or all with --all", Run: endLogin},
	{Name: "prune-logins", Summary: "Delete logins unused for 7 days", Run: pruneLogins},
	{Name: "reconnect", Summary: "Restart connections in liercd from their saved configs", Run: reconnect},
	{Name: "stats", Summary: "Open event streams per user", Run: stats},
	{Name: "users", Summary: "Open event streams for users (default all)", Run: users},
	{Name: "prune-seen", Summary: "Count or delete seen rows for deleted connections or not updated in a month", Run: pruneSeen},
}
