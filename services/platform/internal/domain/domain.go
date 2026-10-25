package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/google/uuid"
)

type Error struct {
	Code, Message string
	Fields        []string
}

func (e *Error) Error() string { return e.Message }
func Invalid(field string) error {
	return &Error{Code: "invalid_input", Message: "Check the identified fields.", Fields: []string{field}}
}

var Forbidden = &Error{Code: "forbidden", Message: "This action is not permitted."}
var NotFound = &Error{Code: "not_found", Message: "The requested resource was not found."}
var Unauthenticated = &Error{Code: "unauthenticated", Message: "Sign in to continue."}
var Credentials = &Error{Code: "invalid_credentials", Message: "The credentials could not be verified."}
var MFARequired = &Error{Code: "mfa_required", Message: "Set up two-factor authentication before changing this workspace."}
var RecentRequired = &Error{Code: "recent_auth_required", Message: "Confirm your credentials before this action."}
var Conflict = &Error{Code: "conflict", Message: "The resource changed. Review the current version before saving."}
var RateLimited = &Error{Code: "rate_limited", Message: "Too many attempts. Try again later."}

func Name(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 80 || strings.ContainsAny(value, "\r\n\x00") {
		return "", Invalid("name")
	}
	return value, nil
}
func Email(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	a, err := mail.ParseAddress(value)
	if err != nil || a.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", Invalid("email")
	}
	return value, nil
}
func Password(value string) error {
	if len(value) > 512 || utf8.RuneCountInString(value) < 15 || utf8.RuneCountInString(value) > 128 {
		return Invalid("password")
	}
	return nil
}
func CanWrite(role string) bool  { return role == "owner" || role == "operator" }
func ValidRole(role string) bool { return CanWrite(role) || role == "viewer" }

type ApplicationSource struct {
	ArtifactID *uuid.UUID `json:"artifactId,omitempty"`
	Kind       string     `json:"kind"`
	Image      *string    `json:"image,omitempty"`
	Provider   *string    `json:"provider,omitempty"`
	Repository *string    `json:"repository,omitempty"`
	Ref        *string    `json:"ref,omitempty"`
}
type Application struct {
	Outputs   []ServiceOutput   `json:"outputs,omitempty"`
	Networks  []uuid.UUID       `json:"networks,omitempty"`
	ID        uuid.UUID         `json:"id"`
	Name      string            `json:"name"`
	Source    ApplicationSource `json:"source"`
	Kind      string            `json:"kind,omitempty"`
	Variables []Variable        `json:"variables,omitempty"`
	Mounts    []VolumeMount     `json:"mounts,omitempty"`
	DependsOn []uuid.UUID       `json:"dependsOn,omitempty"`
	Health    *HealthCheck      `json:"health,omitempty"`
	Postgres  *Postgres         `json:"postgres,omitempty"`
	Command   []string          `json:"command,omitempty"`
}
type Variable struct {
	Name            string     `json:"name"`
	Value           *string    `json:"value,omitempty"`
	SecretVersionID *uuid.UUID `json:"secretVersionId,omitempty"`
}
type Specification struct {
	Compose      *composeproject.Bundle `json:"compose,omitempty"`
	Networks     []Network              `json:"networks,omitempty"`
	Version      int                    `json:"version"`
	Applications []Application          `json:"applications"`
	Variables    []Variable             `json:"variables"`
	Volumes      []Volume               `json:"volumes,omitempty"`
	Bindings     []Binding              `json:"bindings,omitempty"`
	Routes       []Route                `json:"routes,omitempty"`
}
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

var variableName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,79}$`)

func ParseSpec(raw []byte) (Specification, []byte, error) {
	var spec Specification
	if len(raw) > 131072 {
		return spec, nil, Invalid("specification")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&spec) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || spec.Version != 1 || spec.Applications == nil || spec.Variables == nil || len(spec.Applications) > 50 || len(spec.Variables) > 100 {
		return spec, nil, Invalid("specification")
	}
	if spec.Compose != nil {
		if err := deriveCompose(&spec); err != nil {
			return spec, nil, err
		}
	} else if len(spec.Networks) > 0 {
		return spec, nil, Invalid("networks")
	}
	ids := map[uuid.UUID]bool{}
	names := map[string]bool{}
	for _, app := range spec.Applications {
		name, err := Name(app.Name)
		if err != nil || name != app.Name || app.ID == uuid.Nil || ids[app.ID] || names[strings.ToLower(name)] {
			return spec, nil, Invalid("specification.applications")
		}
		ids[app.ID] = true
		names[strings.ToLower(name)] = true
		s := app.Source
		switch s.Kind {
		case "image":
			if s.Image == nil || len(*s.Image) > 512 || strings.TrimSpace(*s.Image) == "" || s.Provider != nil || s.Repository != nil || s.Ref != nil {
				return spec, nil, Invalid("specification.applications.source")
			}
		case "git":
			if s.Image != nil || s.Provider == nil || s.Repository == nil || s.Ref == nil || (*s.Provider != "github" && *s.Provider != "gitlab" && *s.Provider != "gitea") || len(*s.Repository) > 2048 || len(*s.Ref) < 1 || len(*s.Ref) > 256 {
				return spec, nil, Invalid("specification.applications.source")
			}
			u, err := url.Parse(*s.Repository)
			if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return spec, nil, Invalid("specification.applications.source.repository")
			}
		default:
			return spec, nil, Invalid("specification.applications.source.kind")
		}
	}
	names = map[string]bool{}
	for _, v := range spec.Variables {
		if !ValidVariable(v) || names[v.Name] {
			return spec, nil, Invalid("specification.variables")
		}
		names[v.Name] = true
	}
	if err := ValidateVisual(spec); err != nil {
		return spec, nil, err
	}
	canonical, err := json.Marshal(spec)
	if len(canonical) > 131072 {
		return spec, nil, Invalid("specification")
	}
	return spec, canonical, err
}

func ValidateLayout(positions map[string]Position, spec Specification) error {
	if len(positions) > 150 {
		return Invalid("positions")
	}
	ids := map[string]bool{}
	for _, app := range spec.Applications {
		ids[app.ID.String()] = true
	}
	for _, v := range spec.Volumes {
		ids[v.ID.String()] = true
	}
	for _, v := range spec.Routes {
		ids[v.ID.String()] = true
	}
	for _, v := range spec.Networks {
		ids[v.ID.String()] = true
	}
	for id, p := range positions {
		if !ids[id] || math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.Abs(p.X) > 1e6 || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) || math.Abs(p.Y) > 1e6 {
			return Invalid("positions")
		}
	}
	return nil
}
