package domain

import (
	"github.com/google/uuid"
	"testing"
)

func TestGeneratedRouteIdentity(t *testing.T) {
	ws, env, app, server := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	host := ApplicationHostname("apps.example.test", ws, env, app, server)
	spec := Specification{Routes: []Route{{TargetID: app, Domain: host}}}
	if len(host) > 253 || !ValidHostname(host) {
		t.Fatal("invalid generated DNS name", host)
	}
	cases := []struct {
		name            string
		ws, env, server uuid.UUID
		host            string
		valid           bool
	}{
		{"own", ws, env, server, host, true},
		{"other workspace", uuid.New(), env, server, host, false},
		{"other environment", ws, uuid.New(), server, host, false},
		{"other server", ws, env, uuid.New(), host, false},
		{"custom domain", ws, env, server, "app.customer.test", true},
		{"base reservation", ws, env, server, "apps.example.test", false},
		{"forged namespace", ws, env, server, "app.apps.example.test", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec.Routes[0].Domain = c.host
			_, e := GeneratedRouteServers("apps.example.test", c.ws, c.env, spec, &c.server)
			if (e == nil) != c.valid {
				t.Fatal(e)
			}
		})
	}
}
