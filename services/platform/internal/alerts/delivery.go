package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/outbound"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
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

// Delivery runs attempts side by side, each on its own deadline, so that one destination that
// is slow to answer delays no other. A claim passes over a workspace's webhook while an attempt
// on it holds a lease, so that receiver holds one of these on a replica. Replicas that claim at the
// same moment do not see each other's leases, and can each start one. Mail goes through the
// operator's own relay, so a workspace's emails, one per owner, go out side by side.
const (
	deliveryConcurrency = 8
	// webhookDeadline bounds a webhook attempt, connecting included. Receivers are expected to
	// answer at once and do their own work afterwards.
	webhookDeadline = 5 * time.Second
	// attemptDeadline bounds any attempt. The lease outlasts it, so no attempt is still running
	// when its delivery can be claimed again.
	attemptDeadline = 10 * time.Second
	deliveryLease   = 30 * time.Second
	// deliveryPoll is how often a loop with nothing to send looks for deliveries that came due.
	deliveryPoll = 10 * time.Second
)

func (s *Service) claim(ctx context.Context) (*delivery, error) {
	now := s.Now()
	if err := s.Store.Queries.FailSpentAlertDeliveries(ctx, now); err != nil {
		return nil, err
	}
	row, err := s.Store.Queries.LeaseAlertDelivery(ctx, db.LeaseAlertDeliveryParams{Now: now, LeasedUntil: now.Add(deliveryLease)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &delivery{id: row.ID, workspace: row.WorkspaceID, alert: row.AlertID, recipient: row.RecipientID, channel: row.Channel, phase: row.Phase, revision: row.SettingsRevision, attempt: int(row.Attempts)}, nil
}

// Deliver makes every attempt that is due, and returns once none is due and none is running.
// The error is the last to happen of any attempt's, claim's or record's.
func (s *Service) Deliver(ctx context.Context) error {
	return s.deliver(ctx, false)
}

// RunDeliveries delivers until ctx ends, looking for due deliveries whenever an attempt
// finishes and every deliveryPoll otherwise.
func (s *Service) RunDeliveries(ctx context.Context) {
	_ = s.deliver(ctx, true)
}

func (s *Service) deliver(ctx context.Context, continuous bool) error {
	var (
		attempts sync.WaitGroup
		mu       sync.Mutex
		last     error
	)
	failed := func(err error) {
		mu.Lock()
		last = err
		mu.Unlock()
	}
	finished := make(chan struct{}, deliveryConcurrency)
	poll := time.NewTicker(deliveryPoll)
	defer poll.Stop()
	defer attempts.Wait()
	running, claiming := 0, true
	for {
		for claiming && running < deliveryConcurrency && ctx.Err() == nil {
			d, err := s.claim(ctx)
			if err != nil {
				failed(err)
				if !continuous {
					claiming = false
				} else if ctx.Err() == nil {
					slog.Warn("operational alert delivery could not claim work")
				}
				break
			}
			if d == nil {
				break
			}
			running++
			attempts.Go(func() {
				defer func() { finished <- struct{}{} }()
				sent, recorded := s.attempt(ctx, *d)
				if recorded != nil {
					failed(recorded)
					if continuous && ctx.Err() == nil {
						slog.Warn("operational alert delivery could not record an attempt")
					}
				} else if sent != nil {
					failed(sent)
					// Mail goes through the operator's own relay, so its failures are the operator's
					// to see. A webhook is its workspace's to fix, and shows in its alerts.
					if continuous && d.channel == "email" && ctx.Err() == nil {
						slog.Warn("operational alert mail delivery failed")
					}
				}
			})
		}
		if !continuous && running == 0 {
			mu.Lock()
			defer mu.Unlock()
			return last
		}
		select {
		case <-ctx.Done():
			attempts.Wait()
			return ctx.Err()
		case <-finished:
			running--
		case <-poll.C:
		}
	}
}

// attempt sends one claimed delivery and records the outcome under its lease. It returns the
// send's error and the record's apart, since only the second is the controller's own failure.
func (s *Service) attempt(ctx context.Context, d delivery) (error, error) {
	attempt, cancel := context.WithTimeout(ctx, attemptDeadline)
	state, err := s.send(attempt, d)
	cancel()
	failure := ""
	if err != nil {
		state = "pending"
		if d.attempt >= 5 {
			state = "failed"
		}
		failure = "The attempt could not be completed."
		var known *deliveryFailure
		if errors.As(err, &known) {
			failure = known.note
		}
	}
	delay := time.Minute * time.Duration(1<<(d.attempt-1))
	return err, s.Store.Queries.FinishAlertDelivery(ctx, db.FinishAlertDeliveryParams{ID: d.id, State: state, NextAttemptAt: s.Now().Add(delay), Attempts: int32(d.attempt), Failure: failure})
}
func (s *Service) send(ctx context.Context, d delivery) (string, error) {
	var p payload
	p.DeliveryID = d.id
	p.AlertID = d.alert
	p.WorkspaceID = d.workspace
	p.State = d.phase
	alert, err := s.Store.Queries.AlertDeliveryContext(ctx, db.AlertDeliveryContextParams{Phase: d.phase, WorkspaceID: d.workspace, ID: d.alert})
	if errors.Is(err, pgx.ErrNoRows) {
		return "cancelled", nil
	}
	if err != nil {
		return "", err
	}
	p.Kind, p.ResourceID, p.Reason, p.OccurredAt = api.AlertKind(alert.Kind), alert.ResourceID, alert.Reason, alert.OccurredAt
	emailEnabled, webhookEnabled, cipher := alert.EmailEnabled, alert.WebhookEnabled, alert.EncryptedWebhook
	if alert.Revision != d.revision || (d.phase == "firing" && alert.State != "firing") || (alert.Acknowledged && d.phase != "resolved") {
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
			return "", &deliveryFailure{"Mail is not configured for this installation."}
		}
		body := fmt.Sprintf("%s\nState: %s\nReason: %s\nWorkspace: %s\nResource: %s\nObserved: %s\nDelivery: %s\n\nOpen Alerts in your workspace dashboard: %s/alerts\n\nThis records an observation. It does not change your workloads.", Title(p.Kind), p.State, p.Reason, p.WorkspaceID, p.ResourceID, p.OccurredAt.UTC().Format(time.RFC3339), p.DeliveryID, s.Origin)
		if err = s.Mail.Send(ctx, address, "Astilba: "+Title(p.Kind), body); err != nil {
			return "", &deliveryFailure{"The mail relay did not accept the message."}
		}
		return "sent", nil
	}
	if !webhookEnabled {
		return "cancelled", nil
	}
	hookItem := secretbroker.Item{Kind: secretbroker.AlertWebhook, ID: d.workspace}
	keys, err := s.Store.Broker.Begin(ctx, secretbroker.Request{Workspace: d.workspace, Purpose: secretbroker.Operate, Principal: secretbroker.System("alert-delivery"), Items: []secretbroker.Item{hookItem}})
	if err != nil {
		return "", err
	}
	raw, err := keys.Open(hookItem, cipher)
	keys.Close(ctx, secretbroker.Done(err))
	if err != nil {
		return "", err
	}
	var hook webhook
	if json.Unmarshal(raw, &hook) != nil {
		return "", errors.New("alert destination unavailable")
	}
	private, err := s.destination(hook.URL)
	if err != nil {
		return "", &deliveryFailure{"The webhook address is no longer allowed. Save the alert settings again."}
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
	client := outbound.Client(private, webhookDeadline)
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "", &deliveryFailure{fmt.Sprintf("The receiver did not answer within %d seconds.", int(webhookDeadline/time.Second))}
	}
	if err != nil {
		return "", &deliveryFailure{"The receiver could not be reached."}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &deliveryFailure{fmt.Sprintf("The receiver answered with HTTP %d.", response.StatusCode)}
	}
	return "sent", nil
}

// deliveryFailure is a failed attempt as the workspace's owners see it on the delivery: what
// happened, in the controller's words, never the destination or what a receiver said.
type deliveryFailure struct{ note string }

func (f *deliveryFailure) Error() string { return f.note }
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
		return "Scheduled backup failed or overdue"
	case api.DeploymentProblem:
		return "Deployment needs review"
	case api.BuildProblem:
		return "Build needs review"
	case api.BackupProblem:
		return "Backup operation needs review"
	case api.NetworkProblem:
		return "Private network updates are failing"
	case api.IdentityConflict:
		return "Another copy of a server's agent key is in use"
	case api.RecoveryWindow:
		return "Cross-host recovery window is shorter than 35 days"
	default:
		return "Operational alert"
	}
}
