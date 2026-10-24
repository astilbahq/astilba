package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/outbound"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type delivery struct {
	id, workspace, alert, recipient uuid.UUID
	channel, phase                  string
	revision                        int64
	attempt                         int
}
type payload struct {
	DeliveryID  uuid.UUID     `json:"deliveryId"`
	AlertID     uuid.UUID     `json:"alertId"`
	WorkspaceID uuid.UUID     `json:"workspaceId"`
	ResourceID  uuid.UUID     `json:"resourceId"`
	Kind        api.AlertKind `json:"kind"`
	State       string        `json:"state"`
	Reason      string        `json:"reason"`
	OccurredAt  time.Time     `json:"occurredAt"`
}

func (s *Service) claim(ctx context.Context) (*delivery, error) {
	now := s.Now()
	_, err := s.Store.Pool.Exec(ctx, `UPDATE alert_deliveries SET state='failed' WHERE id IN (SELECT id FROM alert_deliveries WHERE state IN ('pending','leased') AND next_attempt_at<=$1 AND (created_at<$1::timestamptz-interval '24 hours' OR attempts>=5) ORDER BY next_attempt_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED)`, now)
	if err != nil {
		return nil, err
	}
	var d delivery
	err = s.Store.Pool.QueryRow(ctx, `UPDATE alert_deliveries SET state='leased',attempts=attempts+1,next_attempt_at=$1::timestamptz+interval '30 seconds' WHERE id=(SELECT id FROM alert_deliveries WHERE state IN ('pending','leased') AND next_attempt_at<=$1 AND created_at>=$1::timestamptz-interval '24 hours' AND attempts<5 ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id,workspace_id,alert_id,recipient_id,channel,phase,settings_revision,attempts`, now).Scan(&d.id, &d.workspace, &d.alert, &d.recipient, &d.channel, &d.phase, &d.revision, &d.attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}
func (s *Service) Deliver(ctx context.Context) error {
	var last error
	for range 20 {
		d, err := s.claim(ctx)
		if err != nil {
			return err
		}
		if d == nil {
			break
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		state, err := s.send(attempt, *d)
		cancel()
		if err != nil {
			last = err
			state = "pending"
			if d.attempt >= 5 {
				state = "failed"
			}
		}
		delay := time.Minute * time.Duration(1<<(d.attempt-1))
		_, finish := s.Store.Pool.Exec(ctx, `UPDATE alert_deliveries SET state=$2,next_attempt_at=$3 WHERE id=$1 AND state='leased' AND attempts=$4`, d.id, state, s.Now().Add(delay), d.attempt)
		if finish != nil {
			return finish
		}
	}
	return last
}
func (s *Service) send(ctx context.Context, d delivery) (string, error) {
	var p payload
	p.DeliveryID = d.id
	p.AlertID = d.alert
	p.WorkspaceID = d.workspace
	p.State = d.phase
	var emailEnabled, webhookEnabled, ack bool
	var revision int64
	var cipher []byte
	var current string
	err := s.Store.Pool.QueryRow(ctx, `SELECT a.kind,a.resource_id,a.reason,CASE WHEN $3='resolved' THEN a.resolved_at ELSE a.opened_at END,a.state,a.acknowledged_at IS NOT NULL,s.email_enabled,s.encrypted_webhook IS NOT NULL,s.revision,s.encrypted_webhook FROM operational_alerts a JOIN alert_settings s ON s.workspace_id=a.workspace_id WHERE a.workspace_id=$1 AND a.id=$2`, d.workspace, d.alert, d.phase).Scan(&p.Kind, &p.ResourceID, &p.Reason, &p.OccurredAt, &current, &ack, &emailEnabled, &webhookEnabled, &revision, &cipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return "cancelled", nil
	}
	if err != nil {
		return "", err
	}
	if revision != d.revision || (d.phase == "firing" && current != "firing") || (ack && d.phase != "resolved") {
		return "cancelled", nil
	}
	if d.channel == "email" {
		if !emailEnabled {
			return "cancelled", nil
		}
		var address string
		err = s.Store.Pool.QueryRow(ctx, `SELECT a.email FROM memberships m JOIN accounts a ON a.id=m.account_id WHERE m.workspace_id=$1 AND m.account_id=$2 AND m.role='owner' AND a.email_verified_at IS NOT NULL`, d.workspace, d.recipient).Scan(&address)
		if errors.Is(err, pgx.ErrNoRows) {
			return "cancelled", nil
		}
		if err != nil {
			return "", err
		}
		if s.Mail == nil {
			return "", errors.New("alert mail unavailable")
		}
		body := fmt.Sprintf("%s\nState: %s\nReason: %s\nWorkspace: %s\nResource: %s\nObserved: %s\nDelivery: %s\n\nOpen Alerts in your workspace dashboard: %s/alerts\n\nThis records an observation. It does not change your workloads.", Title(p.Kind), p.State, p.Reason, p.WorkspaceID, p.ResourceID, p.OccurredAt.UTC().Format(time.RFC3339), p.DeliveryID, s.Origin)
		if err = s.Mail.Send(ctx, address, "Astilba: "+Title(p.Kind), body); err != nil {
			return "", errors.New("alert mail delivery failed")
		}
		return "sent", nil
	}
	if !webhookEnabled {
		return "cancelled", nil
	}
	raw, err := s.Store.Keys.Open(cipher, store.AlertWebhookIdentity(d.workspace))
	if err != nil {
		return "", err
	}
	var hook webhook
	if json.Unmarshal(raw, &hook) != nil {
		return "", errors.New("alert destination unavailable")
	}
	private, err := s.destination(hook.URL)
	if err != nil {
		return "", err
	}
	raw, err = json.Marshal(p)
	if err != nil {
		return "", err
	}
	timestamp := strconv.FormatInt(s.Now().Unix(), 10)
	signature := hmac.New(sha256.New, []byte(hook.Secret))
	signature.Write([]byte(timestamp + "\n"))
	signature.Write(raw)
	req, err := http.NewRequestWithContext(ctx, "POST", hook.URL, bytes.NewReader(raw))
	if err != nil {
		return "", errors.New("alert destination unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", d.id.String())
	req.Header.Set("X-Astilba-Delivery", d.id.String())
	req.Header.Set("X-Astilba-Timestamp", timestamp)
	req.Header.Set("X-Astilba-Signature", "sha256="+hex.EncodeToString(signature.Sum(nil)))
	client := outbound.Client(private, 10*time.Second)
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("alert webhook delivery failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", errors.New("alert webhook delivery failed")
	}
	return "sent", nil
}
func Title(kind api.AlertKind) string {
	switch kind {
	case api.ServerOffline:
		return "Server disconnected"
	case api.DiskDocker:
		return "Docker filesystem pressure"
	case api.DiskAgent:
		return "Agent filesystem pressure"
	case api.ServiceRuntime:
		return "Managed service unavailable"
	case api.ServiceHealth:
		return "Managed service health check failed"
	case api.BackupSchedule:
		return "Scheduled backup failed"
	case api.DeploymentProblem:
		return "Deployment needs review"
	case api.BuildProblem:
		return "Build needs review"
	case api.BackupProblem:
		return "Backup operation needs review"
	default:
		return "Operational alert"
	}
}
