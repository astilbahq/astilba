package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/google/uuid"
)

func SourceProject(p Plan, secrets map[string]string) (*composeproject.Project, error) {
	if p.Specification.Compose == nil {
		return nil, errors.New("not a Compose plan")
	}
	for _, value := range secrets {
		if strings.ContainsRune(value, 0) {
			return nil, errors.New("secret values cannot contain NUL")
		}
	}
	return composeproject.Load(context.Background(), *p.Specification.Compose, domain.VariableValues(p.Specification.Variables, secrets))
}
func sourceCompose(p Plan, secrets map[string]string, root string, export bool) ([]byte, error) {
	project, err := SourceProject(p, secrets)
	if err != nil {
		return nil, err
	}
	model := project.Model
	namespace := p.UnitID
	model.Name = UnitName(namespace)
	for name := range model.Networks {
		id := composeproject.ResourceID(namespace, "network", name)
		runtime := NetworkName(id)
		if name == "default" {
			runtime = NetworkName(namespace)
		}
		model.Networks[name] = types.NetworkConfig{Name: runtime, External: true}
	}
	for name := range model.Volumes {
		model.Volumes[name] = types.VolumeConfig{Name: VolumeName(composeproject.ResourceID(namespace, "volume", name)), External: true}
	}
	for name, v := range model.Configs {
		if v.File != "" {
			v.File = filepath.Join(root, v.File)
		}
		v.Name = UnitName(namespace) + "-" + name
		model.Configs[name] = v
	}
	for name, v := range model.Secrets {
		for _, variable := range p.Specification.Variables {
			if variable.Name == v.Environment && variable.SecretVersionID != nil {
				v.Environment = SecretEnvironmentName(*variable.SecretVersionID)
			}
		}
		v.Content = ""
		v.Name = UnitName(namespace) + "-" + name
		model.Secrets[name] = v
	}
	services := types.Services{}
	for name, s := range model.Services {
		id := composeproject.ResourceID(namespace, "service", name)
		s.Name = ServiceName(id)
		s.ContainerName = UnitName(id)
		s.Image = p.Images[id.String()]
		if s.Labels == nil {
			s.Labels = types.Labels{}
		}
		for key, value := range map[string]string{"server": p.ServerID.String(), "environment": p.EnvironmentID.String(), "unit": p.UnitID.String(), "resource": id.String(), "kind": "visual-v1", "plan": p.Hash} {
			s.Labels["com.astilba."+key] = value
		}
		networks := map[string]*types.ServiceNetworkConfig{}
		for key, network := range s.Networks {
			if network == nil {
				network = &types.ServiceNetworkConfig{}
			}
			network.Aliases = append(network.Aliases, name, ServiceName(id))
			networks[key] = network
		}
		for _, route := range p.Specification.Routes {
			if route.TargetID == id {
				if _, ok := model.Networks["default"]; !ok {
					model.Networks["default"] = types.NetworkConfig{Name: NetworkName(namespace), External: true}
				}
				if networks["default"] == nil {
					networks["default"] = &types.ServiceNetworkConfig{Aliases: []string{name, ServiceName(id)}}
				}
			}
		}
		s.Networks = networks
		depends := types.DependsOnConfig{}
		for dep, condition := range s.DependsOn {
			depends[ServiceName(composeproject.ResourceID(namespace, "service", dep))] = condition
		}
		s.DependsOn = depends
		for i, m := range s.Volumes {
			if m.Type == "bind" {
				m.Source = filepath.Join(root, m.Source)
				s.Volumes[i] = m
			}
		}
		services[s.Name] = s
	}
	model.Services = services
	raw, err := model.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, errors.New("invalid compiled Compose")
	}
	var rewrite func(any) any
	rewrite = func(v any) any {
		switch v := v.(type) {
		case string:
			v = strings.ReplaceAll(v, "$", "$$")
			if export {
				for _, ref := range domain.SecretReferences(p.Specification) {
					v = strings.ReplaceAll(v, "\x00ASTILBA_SECRET_"+ref.String()+"\x00", "${ASTILBA_SECRET_"+strings.ReplaceAll(ref.String(), "-", "")+":?Set the exported secret}")
				}
			}
			return v
		case []any:
			for i, x := range v {
				v[i] = rewrite(x)
			}
			return v
		case map[string]any:
			for k, x := range v {
				v[k] = rewrite(x)
			}
			return v
		default:
			return v
		}
	}
	return json.MarshalIndent(rewrite(value), "", "  ")
}
func SourceNetworkName(unit, id uuid.UUID, name string) string {
	if name == "default" {
		return NetworkName(unit)
	}
	return NetworkName(id)
}

func Claims(p Plan) ([]string, error) {
	out := []string{}
	for _, r := range p.Specification.Routes {
		claim := "http:" + r.Domain
		if r.Protocol == "tcp" {
			claim = fmt.Sprintf("tcp:%d", r.ListenPort)
		}
		out = append(out, claim)
	}
	if p.Specification.Compose != nil {
		source, err := SourceProject(p, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, source.Ports...)
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil, errors.New("repeated host route/port claim")
		}
	}
	return out, nil
}

func SecretEnvironmentName(id uuid.UUID) string {
	return "ASTILBA_SECRET_" + strings.ReplaceAll(id.String(), "-", "")
}
