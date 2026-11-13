package db

// ConnectionChange is a change to a connection, announced to the owner's
// event streams. Action is create, update or delete.
type ConnectionChange struct {
	User       string `json:"user"`
	Connection string `json:"connection"`
	Action     string `json:"action"`
}
