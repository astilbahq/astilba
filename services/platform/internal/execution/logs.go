package execution

import (
	"context"
	"encoding/json"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const logExportBytes = 64 * 1024
const logExportEntries = 100

// ExportLogs reads the same agent-redacted snapshots exposed by runtime and
// build history. It never loads execution payloads or decrypts credentials.
func (s *Service) ExportLogs(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, hours int) (api.LogExport, error) {
	now := time.Now().UTC()
	out := api.LogExport{Version: 1, EnvironmentId: env, Coverage: "retained-snapshots", WindowEnd: now, ContentByteLimit: logExportBytes, EntryLimit: logExportEntries, Entries: []api.LogExportEntry{}}
	if hours != 1 && hours != 6 && hours != 24 {
		return out, domain.Invalid("hours")
	}
	out.WindowStart = now.Add(-time.Duration(hours) * time.Hour)
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		snapshots, err := tx.Query(ctx, `SELECT inventory->'observation',observed_at,server_id,unit_id FROM cross_host_runtime WHERE workspace_id=$1 AND environment_id=$2 AND observed_at BETWEEN $3 AND $4
		 UNION ALL SELECT observation,observed_at,NULL::uuid,NULL::uuid FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND observed_at BETWEEN $3 AND $4 AND NOT EXISTS(SELECT 1 FROM cross_host_runtime WHERE workspace_id=$1 AND environment_id=$2)`, ws, env, out.WindowStart, out.WindowEnd)
		if err != nil {
			return err
		}
		for snapshots.Next() {
			var raw []byte
			var at time.Time
			var server, unit *uuid.UUID
			if err := snapshots.Scan(&raw, &at, &server, &unit); err != nil {
				snapshots.Close()
				return err
			}
			var observation deployment.Observation
			if err := json.Unmarshal(raw, &observation); err != nil {
				snapshots.Close()
				return err
			}
			for _, resource := range observation.Resources {
				if resource.Logs != "" {
					out.Entries = append(out.Entries, api.LogExportEntry{Source: api.RuntimeSample, At: at, ResourceId: resource.ID, ServerId: server, UnitId: unit, Content: resource.Logs})
				}
			}
		}
		err = snapshots.Err()
		snapshots.Close()
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,application_id,created_at,result->>'logs' FROM pipeline_runs WHERE workspace_id=$1 AND environment_id=$2 AND created_at>=$3 AND created_at<=$4 AND result ? 'logs' AND result->>'logs'<>'' ORDER BY created_at DESC,id LIMIT 101`, ws, env, out.WindowStart, out.WindowEnd)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry api.LogExportEntry
			var operation uuid.UUID
			entry.Source = api.BuildOutput
			if err := rows.Scan(&operation, &entry.ResourceId, &entry.At, &entry.Content); err != nil {
				return err
			}
			entry.OperationId = &operation
			out.Entries = append(out.Entries, entry)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	boundLogExport(&out)
	return out, nil
}

func boundLogExport(out *api.LogExport) {
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.At.Equal(b.At) {
			return a.ResourceId.String() < b.ResourceId.String()
		}
		return a.At.After(b.At)
	})
	remaining := logExportBytes
	entries := out.Entries[:0]
	for _, entry := range out.Entries {
		if len(entries) == logExportEntries || remaining == 0 {
			out.Truncated = true
			break
		}
		if len(entry.Content) > remaining {
			end := remaining
			for end > 0 && !utf8.RuneStart(entry.Content[end]) {
				end--
			}
			entry.Content = entry.Content[:end]
			entry.Truncated = true
			out.Truncated = true
		}
		remaining -= len(entry.Content)
		entries = append(entries, entry)
	}
	out.Entries = entries
}
