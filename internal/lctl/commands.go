package lctl

// Commands are lctl's subcommands, in the order help lists them.
var Commands = []Command{
	{Name: "user-info", Summary: "Show a user: email, last login, password storage, connections", Run: userInfo},
	{Name: "inactive-users", Summary: "List users who haven't logged in within --since (e.g. 90d)", Run: inactiveUsers},
	{Name: "stop-inactive", Summary: "Stop the running liercd connections of users who haven't logged in within --since; dry run unless --stop", Run: stopInactive},
	{Name: "connection-info", Summary: "Show a connection: owner, saved config, validity, and its live state in liercd", Run: connectionInfo},
	{Name: "connection-list", Summary: "List connections, one line each: id, instance, owner, host:port, live state; --instance main|next filters", Run: connectionList},
	{Name: "fix-config-encoding", Summary: "Repair saved configs whose non-ASCII text the Perl API stored encoded twice; dry run unless --fix", Run: fixConfigEncoding},
	{Name: "check-connections", Summary: "Check saved connections against the connection rules; --delete removes invalid ones", Run: checkConnections},
	{Name: "delete-users", Summary: "Delete users and all their data; dry run unless --delete", Run: deleteUsers},
	{Name: "delete-connections", Summary: "Delete connections and their data, keeping the users; dry run unless --delete", Run: deleteConnections},
	{Name: "reset", Summary: "Set a user's password (prompted, or one line on stdin)", Run: reset},
	{Name: "reconnect", Summary: "Restart connections in liercd from their saved configs", Run: reconnect},
	{Name: "move-connection", Summary: "Move a connection to another liercd instance (main or next)", Run: moveConnection},
	{Name: "move-connections", Summary: "Move every connection on one liercd instance to the other, in paced batches; dry run unless --move", Run: moveConnections},
	{Name: "revive", Summary: "Start saved connections liercd isn't running, for recently active users", Run: revive},
	{Name: "stats", Summary: "Open event streams per user", Run: stats},
	{Name: "users", Summary: "Open event streams for users (default all)", Run: users},
	{Name: "prune-seen", Summary: "Count or delete seen rows for deleted connections or not updated in a month", Run: pruneSeen},
	{Name: "delete-orphan-logs", Summary: "Find or delete log rows of deleted connections and users", Run: deleteOrphanLogs},
	{Name: "archive", Summary: "Move old log rows into compressed archive blocks; dry run unless --move", Run: archiveCmd},
}
