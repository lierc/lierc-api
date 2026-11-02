package lctl

// Commands are lctl's subcommands, in the order help lists them.
var Commands = []Command{
	{Name: "check-connections", Summary: "Check saved connections against the connection rules; --delete removes invalid ones", Run: checkConnections},
}
