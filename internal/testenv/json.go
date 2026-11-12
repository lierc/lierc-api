package testenv

import (
	"encoding/json"
	"reflect"
)

// SameJSON says whether a and b are the same JSON value. Production's
// connection.config is jsonb, which hands its text back normalised (spaces
// after colons, keys reordered), so config read from the database is
// compared as JSON, not as text.
func SameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
