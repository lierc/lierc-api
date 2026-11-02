package lctl

// Commands are lctl's subcommands, in the order help lists them.
var Commands = []Command{
	{Name: "user-info", Summary: "Show a user: email, last login, password storage, connections", Run: userInfo},
	{Name: "check-connections", Summary: "Check saved connections against the connection rules; --delete removes invalid ones", Run: checkConnections},
	{Name: "delete-users", Summary: "Delete users and all their data; dry run unless --delete", Run: deleteUsers},
	{Name: "delete-connections", Summary: "Delete connections and their data, keeping the users; dry run unless --delete", Run: deleteConnections},
	{Name: "reset", Summary: "Set a user's password (prompted, or one line on stdin)", Run: reset},
	{Name: "reconnect", Summary: "Restart a connection in liercd from its saved config", Run: reconnect},
	{Name: "revive", Summary: "Start saved connections liercd isn't running, for recently active users", Run: revive},
	{Name: "stats", Summary: "Open event streams per user", Run: stats},
	{Name: "users", Summary: "Open event streams for users (default all)", Run: users},
	{Name: "prune-seen", Summary: "Count or delete seen rows for deleted connections or not updated in a month", Run: pruneSeen},
	{Name: "prune-logs", Summary: "Count or delete log rows for deleted connections", Run: pruneLogs},
}
