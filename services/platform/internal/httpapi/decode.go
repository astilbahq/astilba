package httpapi

import (
	"encoding/json/v2"
	"net/http"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// decode is the one reader of request bodies, which the requestbody analyzer enforces, so it is
// where every body is kept clear of NUL, which PostgreSQL cannot store: refused, naming the
// field, from a person or program, and replaced from an agent, whose strings carry its host's
// output, so that a host cannot stop its own agent from reporting.
func decode[T any](r *http.Request) (T, error) {
	var v T
	if json.UnmarshalRead(r.Body, &v, json.RejectUnknownMembers(true)) != nil {
		return v, domain.Invalid("request")
	}
	if agentReported(r) {
		// Only a map holding two keys a replacement would make equal is refused.
		if field, collides := domain.ReplaceNUL(&v); collides {
			return v, domain.Invalid(field)
		}
	} else if field, found := domain.NULField(v, ""); found {
		return v, domain.Invalid(field)
	}
	return v, nil
}

// agentReported is whether an agent sent r: one authenticated as an agent, or one of the
// requests an agent makes to obtain its credentials.
func agentReported(r *http.Request) bool {
	return agentIdentity(r.Context()).ID != uuid.Nil || agentSelfAuthenticated(r.URL.Path)
}

// urlNUL refuses a path or query that decodes to NUL: their values reach queries as text, which
// cannot hold it. Only the escape %00 decodes to one, since the server refuses a control
// character in the request line. No parameter is text today; this covers one added later.
func urlNUL(r *http.Request) error {
	switch {
	case strings.ContainsRune(r.URL.Path, 0):
		return domain.Invalid("path")
	case strings.Contains(r.URL.RawQuery, "%00"):
		return domain.Invalid("query")
	}
	return nil
}
