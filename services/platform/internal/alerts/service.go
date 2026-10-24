package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/outbound"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Sender interface {
	Send(context.Context, string, string, string) error
}
type Service struct {
	Store       *store.Store
	Mail        Sender
	Origin      string
	Origins     []string
	Development bool
	Now         func() time.Time
}
type webhook struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

func New(st *store.Store, mail *mailer.Mailer, origin string, development bool, origins []string) *Service {
	return &Service{Store: st, Mail: mail, Origin: origin, Development: development, Origins: origins, Now: time.Now}
}
func (s *Service) destination(raw string) (bool, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return false, domain.Invalid("webhookUrl")
	}
	origin := u.Scheme + "://" + u.Host
	private := false
	for _, allowed := range s.Origins {
		if allowed == origin {
			private = true
		}
	}
	ip := net.ParseIP(u.Hostname())
	local := s.Development && ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && (private || local)) {
		return false, domain.Invalid("webhookUrl")
	}
	if !private && !local && u.Port() != "" && u.Port() != "443" {
		return false, domain.Invalid("webhookUrl")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !private && !local && !outbound.Public(ip) {
		return false, domain.Invalid("webhookUrl")
	}
	return private || local, nil
}
func (s *Service) Settings(ctx context.Context, p store.Principal, ws uuid.UUID) (api.AlertSettings, error) {
	var out api.AlertSettings
	e := s.Store.InWorkspaceTx(ctx, p, ws, false, true, false, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		e := tx.QueryRow(ctx, "SELECT email_enabled,encrypted_webhook IS NOT NULL FROM alert_settings WHERE workspace_id=$1", ws).Scan(&out.EmailEnabled, &out.WebhookEnabled)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	})
	return out, e
}
func (s *Service) SaveSettings(ctx context.Context, p store.Principal, ws uuid.UUID, in api.SaveAlertSettings) (api.AlertSettings, error) {
	out := api.AlertSettings{EmailEnabled: in.EmailEnabled, WebhookEnabled: in.WebhookEnabled}
	e := s.Store.InWorkspaceTx(ctx, p, ws, true, true, true, func(tx pgx.Tx, q *db.Queries, _ store.Workspace) error {
		var encrypted []byte
		err := tx.QueryRow(ctx, "SELECT encrypted_webhook FROM alert_settings WHERE workspace_id=$1", ws).Scan(&encrypted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.WebhookEnabled {
			if (in.WebhookUrl == nil) != (in.WebhookSecret == nil) {
				return domain.Invalid("webhook")
			}
			if in.WebhookUrl != nil {
				if _, err = s.destination(*in.WebhookUrl); err != nil {
					return err
				}
				if len(*in.WebhookSecret) < 32 || len(*in.WebhookSecret) > 256 || strings.ContainsAny(*in.WebhookSecret, "\r\n\x00") {
					return domain.Invalid("webhookSecret")
				}
				raw, _ := json.Marshal(webhook{*in.WebhookUrl, *in.WebhookSecret})
				encrypted, err = s.Store.Keys.Seal(raw, store.AlertWebhookIdentity(ws))
				if err != nil {
					return err
				}
			}
			if len(encrypted) == 0 {
				return domain.Invalid("webhook")
			}
		} else {
			encrypted = nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO alert_settings(workspace_id,email_enabled,encrypted_webhook) VALUES($1,$2,$3) ON CONFLICT(workspace_id) DO UPDATE SET email_enabled=EXCLUDED.email_enabled,encrypted_webhook=EXCLUDED.encrypted_webhook,revision=alert_settings.revision+1`, ws, in.EmailEnabled, encrypted); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE alert_deliveries SET state='cancelled' WHERE workspace_id=$1 AND state IN ('pending','leased')`, ws); err != nil {
			return err
		}
		return store.Audit(ctx, q, p, ws, "alerts.settings", ws)
	})
	return out, e
}
func (s *Service) Inbox(ctx context.Context, p store.Principal, ws uuid.UUID, before *uuid.UUID, attention bool) (api.AlertInbox, error) {
	out := api.AlertInbox{Alerts: []api.Alert{}}
	e := s.Store.InWorkspaceTx(ctx, p, ws, false, false, false, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		var stamp *time.Time
		if before != nil {
			var t time.Time
			if err := tx.QueryRow(ctx, "SELECT opened_at FROM operational_alerts WHERE workspace_id=$1 AND id=$2", ws, *before).Scan(&t); err != nil {
				return err
			}
			stamp = &t
		}
		rows, err := tx.Query(ctx, `SELECT a.id,a.kind,a.resource_id,a.server_id,a.environment_id,a.state,a.reason,a.opened_at,a.resolved_at,a.acknowledged_at,COALESCE((SELECT jsonb_agg(d.value) FROM (SELECT jsonb_build_object('channel',channel,'phase',phase,'state',state,'attempts',attempts) value FROM alert_deliveries WHERE alert_id=a.id ORDER BY created_at,id LIMIT 202) d),'[]'::jsonb) FROM operational_alerts a WHERE workspace_id=$1 AND (NOT $4::boolean OR state='firing' OR (state='event' AND acknowledged_at IS NULL)) AND ($2::timestamptz IS NULL OR (opened_at,id)<($2,$3)) ORDER BY opened_at DESC,id DESC LIMIT 101`, ws, stamp, before, attention)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a api.Alert
			var raw []byte
			if err = rows.Scan(&a.Id, &a.Kind, &a.ResourceId, &a.ServerId, &a.EnvironmentId, &a.State, &a.Reason, &a.OpenedAt, &a.ResolvedAt, &a.AcknowledgedAt, &raw); err != nil {
				return err
			}
			if len(out.Alerts) == 100 {
				id := out.Alerts[99].Id
				out.Next = &id
				break
			}
			if err = json.Unmarshal(raw, &a.Deliveries); err != nil {
				return err
			}
			out.Alerts = append(out.Alerts, a)
		}
		return rows.Err()
	})
	return out, e
}
func (s *Service) Acknowledge(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	return s.Store.InWorkspaceTx(ctx, p, ws, true, false, false, func(tx pgx.Tx, q *db.Queries, _ store.Workspace) error {
		result, e := tx.Exec(ctx, `UPDATE operational_alerts SET acknowledged_at=COALESCE(acknowledged_at,$3),acknowledged_by=COALESCE(acknowledged_by,$4) WHERE workspace_id=$1 AND id=$2`, ws, id, s.Now(), p.Account.ID)
		if e != nil {
			return e
		}
		if result.RowsAffected() != 1 {
			return domain.NotFound
		}
		if _, e = tx.Exec(ctx, "UPDATE alert_deliveries SET state='cancelled' WHERE workspace_id=$1 AND alert_id=$2 AND phase<>'resolved' AND state IN ('pending','leased')", ws, id); e != nil {
			return e
		}
		return store.Audit(ctx, q, p, ws, "alert.acknowledge", id)
	})
}
