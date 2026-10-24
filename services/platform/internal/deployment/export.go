package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"strings"
)

type Export struct {
	Files         []composeproject.File `json:"files,omitempty"`
	Specification domain.Specification  `json:"specification"`
	Compose       string                `json:"compose"`
	Routes        string                `json:"routes"`
	Readme        string                `json:"readme"`
}

func ExportConfiguration(plan Plan) (Export, error) {
	root := "."
	if plan.Specification.Compose != nil {
		root = "source"
	}
	raw, err := ComposeAt(plan, nil, true, root)
	if err != nil {
		return Export{}, err
	}
	set := RouteSet{UnitID: plan.UnitID, EnvironmentID: plan.EnvironmentID, Routes: plan.Specification.Routes, Backends: map[string]Backend{}}
	for _, a := range plan.Specification.Applications {
		set.Backends[a.ID.String()] = Backend{Address: ServiceName(a.ID)}
	}
	routes, err := RouteConfig(map[string]RouteSet{plan.UnitID.String(): set})
	if err != nil {
		return Export{}, err
	}
	readme := "Save compose.json and routes.yaml together. Supply the indicated environment variables without committing them. This bundle needs Docker and Compose 5.6.1. Data is not included: restore it before starting on another host. The external volumes and network deliberately retain their runtime names.\n\n"
	networkCommand := "docker network create "
	for _, network := range plan.Specification.Networks {
		if network.Name == "default" && network.Internal {
			networkCommand += "--internal "
		}
	}
	readme += "Create the network if absent: " + networkCommand + NetworkName(plan.UnitID) + "\n"
	for _, v := range plan.Specification.Volumes {
		readme += "Create the volume only for a fresh target (never replace existing data): docker volume create " + VolumeName(v.ID) + "\n"
	}
	files := []composeproject.File{}
	if plan.Specification.Compose != nil {
		for _, file := range plan.Specification.Compose.Files {
			files = append(files, composeproject.File{Path: "source/" + file.Path, Content: file.Content})
		}
		readme += "The source/ directory contains the unchanged authoritative files, including extensions and supporting files. compose.json is the frozen executable projection; keep all exported relative paths intact. Runtime secret values are supplied through the required placeholders.\n"
		for _, network := range plan.Specification.Networks {
			if network.Name != "default" {
				command := "docker network create "
				if network.Internal {
					command += "--internal "
				}
				readme += "Create network if absent: " + command + NetworkName(network.ID) + "\n"
			}
		}
	}
	if len(plan.Specification.Routes) > 0 {
		var compose map[string]any
		if json.Unmarshal(raw, &compose) != nil {
			return Export{}, errors.New("invalid rendered compose")
		}
		services := compose["services"].(map[string]any)
		args := []string{"--providers.file.filename=/etc/traefik/routes.yaml", "--entrypoints.web.address=:80", "--entrypoints.websecure.address=:443"}
		ports := []string{"80:80", "443:443"}
		managed := false
		tcp := map[int]bool{}
		for _, r := range plan.Specification.Routes {
			managed = managed || r.TLS == "managed"
			if r.Protocol == "tcp" && !tcp[r.ListenPort] {
				tcp[r.ListenPort] = true
				args = append(args, fmt.Sprintf("--entrypoints.tcp-%d.address=:%d", r.ListenPort, r.ListenPort))
				ports = append(ports, fmt.Sprintf("%d:%d", r.ListenPort, r.ListenPort))
			}
		}
		mounts := []string{"./routes.yaml:/etc/traefik/routes.yaml:ro"}
		if managed {
			args = append(args, "--certificatesresolvers.letsencrypt.acme.email=${ASTILBA_ACME_EMAIL:?Set certificate contact email}", "--certificatesresolvers.letsencrypt.acme.storage=/data/acme.json", "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web")
			mounts = append(mounts, "./acme.json:/data/acme.json")
			readme += "For managed TLS, set ASTILBA_ACME_EMAIL, point DNS to this host, and create acme.json with mode 0600 before starting.\n"
		}
		network := "unit"
		if plan.Specification.Compose != nil {
			network = "default"
		}
		services["ingress"] = map[string]any{"image": ProxyImage, "command": args, "ports": ports, "volumes": mounts, "networks": []string{network}, "restart": "unless-stopped"}
		raw, err = json.MarshalIndent(compose, "", "  ")
		if err != nil {
			return Export{}, err
		}
	}
	readme += "\nStart: docker compose --file compose.json up --detach --wait\nStop: docker compose --file compose.json down (do not add --volumes).\n"
	readme += "Bindings use separate URL-encoded password placeholders ending in _URLENCODED. Set these to the percent-encoded form of the corresponding database password. Plain password placeholders have no suffix.\n"
	return Export{Files: files, Specification: plan.Specification, Compose: string(raw), Routes: string(routes), Readme: strings.TrimSpace(readme)}, nil
}
