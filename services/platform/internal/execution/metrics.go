package execution

import (
	"context"
	"encoding/json"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/metrics"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) RecordMetrics(ctx context.Context, a Identity, r api.MetricReport) error {
	if !metrics.Valid(r) {
		return domain.Invalid("metrics")
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 12000 {
		return domain.Invalid("metrics")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	var workspace uuid.UUID
	err = tx.QueryRow(ctx, `SELECT workspace_id,NOT revoked AND NOT awaiting_transfer AND (credential_hash=$2 OR (previous_hash=$2 AND previous_until>now())) FROM servers WHERE id=$1 FOR UPDATE`, a.ID, hash(a.Token)).Scan(&workspace, &valid)
	if err != nil {
		return err
	}
	if !valid {
		return domain.Unauthenticated
	}
	for _, v := range r.Services {
		var owned bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_resources r JOIN deployment_units u ON u.workspace_id=r.workspace_id AND u.id=r.unit_id WHERE r.workspace_id=$1 AND r.id=$2 AND r.unit_id=$3 AND u.server_id=$4)`, workspace, v.ResourceId, v.UnitId, a.ID).Scan(&owned)
		if err != nil {
			return err
		}
		if !owned {
			return domain.Forbidden
		}
	}
	// Receipt time and the first sample in a minute bound retention and retry volume.
	_, err = tx.Exec(ctx, `INSERT INTO server_metrics(workspace_id,server_id,received_at,report) VALUES($1,$2,date_trunc('minute',now()),$3) ON CONFLICT DO NOTHING`, workspace, a.ID, raw)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM server_metrics WHERE server_id=$1 AND received_at<=now()-interval '24 hours'`, a.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Metrics(ctx context.Context, p store.Principal, ws, id uuid.UUID, hours int) (api.MetricHistory, error) {
	out := api.MetricHistory{Samples: []api.MetricSample{}, Resources: []api.MetricResource{}}
	if hours != 1 && hours != 6 && hours != 24 {
		return out, domain.Invalid("hours")
	}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE workspace_id=$1 AND id=$2)`, ws, id).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return domain.NotFound
		}
		rows, err := tx.Query(ctx, `SELECT received_at,report FROM server_metrics WHERE workspace_id=$1 AND server_id=$2 AND received_at>now()-($3*interval '1 hour') ORDER BY received_at LIMIT 1440`, ws, id, hours)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sample api.MetricSample
			var raw []byte
			if err = rows.Scan(&sample.ReceivedAt, &raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &sample.Report); err != nil {
				rows.Close()
				return err
			}
			out.Samples = append(out.Samples, sample)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT r.id,coalesce((SELECT a->>'name' FROM jsonb_array_elements(v.specification->'applications') a WHERE a->>'id'=r.id::text LIMIT 1),r.id::text),e.name FROM managed_resources r JOIN deployment_units u ON u.workspace_id=r.workspace_id AND u.id=r.unit_id JOIN environments e ON e.workspace_id=r.workspace_id AND e.id=r.environment_id LEFT JOIN specification_revisions v ON v.workspace_id=e.workspace_id AND v.id=e.applied_revision_id WHERE r.workspace_id=$1 AND u.server_id=$2 ORDER BY r.id LIMIT 1000`, ws, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var resource api.MetricResource
			if err = rows.Scan(&resource.Id, &resource.Name, &resource.Environment); err != nil {
				return err
			}
			out.Resources = append(out.Resources, resource)
		}
		return rows.Err()
	})
	return out, err
}
