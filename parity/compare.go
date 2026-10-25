package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
)

// Result is one server's answer to one step.
type Result struct {
	Status      int
	ContentType string
	Body        []byte
}

// AllowKind says how an allowed path may differ.
type AllowKind int

const (
	// AnyValue: the values may differ, but their JSON types must match. For
	// an object or array, its contents are not compared.
	AnyValue AllowKind = iota
	// SameFormat: strings must match once every run of digits is collapsed
	// to "9", e.g. timestamps taken at different moments.
	SameFormat
	// Pattern: both values must be strings matching Allow.Pattern.
	Pattern
)

// Allow relaxes the comparison of the JSON values at Path. Path is "$" for
// the whole body, ".key" for an object member and "[*]" for any array index,
// e.g. "$[*].time" or "$.extra[*]". ".*" matches any one object member. The
// keys present must still match exactly.
type Allow struct {
	Path    string
	Kind    AllowKind
	Pattern string
	Why     string
}

func anyValue(path, why string) Allow   { return Allow{Path: path, Kind: AnyValue, Why: why} }
func sameFormat(path, why string) Allow { return Allow{Path: path, Kind: SameFormat, Why: why} }
func pattern(path, re, why string) Allow {
	return Allow{Path: path, Kind: Pattern, Pattern: re, Why: why}
}

var digitRuns = regexp.MustCompile(`[0-9]+`)

// Compare diffs two results: status, Content-Type and body. JSON bodies are
// compared by meaning (key order ignored, value types strict); event-stream
// bodies are compared event by event with each data line parsed as JSON;
// anything else byte for byte. It returns one line per difference.
func Compare(perl, goRes Result, allows []Allow) []string {
	var diffs []string
	if perl.Status != goRes.Status {
		diffs = append(diffs, fmt.Sprintf("status: perl %d, go %d", perl.Status, goRes.Status))
	}
	if perl.ContentType != goRes.ContentType {
		diffs = append(diffs, fmt.Sprintf("Content-Type: perl %q, go %q", perl.ContentType, goRes.ContentType))
	}
	pv, perr := decodeBody(perl)
	gv, gerr := decodeBody(goRes)
	switch {
	case perr == nil && gerr == nil:
		c := comparer{allows: compileAllows(allows)}
		c.value("$", pv, gv)
		diffs = append(diffs, c.diffs...)
	case !bytes.Equal(perl.Body, goRes.Body):
		diffs = append(diffs, fmt.Sprintf("body: perl %s, go %s", clip(perl.Body), clip(goRes.Body)))
	}
	return diffs
}

// decodeBody returns the body as a JSON value: a JSON body as itself, an
// event stream as [{"event":…,"data":…,"id":…}], and an error for anything
// else (including an empty body).
func decodeBody(r Result) (any, error) {
	if len(r.Body) == 0 {
		return nil, fmt.Errorf("empty")
	}
	if strings.HasPrefix(r.ContentType, "text/event-stream") {
		var out []any
		for _, ev := range ParseSSE(r.Body) {
			m := map[string]any{"event": ev.Event, "id": ev.ID}
			if d, err := decodeJSON([]byte(ev.Data)); err == nil {
				m["data"] = d
			} else {
				m["data"] = ev.Data
			}
			out = append(out, m)
		}
		return out, nil
	}
	return decodeJSON(r.Body)
}

func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("trailing data")
	}
	return v, nil
}

// Event is one Server-Sent Event. Comment lines (such as the stream's
// padding) are returned as events with Comment set.
type Event struct {
	Event, Data, ID string
	Comment         string
}

// ParseSSE splits a complete event-stream body into events.
func ParseSSE(b []byte) []Event {
	var out []Event
	var cur Event
	open := false
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case line == "":
			if open {
				out = append(out, cur)
			}
			cur, open = Event{}, false
		case strings.HasPrefix(line, ":"):
			out = append(out, Event{Comment: line})
		default:
			k, v, _ := strings.Cut(line, ":")
			v = strings.TrimPrefix(v, " ")
			open = true
			switch k {
			case "event":
				cur.Event = v
			case "data":
				cur.Data = v
			case "id":
				cur.ID = v
			}
		}
	}
	if open {
		out = append(out, cur)
	}
	return out
}

type compiledAllow struct {
	Allow
	path *regexp.Regexp
	re   *regexp.Regexp
}

func compileAllows(allows []Allow) []compiledAllow {
	out := make([]compiledAllow, len(allows))
	for i, a := range allows {
		p := regexp.QuoteMeta(a.Path)
		p = strings.ReplaceAll(p, `\[\*\]`, `\[[0-9]+\]`)
		p = strings.ReplaceAll(p, `\.\*`, `\.[^.\[]*`)
		out[i] = compiledAllow{Allow: a, path: regexp.MustCompile("^" + p + "$")}
		if a.Kind == Pattern {
			out[i].re = regexp.MustCompile(a.Pattern)
		}
	}
	return out
}

type comparer struct {
	allows []compiledAllow
	diffs  []string
}

func (c *comparer) errorf(path, f string, a ...any) {
	c.diffs = append(c.diffs, path+": "+fmt.Sprintf(f, a...))
}

func (c *comparer) allowFor(path string) *compiledAllow {
	for i := range c.allows {
		if c.allows[i].path.MatchString(path) {
			return &c.allows[i]
		}
	}
	return nil
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func (c *comparer) value(path string, p, g any) {
	pt, gt := typeName(p), typeName(g)
	if pt != gt {
		c.errorf(path, "type: perl %s %s, go %s %s", pt, short(p), gt, short(g))
		return
	}
	if a := c.allowFor(path); a != nil {
		switch a.Kind {
		case AnyValue:
			return // the types matched; for containers, contents are not checked
		case SameFormat:
			if ps, ok := p.(string); ok {
				gs := g.(string)
				if digitRuns.ReplaceAllString(ps, "9") != digitRuns.ReplaceAllString(gs, "9") {
					c.errorf(path, "format: perl %q, go %q", ps, gs)
				}
			}
			return
		case Pattern:
			ps, _ := p.(string)
			gs, _ := g.(string)
			if !a.re.MatchString(ps) {
				c.errorf(path, "perl %q does not match %s", ps, a.Pattern)
			}
			if !a.re.MatchString(gs) {
				c.errorf(path, "go %q does not match %s", gs, a.Pattern)
			}
			return
		}
	}
	switch pv := p.(type) {
	case nil:
	case bool:
		if pv != g.(bool) {
			c.errorf(path, "perl %v, go %v", pv, g)
		}
	case string:
		if pv != g.(string) {
			c.errorf(path, "perl %q, go %q", pv, g)
		}
	case json.Number:
		if !numbersEqual(pv, g.(json.Number)) {
			c.errorf(path, "perl %s, go %s", pv, g)
		}
	case []any:
		gv := g.([]any)
		if len(pv) != len(gv) {
			c.errorf(path, "length: perl %d %s, go %d %s", len(pv), short(pv), len(gv), short(gv))
			return
		}
		for i := range pv {
			c.value(fmt.Sprintf("%s[%d]", path, i), pv[i], gv[i])
		}
	case map[string]any:
		gv := g.(map[string]any)
		for _, k := range sortedKeys(pv, gv) {
			pe, pok := pv[k]
			ge, gok := gv[k]
			switch {
			case !gok:
				c.errorf(path, "key %q only in perl (%s)", k, short(pe))
			case !pok:
				c.errorf(path, "key %q only in go (%s)", k, short(ge))
			default:
				c.value(path+"."+k, pe, ge)
			}
		}
	}
}

// numbersEqual compares JSON numbers by value: 1 and 1.0 mean the same to a
// JavaScript client.
func numbersEqual(a, b json.Number) bool {
	if a == b {
		return true
	}
	x, ok1 := new(big.Float).SetString(string(a))
	y, ok2 := new(big.Float).SetString(string(b))
	return ok1 && ok2 && x.Cmp(y) == 0
}

func sortedKeys(ms ...map[string]any) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range ms {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func short(v any) string {
	b, _ := json.Marshal(v)
	return clip(b)
}

func clip(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// Normalize replaces each variable's value with "{{name}}" throughout body,
// so that generated ids captured in each server's own run compare equal
// wherever they appear (values, object keys, error text). The values are
// generated ids and tokens, whose JSON encoding is the value itself. Longer
// values are replaced first.
func Normalize(body []byte, vars map[string]string) []byte {
	type kv struct{ k, v string }
	var list []kv
	for k, v := range vars {
		if len(v) >= 4 { // short values would match by accident
			list = append(list, kv{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if len(list[i].v) != len(list[j].v) {
			return len(list[i].v) > len(list[j].v)
		}
		return list[i].k < list[j].k
	})
	pairs := make([]string, 0, 2*len(list))
	for _, e := range list {
		pairs = append(pairs, e.v, "{{"+e.k+"}}")
	}
	if len(pairs) == 0 {
		return body
	}
	return []byte(strings.NewReplacer(pairs...).Replace(string(body)))
}
