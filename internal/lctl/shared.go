package lctl

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lierc/lierc-api/internal/connconfig"
)

func statusLine(code int) string { return fmt.Sprintf("%d %s", code, http.StatusText(code)) }

// connectionProblem is why a saved config is invalid, or "".
func connectionProblem(ctx context.Context, env *Env, raw []byte, dns bool) string {
	v, err := connconfig.Decode(raw)
	if _, ok := v.(map[string]any); err != nil || !ok {
		return "config isn't a JSON object"
	}
	config, err := connconfig.Clean(raw)
	if err != nil {
		return err.Error()
	}
	if !dns {
		return ""
	}
	if err := connconfig.CheckHost(ctx, env.Getenv, env.Resolve, config["Host"].(string), config["Port"].(int)); err != nil {
		return err.Error()
	}
	return ""
}

// hostPort is a saved config's "Host:Port" as stored, "?" for a missing or
// non-scalar part, or "?" if it isn't an object.
func hostPort(raw string) string {
	v, _ := connconfig.Decode([]byte(raw))
	params, ok := v.(map[string]any)
	if !ok {
		return "?"
	}
	part := func(k string) string {
		if s, ok := connconfig.Display(params[k]); ok {
			return s
		}
		return "?"
	}
	return part("Host") + ":" + part("Port")
}
