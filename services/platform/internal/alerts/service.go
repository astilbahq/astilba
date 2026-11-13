package alerts

import (
	"context"
	"encoding/json/v2"
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
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
	e := s.Store.InWorkspaceTx(ctx, p, ws, store.WorkspaceAccess{Owner: true}, func(_ pgx.Tx, q *db.Queries, _ store.Workspace) error {
		settings, e := q.AlertSettings(ctx, ws)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		out.EmailEnabled, out.WebhookEnabled = settings.EmailEnabled, settings.WebhookEnabled
		return e
	})
	return out, e
}
func (s *Service) SaveSettings(ctx context.Context, p store.Principal, ws uuid.UUID, in api.SaveAlertSettings) (api.AlertSettings, error) {
	out := api.AlertSettings{EmailEnabled: in.EmailEnabled, WebhookEnabled: in.WebhookEnabled}
	hook := secretbroker.Item{Kind: secretbroker.AlertWebhook, ID: ws}
	access := store.WorkspaceAccess{Write: true, Owner: true, Recent: true}
	var keys *secretbroker.Session
	if in.WebhookEnabled && in.WebhookUrl != nil {
		var err error
		if keys, err = s.Store.BeginKeys(ctx, p, access, false, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Items: []secretbroker.Item{hook}}); err != nil {
			return out, err
		}
	}
	sealed := false
	defer func() {
		if keys != nil {
			keys.Close(ctx, secretbroker.UsedIf(sealed))
		}
	}()
	e := s.Store.InWorkspaceTx(ctx, p, ws, access, func(_ pgx.Tx, q *db.Queries, _ store.Workspace) error {
		encrypted, err := q.AlertWebhookCipher(ctx, ws)
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
				raw, _ := stablejson.Marshal(webhook{*in.WebhookUrl, *in.WebhookSecret})
				encrypted, err = keys.Seal(hook, raw)
				if err != nil {
					return err
				}
				sealed = true
			}
			if len(encrypted) == 0 {
				return domain.Invalid("webhook")
			}
		} else {
			encrypted = nil
		}
		if err = q.UpsertAlertSettings(ctx, db.UpsertAlertSettingsParams{WorkspaceID: ws, EmailEnabled: in.EmailEnabled, EncryptedWebhook: encrypted}); err != nil {
			return err
		}
		if err = q.CancelWorkspaceAlertDeliveries(ctx, ws); err != nil {
			return err
		}
		return store.Audit(ctx, q, p, ws, "alerts.settings", ws)
	})
	return out, e
}
func (s *Service) Inbox(ctx context.Context, p store.Principal, ws uuid.UUID, before *uuid.UUID, attention bool) (api.AlertInbox, error) {
	out := api.AlertInbox{Alerts: []api.Alert{}}
	e := s.Store.InWorkspaceTx(ctx, p, ws, store.WorkspaceAccess{}, func(_ pgx.Tx, q *db.Queries, _ store.Workspace) error {
		var stamp *time.Time
		if before != nil {
			t, err := q.AlertOpenedAt(ctx, db.AlertOpenedAtParams{WorkspaceID: ws, ID: *before})
			if err != nil {
				return err
			}
			stamp = &t
		}
		rows, err := q.ListAlerts(ctx, db.ListAlertsParams{WorkspaceID: ws, Attention: attention, BeforeAt: stamp, BeforeID: before})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if len(out.Alerts) == 100 {
				id := out.Alerts[99].Id
				out.Next = &id
				break
			}
			a := api.Alert{Id: row.ID, Kind: api.AlertKind(row.Kind), ResourceId: row.ResourceID, ServerId: row.ServerID, EnvironmentId: row.EnvironmentID, State: api.AlertState(row.State), Reason: api.AlertReason(row.Reason), OpenedAt: row.OpenedAt, ResolvedAt: row.ResolvedAt, AcknowledgedAt: row.AcknowledgedAt}
			if err = json.Unmarshal(row.Deliveries, &a.Deliveries); err != nil {
				return err
			}
			out.Alerts = append(out.Alerts, a)
		}
		return nil
	})
	return out, e
}
func (s *Service) Acknowledge(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	return s.Store.InWorkspaceTx(ctx, p, ws, store.WorkspaceAccess{Write: true}, func(_ pgx.Tx, q *db.Queries, _ store.Workspace) error {
		changed, e := q.AcknowledgeAlert(ctx, db.AcknowledgeAlertParams{AcknowledgedAt: s.Now(), AcknowledgedBy: p.Account.ID, WorkspaceID: ws, ID: id})
		if e != nil {
			return e
		}
		if changed != 1 {
			return domain.NotFound
		}
		if e = q.CancelAcknowledgedAlertDeliveries(ctx, db.CancelAcknowledgedAlertDeliveriesParams{WorkspaceID: ws, AlertID: id}); e != nil {
			return e
		}
		return store.Audit(ctx, q, p, ws, "alert.acknowledge", id)
	})
}
