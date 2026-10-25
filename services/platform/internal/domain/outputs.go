package domain

import (
	"regexp"
	"strings"
)

// Outputs describe values; credentials stay in the source variable's immutable reference.
type ServiceOutput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Port     int    `json:"port,omitempty"`
	Scheme   string `json:"scheme,omitempty"`
	Path     string `json:"path,omitempty"`
	Variable string `json:"variable,omitempty"`
}

func ServiceOutputs(a Application) []ServiceOutput {
	out := []ServiceOutput{{Name: "HOST", Type: "address"}}
	if a.Postgres != nil {
		out = append(out, ServiceOutput{Name: "PORT", Type: "port", Port: 5432}, ServiceOutput{Name: "URL", Type: "url", Scheme: "postgresql", Port: 5432, Path: "/" + a.Postgres.Database})
		for _, pair := range [][2]string{{"DATABASE", "POSTGRES_DB"}, {"USERNAME", "POSTGRES_USER"}, {"PASSWORD", "POSTGRES_PASSWORD"}} {
			out = append(out, ServiceOutput{Name: pair[0], Type: "variable", Variable: pair[1]})
		}
	}
	return append(out, a.Outputs...)
}

func BindingOutput(a Application, name string) (ServiceOutput, bool) {
	if name == "" {
		if a.Postgres == nil {
			return ServiceOutput{}, false
		}
		name = "URL"
	}
	for _, output := range ServiceOutputs(a) {
		if output.Name == name {
			return output, true
		}
	}
	return ServiceOutput{}, false
}

func OutputVariable(s Specification, a Application, name string) (Variable, bool) {
	if a.Postgres != nil {
		switch name {
		case "POSTGRES_DB":
			return Variable{Name: name, Value: &a.Postgres.Database}, true
		case "POSTGRES_USER":
			return Variable{Name: name, Value: &a.Postgres.Username}, true
		case "POSTGRES_PASSWORD":
			return Variable{Name: name, SecretVersionID: &a.Postgres.PasswordSecretVersionID}, true
		}
	}
	for _, values := range [][]Variable{a.Variables, s.Variables} {
		for _, value := range values {
			if value.Name == name {
				return value, true
			}
		}
	}
	return Variable{}, false
}

var outputScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,31}$`)

func validateOutputs(s Specification, a Application) error {
	if len(a.Outputs) > 20 {
		return Invalid("applications.outputs")
	}
	names := map[string]bool{}
	base := a
	base.Outputs = nil
	for _, output := range ServiceOutputs(base) {
		names[output.Name] = true
	}
	for _, o := range a.Outputs {
		if !variableName.MatchString(o.Name) || names[o.Name] {
			return Invalid("applications.outputs.name")
		}
		names[o.Name] = true
		switch o.Type {
		case "address":
			if o.Port != 0 || o.Scheme != "" || o.Path != "" || o.Variable != "" {
				return Invalid("applications.outputs.address")
			}
		case "port":
			if o.Port < 1 || o.Port > 65535 || o.Scheme != "" || o.Path != "" || o.Variable != "" {
				return Invalid("applications.outputs.port")
			}
		case "url":
			if o.Port < 1 || o.Port > 65535 || !outputScheme.MatchString(o.Scheme) || len(o.Path) > 256 || (o.Path != "" && !strings.HasPrefix(o.Path, "/")) || strings.ContainsAny(o.Path, "\x00\r\n") || o.Variable != "" {
				return Invalid("applications.outputs.url")
			}
		case "variable":
			if _, ok := OutputVariable(s, a, o.Variable); !ok || o.Port != 0 || o.Scheme != "" || o.Path != "" {
				return Invalid("applications.outputs.variable")
			}
		default:
			return Invalid("applications.outputs.type")
		}
	}
	return nil
}

func UsesServiceOutputs(s Specification) bool {
	for _, a := range s.Applications {
		if len(a.Outputs) > 0 {
			return true
		}
	}
	for _, b := range s.Bindings {
		if b.Output != "" {
			return true
		}
	}
	return false
}
