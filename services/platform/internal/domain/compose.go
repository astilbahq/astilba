package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/google/uuid"
)

type Network struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Internal bool      `json:"internal,omitempty"`
}

func VariableValues(variables []Variable, secrets map[string]string) map[string]string {
	out := map[string]string{}
	for _, v := range variables {
		if v.Value != nil {
			out[v.Name] = *v.Value
		} else if v.SecretVersionID != nil {
			value, ok := secrets[v.SecretVersionID.String()]
			if !ok {
				value = "\x00ASTILBA_SECRET_" + v.SecretVersionID.String() + "\x00"
			}
			out[v.Name] = value
		}
	}
	return out
}
func deriveCompose(spec *Specification) error {
	project, err := composeproject.Load(context.Background(), *spec.Compose, VariableValues(spec.Variables, nil))
	if err != nil {
		d := composeproject.ParseDiagnostic(err)
		return &Error{Code: "invalid_input", Message: d.Message, Fields: []string{fmt.Sprintf("%s:%d:%d", d.Path, d.Line, d.Column)}}
	}
	for name, secret := range project.Model.Secrets {
		found := false
		for _, v := range spec.Variables {
			if v.Name == secret.Environment && v.SecretVersionID != nil {
				found = true
			}
		}
		if !found {
			d := project.SourceLocation("secrets", name, "environment")
			return &Error{Code: "invalid_input", Message: "Compose secrets must use an immutable secret-version reference.", Fields: []string{fmt.Sprintf("%s:%d:%d", d.Path, d.Line, d.Column)}}
		}
	}
	namespace := spec.Compose.Namespace
	spec.Applications = []Application{}
	spec.Volumes = nil
	spec.Networks = nil
	spec.Bindings = nil
	spec.Routes = nil
	for _, name := range project.Model.ServiceNames() {
		s := project.Model.Services[name]
		image := s.Image
		app := Application{ID: composeproject.ResourceID(namespace, "service", name), Name: name, Kind: "worker", Source: ApplicationSource{Kind: "image", Image: &image}}
		for _, m := range s.Volumes {
			if m.Type == "volume" {
				app.Mounts = append(app.Mounts, VolumeMount{VolumeID: composeproject.ResourceID(namespace, "volume", m.Source), Target: m.Target, ReadOnly: m.ReadOnly})
			}
		}
		for name := range s.Networks {
			app.Networks = append(app.Networks, composeproject.ResourceID(namespace, "network", name))
		}
		sort.Slice(app.Networks, func(i, j int) bool { return app.Networks[i].String() < app.Networks[j].String() })
		for dep := range s.DependsOn {
			app.DependsOn = append(app.DependsOn, composeproject.ResourceID(namespace, "service", dep))
		}
		sort.Slice(app.DependsOn, func(i, j int) bool { return app.DependsOn[i].String() < app.DependsOn[j].String() })
		spec.Applications = append(spec.Applications, app)
	}
	for name := range project.Model.Volumes {
		spec.Volumes = append(spec.Volumes, Volume{ID: composeproject.ResourceID(namespace, "volume", name), Name: name})
	}
	for name, n := range project.Model.Networks {
		spec.Networks = append(spec.Networks, Network{ID: composeproject.ResourceID(namespace, "network", name), Name: name, Internal: n.Internal})
	}
	sort.Slice(spec.Volumes, func(i, j int) bool { return spec.Volumes[i].Name < spec.Volumes[j].Name })
	sort.Slice(spec.Networks, func(i, j int) bool { return spec.Networks[i].Name < spec.Networks[j].Name })
	if raw, ok := project.Model.Extensions["x-astilba"]; ok {
		var metadata struct {
			Routes []struct {
				Name       string        `json:"name"`
				Service    string        `json:"service"`
				Protocol   string        `json:"protocol"`
				Domain     string        `json:"domain"`
				Port       int           `json:"port"`
				ListenPort int           `json:"listenPort"`
				TLS        string        `json:"tls"`
				AllowCIDRs []string      `json:"allowCidrs"`
				Advanced   *RouteOptions `json:"advanced,omitempty"`
			} `json:"routes"`
		}
		b, _ := json.Marshal(raw)
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&metadata) != nil {
			return Invalid("compose.x-astilba")
		}
		for _, r := range metadata.Routes {
			if _, ok := project.Model.Services[r.Service]; !ok || r.Name == "" {
				return Invalid("compose.routes.service")
			}
			route := Route{ID: composeproject.ResourceID(namespace, "route", r.Name), TargetID: composeproject.ResourceID(namespace, "service", r.Service), Protocol: r.Protocol, Domain: r.Domain, Port: r.Port, ListenPort: r.ListenPort, TLS: r.TLS, AllowCIDRs: r.AllowCIDRs, Advanced: r.Advanced}
			spec.Routes = append(spec.Routes, route)
			for i := range spec.Applications {
				if spec.Applications[i].ID == route.TargetID && r.Protocol == "http" {
					spec.Applications[i].Kind = "web"
				}
			}
		}
	}
	return nil
}
