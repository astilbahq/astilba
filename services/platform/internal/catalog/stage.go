package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
)

// Find returns the catalog entry with the ID.
func Find(id string) (Recipe, bool) {
	for _, r := range Recipes() {
		if r.ID == id {
			return r, true
		}
	}
	return Recipe{}, false
}

// Stage is what a person chooses when staging an entry.
type Stage struct {
	// Name replaces the entry's name in its service and volume names, so one environment can
	// hold the entry twice.
	Name string
	// Existing names, by a Secret's placeholder ID, an existing secret version to use instead
	// of generating one. The caller checks the version belongs to the environment.
	Existing map[uuid.UUID]uuid.UUID
	Route    *StageRoute
}

// StageRoute is where an entry's route publishes. Domain is either a domain the person
// controls or, when empty, the generated application domain the caller computes for the new
// service. AllowCIDRs is required: see Recipe.Route.
type StageRoute struct {
	Domain     string
	TLS        string
	AllowCIDRs []string
}

// Generated is a secret version staging creates. Its value comes from Value.
type Generated struct {
	VersionID uuid.UUID
	// Name is the stored secret's name: the service's name and the secret's.
	Name   string
	Format string
}

// Value returns a new random value in the secret's format.
func (g Generated) Value() (string, error) {
	if g.Format == "hex" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(b[:]), nil
	}
	return randtoken.New()
}

// Staged is an entry under new identities, ready to add to a draft.
type Staged struct {
	// Specification holds only the entry's own resources.
	Specification domain.Specification
	Generated     []Generated
	// Identities maps each placeholder application, volume and binding to its new ID.
	Identities map[uuid.UUID]uuid.UUID
}

// Stage copies the entry under fresh identities. hostname returns the generated application
// domain of the new service a route targets, for a route with no domain of its own.
func (r Recipe) Stage(in Stage, hostname func(service uuid.UUID) (string, error)) (Staged, error) {
	out := Staged{Identities: map[uuid.UUID]uuid.UUID{}}
	name, err := domain.Name(in.Name)
	if err != nil {
		return out, err
	}
	spec := r.Specification
	ids := out.Identities
	for _, a := range spec.Applications {
		ids[a.ID] = uuid.New()
	}
	for _, v := range spec.Volumes {
		ids[v.ID] = uuid.New()
	}
	for _, b := range spec.Bindings {
		ids[b.ID] = uuid.New()
	}
	for id := range in.Existing {
		if !slices.ContainsFunc(r.Secrets, func(s Secret) bool { return s.ID == id }) {
			return out, domain.Invalid("secrets")
		}
	}
	rename := func(old string) (string, error) {
		next, ok := strings.CutPrefix(old, r.Name)
		if !ok {
			return "", domain.Invalid("name")
		}
		return domain.Name(name + next)
	}
	services := map[uuid.UUID]string{}
	for _, a := range spec.Applications {
		if services[a.ID], err = rename(a.Name); err != nil {
			return out, err
		}
	}
	secrets := map[uuid.UUID]uuid.UUID{}
	for _, s := range r.Secrets {
		if existing := in.Existing[s.ID]; existing != uuid.Nil {
			secrets[s.ID] = existing
			continue
		}
		g := Generated{VersionID: uuid.New(), Format: s.Format}
		if g.Name, err = domain.Name(services[s.ServiceID] + " · " + s.Name); err != nil {
			return out, err
		}
		secrets[s.ID] = g.VersionID
		out.Generated = append(out.Generated, g)
	}
	if out.Specification, err = domain.RemapSpecification(spec, ids, secrets, "catalog"); err != nil {
		return out, err
	}
	s := &out.Specification
	for i := range s.Applications {
		a := &s.Applications[i]
		a.Name = services[r.Specification.Applications[i].ID]
	}
	for i := range s.Volumes {
		if s.Volumes[i].Name, err = rename(s.Volumes[i].Name); err != nil {
			return out, err
		}
	}
	if (r.Route == nil) != (in.Route == nil) {
		return out, domain.Invalid("route")
	}
	if r.Route != nil {
		target := ids[r.Route.ServiceID]
		route := domain.Route{ID: uuid.New(), TargetID: target, Protocol: "http", Domain: in.Route.Domain, Port: r.Route.Port, TLS: in.Route.TLS, AllowCIDRs: in.Route.AllowCIDRs}
		// The allowlist is what keeps a new instance's first sign-up to its owner.
		if err = domain.ValidateOwnerAllowlist(route.AllowCIDRs); err != nil {
			return out, err
		}
		if route.Domain == "" {
			if route.Domain, err = hostname(target); err != nil {
				return out, err
			}
		}
		if err = domain.ValidateRoute(route); err != nil {
			return out, err
		}
		scheme := map[string]string{"managed": "https", "off": "http"}[route.TLS]
		origin := scheme + "://" + route.Domain
		for i := range s.Applications {
			if s.Applications[i].ID == target {
				for _, v := range r.Route.OriginVariables {
					s.Applications[i].Variables = append(s.Applications[i].Variables, domain.Variable{Name: v, Value: &origin})
				}
			}
		}
		s.Routes = []domain.Route{route}
	}
	return out, nil
}
