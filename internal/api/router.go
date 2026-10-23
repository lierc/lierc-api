package api

import (
	"regexp"
	"strings"
)

// route is one entry of lib/API/Routes.pm. An empty Method matches any
// method (Router::Boom::Method with undef). Public routes skip the login check
// (API::Dispatch::handle).
type route struct {
	Method, Pattern, Name string
	Public                bool
	H                     Handler
}

// routes lists lib/API/Routes.pm in order. Handlers are filled in by later
// tasks; a nil handler answers 404 not found.
var routes = []route{
	{Method: "GET", Pattern: "/health", Name: "auth.health", H: authHealth},
	{Method: "GET", Pattern: "/auth", Name: "auth.show", H: authShow},
	{Method: "POST", Pattern: "/auth", Name: "auth.login", Public: true, H: authLogin},
	{Method: "POST", Pattern: "/register", Name: "auth.register", Public: true, H: authRegister},
	{Method: "", Pattern: "/logout", Name: "auth.logout", Public: true, H: authLogout},
	{Method: "GET", Pattern: "/token", Name: "auth.token", H: authToken},
	{Method: "GET", Pattern: "/verify", Name: "auth.verify", H: authVerify},

	{Method: "GET", Pattern: "/log/:event", Name: "message.log", H: messageLog},
	{Method: "GET", Pattern: "/missed", Name: "message.missed", H: messageMissed},
	{Method: "GET", Pattern: "/seen", Name: "message.seen", H: messageSeen},
	{Method: "GET", Pattern: "/highlight", Name: "message.highlight", H: messageHighlight},
	{Method: "GET", Pattern: "/highlight/:event", Name: "message.highlight", H: messageHighlight},

	{Method: "GET", Pattern: "/preference", Name: "pref.list"},
	{Method: "GET", Pattern: "/preference/:pref", Name: "pref.show"},
	{Method: "POST", Pattern: "/preference/:pref", Name: "pref.upsert"},
	{Method: "GET", Pattern: "/unsubscribe", Name: "pref.unsubscribe"},

	{Method: "GET", Pattern: "/connection", Name: "connection.list", H: connectionList},
	{Method: "POST", Pattern: "/connection", Name: "connection.create", H: connectionCreate},
	{Method: "GET", Pattern: "/connection/:id", Name: "connection.show", H: connectionShow},
	{Method: "PUT", Pattern: "/connection/:id", Name: "connection.edit", H: connectionEdit},
	{Method: "DELETE", Pattern: "/connection/:id", Name: "connection.delete", H: connectionDelete},
	{Method: "POST", Pattern: "/connection/:id", Name: "connection.send", H: connectionSend},

	{Method: "DELETE", Pattern: "/connection/:id/nick/:nick", Name: "private.delete", H: privateDelete},
	{Method: "POST", Pattern: "/connection/:id/nick/:nick", Name: "private.create", H: privateCreate},
	{Method: "GET", Pattern: "/privates", Name: "private.list", H: privateList},

	{Method: "GET", Pattern: "/connection/:id/channel/:channel/date/:from/:to", Name: "channel.date", H: channelDate},
	{Method: "GET", Pattern: "/connection/:id/channel/:channel/events", Name: "channel.logs", H: channelLogs},
	{Method: "GET", Pattern: "/connection/:id/channel/:channel/events/:event", Name: "channel.logs_id", H: channelLogsID},
	{Method: "POST", Pattern: "/connection/:id/channel/:channel/seen", Name: "channel.set_seen", H: channelSetSeen},
	{Method: "GET", Pattern: "/connection/:id/channel/:channel/last", Name: "channel.last", H: channelLast},
	{Method: "GET", Pattern: "/connection/:id/channel", Name: "channel.list", H: channelList},

	{Method: "POST", Pattern: "/connection/:id/channel/:channel/ignore", Name: "ignore.create", H: ignoreCreate},
	{Method: "DELETE", Pattern: "/connection/:id/channel/:channel/ignore/:from", Name: "ignore.delete", H: ignoreDelete},
	{Method: "GET", Pattern: "/ignore", Name: "ignore.list", H: ignoreList},

	{Method: "GET", Pattern: "/notification/web_push_keys", Name: "webpush.keys"},
	{Method: "GET", Pattern: "/notification/web_push", Name: "webpush.list"},
	{Method: "POST", Pattern: "/notification/web_push", Name: "webpush.upsert"},
	{Method: "DELETE", Pattern: "/notification/web_push/{endpoint:.+}", Name: "webpush.delete"},

	{Method: "GET", Pattern: "/notification/apn/config", Name: "apn.config"},
	{Method: "POST", Pattern: "/notification/apn/device", Name: "apn.device"},
	{Method: "POST", Pattern: "/v1/pushPackages/:push_id", Name: "apn.package", Public: true},
	{Method: "POST", Pattern: "/v1/log", Name: "apn.log", Public: true},
	{Method: "POST", Pattern: "/v1/devices/:device_id/registrations/:push_id", Name: "apn.register", Public: true},
	{Method: "DELETE", Pattern: "/v1/devices/:device_id/registrations/:push_id", Name: "apn.unregister", Public: true},

	{Method: "POST", Pattern: "/image", Name: "image.create"},
	{Method: "GET", Pattern: "/image", Name: "image.list"},
	{Method: "DELETE", Pattern: "/image/{url:.*}", Name: "image.delete"},
}

type compiledRoute struct {
	route
	re    *regexp.Regexp
	names []string
}

type router []compiledRoute

// placeholder matches Router::Boom's `:name` and `{name:regex}` forms.
var placeholder = regexp.MustCompile(`:([A-Za-z0-9_]+)|\{([A-Za-z0-9_]+):([^{}]+)\}`)

func compile(table []route) router {
	rt := make(router, len(table))
	for i, r := range table {
		var sb strings.Builder
		var names []string
		sb.WriteString(`\A`)
		last := 0
		for _, m := range placeholder.FindAllStringSubmatchIndex(r.Pattern, -1) {
			sb.WriteString(regexp.QuoteMeta(r.Pattern[last:m[0]]))
			if m[2] >= 0 {
				names = append(names, r.Pattern[m[2]:m[3]])
				sb.WriteString(`([^/]+)`)
			} else {
				names = append(names, r.Pattern[m[4]:m[5]])
				sb.WriteString(`(` + r.Pattern[m[6]:m[7]] + `)`)
			}
			last = m[1]
		}
		sb.WriteString(regexp.QuoteMeta(r.Pattern[last:]))
		sb.WriteString(`\z`)
		rt[i] = compiledRoute{route: r, re: regexp.MustCompile(sb.String()), names: names}
	}
	return rt
}

// match returns the first route matching method and the decoded path, and its
// captures. A path that matches only with another method is no match, as in
// Router::Boom::Method.
func (rt router) match(method, path string) (*compiledRoute, map[string]string) {
	for i := range rt {
		r := &rt[i]
		if r.Method != "" && r.Method != method {
			continue
		}
		m := r.re.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		caps := make(map[string]string, len(r.names))
		for j, n := range r.names {
			caps[n] = m[j+1]
		}
		return r, caps
	}
	return nil, map[string]string{}
}
