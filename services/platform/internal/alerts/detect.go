package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type evidence int

const (
	unknown evidence = iota
	bad
	good
	stopped
)

type target struct {
	resource    uuid.UUID
	server      *uuid.UUID
	environment *uuid.UUID
}

// Sweep claims work before evaluation so an invalid source cannot starve the fleet.
func (s *Service) Sweep(ctx context.Context) error {
	var last error
	for range 20 {
		var ws uuid.UUID
		err := s.Store.Pool.QueryRow(ctx, `UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT id FROM workspaces WHERE alert_scanned_at<$1::timestamptz-interval '30 seconds' ORDER BY alert_scanned_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`, s.Now()).Scan(&ws)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		if err = s.detect(ctx, ws); err != nil {
			last = err
		}
	}
	return last
}
func (s *Service) detect(ctx context.Context, ws uuid.UUID) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
		return err
	}
	if err = s.operations(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.servers(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.backups(ctx, tx, ws); err != nil {
		return err
	}
	// Active incidents remain visible until observation establishes recovery or monitoring ends.
	if _, err = tx.Exec(ctx, `DELETE FROM operational_alerts WHERE id IN (SELECT id FROM operational_alerts WHERE workspace_id=$1 AND state<>'firing' AND (COALESCE(resolved_at,opened_at)<$2::timestamptz-interval '30 days' OR id IN (SELECT id FROM operational_alerts WHERE workspace_id=$1 AND state<>'firing' ORDER BY opened_at DESC,id DESC OFFSET 1000)) LIMIT 1000)`, ws, s.Now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) condition(ctx context.Context, tx pgx.Tx, ws uuid.UUID, kind api.AlertKind, t target, value evidence) error {
	if value == unknown {
		return nil
	}
	var id uuid.UUID
	phase := "firing"
	var err error
	if value == bad {
		err = tx.QueryRow(ctx, `INSERT INTO operational_alerts(id,workspace_id,kind,resource_id,server_id,environment_id,state,opened_at) VALUES($1,$2,$3,$4,$5,$6,'firing',$7) ON CONFLICT DO NOTHING RETURNING id`, uuid.New(), ws, kind, t.resource, t.server, t.environment, s.Now()).Scan(&id)
	} else {
		phase = "resolved"
		reason := "recovered"
		if value == stopped {
			reason = "monitoring_stopped"
		}
		err = tx.QueryRow(ctx, `UPDATE operational_alerts SET state='resolved',reason=$4,resolved_at=$5 WHERE workspace_id=$1 AND kind=$2 AND resource_id=$3 AND state='firing' RETURNING id`, ws, kind, t.resource, reason, s.Now()).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.enqueue(ctx, tx, ws, id, phase)
}
func (s *Service) operations(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	// Reports lock the server before the operation. Keep that order when marking
	// observations, including commands that do not produce an alert.
	rows, err := tx.Query(ctx, `SELECT o.id,o.command,o.environment_id,o.server_id FROM execution_operations o JOIN servers s ON s.id=o.server_id WHERE o.workspace_id=$1 AND NOT o.alert_observed AND o.state IN ('failed','needs_attention') ORDER BY o.created_at,o.id LIMIT 100 FOR KEY SHARE OF s`, ws)
	if err != nil {
		return err
	}
	type operation struct {
		id          uuid.UUID
		raw         []byte
		environment *uuid.UUID
		server      uuid.UUID
	}
	list := []operation{}
	for rows.Next() {
		var o operation
		if err = rows.Scan(&o.id, &o.raw, &o.environment, &o.server); err != nil {
			rows.Close()
			return err
		}
		list = append(list, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range list {
		var c api.ExecutionCommand
		if err = json.Unmarshal(o.raw, &c); err != nil {
			return err
		}
		kind := api.AlertKind("")
		switch c.Kind {
		case "visual":
			var p deployment.Payload
			if c.Visual != nil && json.Unmarshal(*c.Visual, &p) == nil && p.Action == "deploy" {
				kind = api.DeploymentProblem
			}
		case "pipeline":
			var p struct {
				Action string `json:"action"`
			}
			if c.Pipeline != nil && json.Unmarshal(*c.Pipeline, &p) == nil && p.Action == "build" {
				kind = api.BuildProblem
			}
		case "backup":
			kind = api.BackupProblem
		}
		if kind != "" {
			id := uuid.New()
			if _, err = tx.Exec(ctx, `INSERT INTO operational_alerts(id,workspace_id,kind,resource_id,server_id,environment_id,state,opened_at) VALUES($1,$2,$3,$4,$5,$6,'event',$7)`, id, ws, kind, o.id, o.server, o.environment, s.Now()); err != nil {
				return err
			}
			if err = s.enqueue(ctx, tx, ws, id, "event"); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE execution_operations SET alert_observed=true WHERE id=$1", o.id); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, phase string) error {
	var email, hook bool
	var revision int64
	err := tx.QueryRow(ctx, "SELECT email_enabled,encrypted_webhook IS NOT NULL,revision FROM alert_settings WHERE workspace_id=$1", ws).Scan(&email, &hook, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	insert := func(channel string, recipient uuid.UUID, state string) error {
		_, e := tx.Exec(ctx, `INSERT INTO alert_deliveries(id,workspace_id,alert_id,phase,channel,recipient_id,settings_revision,state,next_attempt_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT DO NOTHING`, uuid.New(), ws, id, phase, channel, recipient, revision, state, s.Now())
		return e
	}
	if email {
		rows, e := tx.Query(ctx, `SELECT a.id FROM accounts a JOIN memberships m ON m.account_id=a.id WHERE m.workspace_id=$1 AND m.role='owner' AND a.email_verified_at IS NOT NULL ORDER BY a.id LIMIT 101`, ws)
		if e != nil {
			return e
		}
		ids := []uuid.UUID{}
		for rows.Next() {
			var v uuid.UUID
			if e = rows.Scan(&v); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(ids) > 100 {
			if e = insert("email", uuid.Nil, "failed"); e != nil {
				return e
			}
		} else {
			for _, v := range ids {
				if e = insert("email", v, "pending"); e != nil {
					return e
				}
			}
		}
	}
	if hook {
		return insert("webhook", uuid.Nil, "pending")
	}
	return nil
}
func fresh(samples []api.MetricSample, now time.Time, n int) bool {
	if len(samples) < n {
		return false
	}
	list := samples[len(samples)-n:]
	if now.Sub(list[n-1].ReceivedAt) > 180*time.Second || list[n-1].ReceivedAt.After(now) {
		return false
	}
	for i := 1; i < n; i++ {
		if list[i].ReceivedAt.Sub(list[i-1].ReceivedAt) != time.Minute {
			return false
		}
	}
	return true
}
func diskEvidence(samples []api.MetricSample, now time.Time, source api.HostMetricsDiskSource) evidence {
	if !fresh(samples, now, 1) {
		return unknown
	}
	matches := func(n int, threshold float64, high bool) bool {
		if !fresh(samples, now, n) {
			return false
		}
		for _, v := range samples[len(samples)-n:] {
			d := v.Report.Host.Disk
			if v.Report.Host.DiskSource != source || d == nil {
				return false
			}
			ratio := 100 * float64(d.UsedBytes) / float64(d.TotalBytes)
			if high && ratio < threshold || !high && ratio >= threshold {
				return false
			}
		}
		return true
	}
	if matches(3, 90, true) {
		return bad
	}
	if matches(2, 85, false) {
		return good
	}
	return unknown
}
func serviceEvidence(samples []api.MetricSample, now time.Time, id uuid.UUID, health bool) evidence {
	if !fresh(samples, now, 1) {
		return unknown
	}
	classify := func(v api.MetricSample) evidence {
		for _, r := range v.Report.Services {
			if r.ResourceId == id {
				if health {
					if r.Health == nil {
						return unknown
					}
					switch *r.Health {
					case "unhealthy":
						return bad
					case "healthy":
						return good
					case "none":
						return stopped
					default:
						return unknown
					}
				}
				switch r.State {
				case "running":
					return good
				case "stopped", "paused", "restarting":
					return bad
				default:
					return unknown
				}
			}
		}
		if !health && !v.Report.Partial {
			return bad
		}
		return unknown
	}
	last := classify(samples[len(samples)-1])
	if last != bad {
		return last
	}
	if !fresh(samples, now, 3) {
		return unknown
	}
	for _, v := range samples[len(samples)-3:] {
		if classify(v) != bad {
			return unknown
		}
	}
	return bad
}
func (s *Service) servers(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	type server struct {
		id       uuid.UUID
		last     *time.Time
		disabled bool
	}
	rows, err := tx.Query(ctx, "SELECT id,last_seen,revoked OR awaiting_transfer FROM servers WHERE workspace_id=$1 ORDER BY id LIMIT 100", ws)
	if err != nil {
		return err
	}
	servers := []server{}
	for rows.Next() {
		var v server
		if err = rows.Scan(&v.id, &v.last, &v.disabled); err != nil {
			rows.Close()
			return err
		}
		servers = append(servers, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range servers {
		t := target{resource: v.id, server: &v.id}
		value := unknown
		if v.disabled {
			value = stopped
		} else if v.last != nil {
			if s.Now().Sub(*v.last) > 2*time.Minute {
				value = bad
			} else if s.Now().Sub(*v.last) <= 45*time.Second {
				value = good
			}
		}
		if err = s.condition(ctx, tx, ws, api.ServerOffline, t, value); err != nil {
			return err
		}
		if v.disabled {
			if err = s.stopServer(ctx, tx, ws, v.id); err != nil {
				return err
			}
			continue
		}
		samples := []api.MetricSample{}
		rr, e := tx.Query(ctx, `SELECT received_at,report FROM (SELECT received_at,report FROM server_metrics WHERE workspace_id=$1 AND server_id=$2 ORDER BY received_at DESC LIMIT 3) m ORDER BY received_at`, ws, v.id)
		if e != nil {
			return e
		}
		for rr.Next() {
			var m api.MetricSample
			var raw []byte
			if e = rr.Scan(&m.ReceivedAt, &raw); e != nil {
				rr.Close()
				return e
			}
			if e = json.Unmarshal(raw, &m.Report); e != nil {
				rr.Close()
				return e
			}
			samples = append(samples, m)
		}
		e = rr.Err()
		rr.Close()
		if e != nil {
			return e
		}
		for source, kind := range map[api.HostMetricsDiskSource]api.AlertKind{"docker": api.DiskDocker, "agent": api.DiskAgent} {
			if e = s.condition(ctx, tx, ws, kind, t, diskEvidence(samples, s.Now(), source)); e != nil {
				return e
			}
		}
		if e = s.services(ctx, tx, ws, v.id, samples); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) stopServer(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT kind,resource_id,environment_id FROM operational_alerts WHERE workspace_id=$1 AND server_id=$2 AND state='firing'`, ws, server)
	if err != nil {
		return err
	}
	type item struct {
		kind api.AlertKind
		t    target
	}
	list := []item{}
	for rows.Next() {
		v := item{t: target{server: &server}}
		if err = rows.Scan(&v.kind, &v.t.resource, &v.t.environment); err != nil {
			rows.Close()
			return err
		}
		list = append(list, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range list {
		if err = s.condition(ctx, tx, ws, v.kind, v.t, stopped); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) services(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID, samples []api.MetricSample) error {
	rows, err := tx.Query(ctx, `SELECT r.id,r.environment_id FROM managed_resources r JOIN deployment_units u ON u.workspace_id=r.workspace_id AND u.id=r.unit_id JOIN environments e ON e.workspace_id=r.workspace_id AND e.id=r.environment_id JOIN specification_revisions v ON v.workspace_id=e.workspace_id AND v.id=e.applied_revision_id WHERE r.workspace_id=$1 AND u.server_id=$2 AND EXISTS(SELECT 1 FROM jsonb_array_elements(v.specification->'applications') a WHERE a->>'id'=r.id::text) ORDER BY r.id LIMIT 2001`, ws, server)
	if err != nil {
		return err
	}
	list := []target{}
	ids := []uuid.UUID{}
	for rows.Next() {
		v := target{server: &server}
		if err = rows.Scan(&v.resource, &v.environment); err != nil {
			rows.Close()
			return err
		}
		list = append(list, v)
		ids = append(ids, v.resource)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(list) > 2000 {
		return errors.New("alert service inventory limit exceeded")
	}
	for _, v := range list {
		if err = s.condition(ctx, tx, ws, api.ServiceRuntime, v, serviceEvidence(samples, s.Now(), v.resource, false)); err != nil {
			return err
		}
		if err = s.condition(ctx, tx, ws, api.ServiceHealth, v, serviceEvidence(samples, s.Now(), v.resource, true)); err != nil {
			return err
		}
	}
	rr, err := tx.Query(ctx, `SELECT kind,resource_id,environment_id FROM operational_alerts WHERE workspace_id=$1 AND server_id=$2 AND state='firing' AND kind IN ('service_runtime','service_health') AND NOT(resource_id=ANY($3::uuid[]))`, ws, server, ids)
	if err != nil {
		return err
	}
	type old struct {
		kind api.AlertKind
		t    target
	}
	oldList := []old{}
	for rr.Next() {
		v := old{t: target{server: &server}}
		if err = rr.Scan(&v.kind, &v.t.resource, &v.t.environment); err != nil {
			rr.Close()
			return err
		}
		oldList = append(oldList, v)
	}
	err = rr.Err()
	rr.Close()
	if err != nil {
		return err
	}
	for _, v := range oldList {
		if err = s.condition(ctx, tx, ws, v.kind, v.t, stopped); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) backups(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT p.id,p.server_id,p.environment_id,p.status,p.configuration,s.revoked OR s.awaiting_transfer FROM backup_policies p JOIN servers s ON s.id=p.server_id WHERE p.workspace_id=$1 ORDER BY p.id LIMIT 2000`, ws)
	if err != nil {
		return err
	}
	type item struct {
		t              target
		status, config []byte
		disabled       bool
	}
	list := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.t.resource, &v.t.server, &v.t.environment, &v.status, &v.config, &v.disabled); err != nil {
			rows.Close()
			return err
		}
		list = append(list, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range list {
		var status backup.Status
		var policy backup.Policy
		if err = json.Unmarshal(v.status, &status); err != nil {
			return err
		}
		if err = json.Unmarshal(v.config, &policy); err != nil {
			return err
		}
		value := unknown
		if v.disabled || !policy.Enabled {
			value = stopped
		} else {
			var latest *backup.Run
			var latestAt time.Time
			for i := range status.Runs {
				r := &status.Runs[i]
				if r.TargetID != uuid.Nil || (r.State != "failed" && r.State != "needs_attention" && r.State != "succeeded") {
					continue
				}
				at := r.StartedAt
				if r.FinishedAt != nil {
					at = *r.FinishedAt
				}
				if latest == nil || at.After(latestAt) {
					latest = r
					latestAt = at
				}
			}
			if latest != nil {
				switch latest.State {
				case "failed", "needs_attention":
					value = bad
				case "succeeded":
					value = good
				}
			}
		}
		if err = s.condition(ctx, tx, ws, api.BackupSchedule, v.t, value); err != nil {
			return err
		}
	}
	return nil
}
