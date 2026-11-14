package api

import (
	"io"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

const invalidUTF8Body = `{"status":"error","error":"request body isn't valid UTF-8"}`

// Bodies that aren't valid UTF-8 are refused, whether raw or a form field
// percent-encoded.
func TestFormInvalidUTF8(t *testing.T) {
	e := setup(t)
	id := testenv.AddUser(t, e.db, "alice")
	e.setPassword(id, "hunter2")

	res := e.doReq("POST", "/auth", "", "email=alice%40example.com&pass=%FF", "Content-Type", formType)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 400 || strings.TrimSpace(string(b)) != invalidUTF8Body {
		t.Errorf("form with %%FF: %d %s", res.StatusCode, b)
	}

	a := testenv.AddUser(t, e.db, "bob")
	e.sendWant("POST", "/preference/theme", a, "\xff", 400, invalidUTF8Body)
}
