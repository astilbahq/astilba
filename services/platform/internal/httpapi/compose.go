package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func sourceDiagnostic(err error) composeproject.Diagnostic {
	var de *domain.Error
	if errors.As(err, &de) {
		d := composeproject.Diagnostic{Line: 1, Column: 1, Message: de.Message}
		if len(de.Fields) > 0 {
			parts := strings.Split(de.Fields[0], ":")
			if len(parts) == 3 {
				d.Path = parts[0]
				d.Line, _ = strconv.Atoi(parts[1])
				d.Column, _ = strconv.Atoi(parts[2])
			}
		}
		return d
	}
	return composeproject.ParseDiagnostic(err)
}
func (s *Server) PreviewCompose(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, err := decode[api.ComposePreviewRequest](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	preview := func(q *db.Queries, e db.GetEnvironmentRow) (any, error) {
		if e.ConfigurationOwner != "compose" {
			return nil, domain.Invalid("configurationOwner")
		}
		result := map[string]any{"diagnostics": []composeproject.Diagnostic{}, "changes": []map[string]string{}, "permissions": []string{}}
		spec, _, err := domain.ParseSpec(b.Specification)
		if err != nil {
			result["diagnostics"] = []composeproject.Diagnostic{sourceDiagnostic(err)}
			return result, nil
		}
		if spec.Compose == nil || spec.Compose.Namespace != e.UnitID {
			return nil, domain.Invalid("compose.namespace")
		}
		for _, id := range domain.SecretReferences(spec) {
			if _, err := q.GetSecretVersion(r.Context(), db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: env, ID: id}); err != nil {
				return nil, domain.Invalid("secretVersionId")
			}
		}
		if b.Edit != nil {
			before := spec.Compose.Files
			edited, err := composeproject.EditImage(r.Context(), *spec.Compose, domain.VariableValues(spec.Variables, nil), b.Edit.Service, b.Edit.Image)
			if err != nil {
				result["diagnostics"] = []composeproject.Diagnostic{sourceDiagnostic(err)}
				return result, nil
			}
			spec.Compose = &edited
			changes := []map[string]string{}
			for i, file := range before {
				if file.Content != edited.Files[i].Content {
					changes = append(changes, map[string]string{"path": file.Path, "before": file.Content, "after": edited.Files[i].Content})
				}
			}
			result["changes"] = changes
			raw, _ := json.Marshal(spec)
			spec, _, err = domain.ParseSpec(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid source edit: %w", err)
			}
		}
		project, err := composeproject.Load(r.Context(), *spec.Compose, domain.VariableValues(spec.Variables, nil))
		if err != nil {
			result["diagnostics"] = []composeproject.Diagnostic{sourceDiagnostic(err)}
			return result, nil
		}
		result["specification"] = spec
		result["permissions"] = project.Permissions
		return result, nil
	}
	var output any
	err = s.Store.InWorkspace(r.Context(), *principal(r.Context()), ws, true, false, false, func(q *db.Queries, _ store.Workspace) error {
		environment, e := q.GetEnvironment(r.Context(), db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env})
		if e != nil {
			return e
		}
		output, e = preview(q, environment)
		return e
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, 200, output)
}

func (s *Server) ExportEnvironmentArchive(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	bundle, err := s.Execution.ExportVisual(r.Context(), *principal(r.Context()), ws, pid, env)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	files := append([]composeproject.File{{Path: "compose.json", Content: bundle.Compose}, {Path: "routes.yaml", Content: bundle.Routes}, {Path: "README.txt", Content: bundle.Readme}}, bundle.Files...)
	for _, file := range files {
		entry, err := archive.Create(file.Path)
		if err == nil {
			_, err = io.WriteString(entry, file.Content)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := archive.Close(); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="astilba-configuration.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buffer.Bytes())
}
