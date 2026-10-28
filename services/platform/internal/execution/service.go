package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"net"
	"os"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const TestImage = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
const PollDuration = 25 * time.Second
const OfflineAfter = 45 * time.Second
const RotationOverlap = 2 * time.Minute

type Service struct {
	DNS         *applicationdns.Cloudflare
	DNSResolver *net.Resolver
	GitHub      *githubapp.Client
	Policy      pipeline.Policy
	Store       *store.Store
	Queue       *river.Client[pgx.Tx]
	Releases    []api.AgentRelease
}

func New(st *store.Store) (*Service, error) {
	s := &Service{Store: st, Policy: pipeline.Policy{Origins: strings.FieldsFunc(os.Getenv("ASTILBA_INTEGRATION_ORIGINS"), func(r rune) bool { return r == ',' })}}
	workers := river.NewWorkers()
	river.AddWorker(workers, &domainWorker{service: s})
	river.AddWorker(workers, &networkSweepWorker{service: s})
	river.AddWorker(workers, &coordinatedScheduleWorker{service: s})
	river.AddWorker(workers, &previewWorker{service: s})
	river.AddWorker(workers, &previewSweepWorker{service: s})
	river.AddWorker(workers, &worker{service: s})
	river.AddWorker(workers, &refreshWorker{service: s})
	river.AddWorker(workers, &automaticWorker{service: s})
	river.AddWorker(workers, &githubFanoutWorker{service: s})
	river.AddWorker(workers, &githubReconcileWorker{service: s})
	q, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{PeriodicJobs: []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(5*time.Second), func() (river.JobArgs, *river.InsertOpts) { return networkSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}), river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) { return coordinatedScheduleArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}), river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) { return previewSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true})}, Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}}, JobTimeout: 10 * time.Second})
	s.Queue = q
	return s, err
}
func (s *Service) Start(ctx context.Context) error { return s.Queue.Start(ctx) }
func (s *Service) Stop(ctx context.Context) error  { return s.Queue.Stop(ctx) }
func hash(s string) []byte                         { h := sha256.Sum256([]byte(s)); return h[:] }
func Hash(v any) string                            { b, _ := json.Marshal(v); return hex.EncodeToString(hash(string(b))) }
func Terminal(s api.ExecutionState) bool {
	return s == "succeeded" || s == "failed" || s == "cancelled"
}
func (s *Service) scope(ctx context.Context, p store.Principal, ws uuid.UUID, write, owner, recent bool, action string, id uuid.UUID, fn func(pgx.Tx) error) error {
	fnScope := func(tx pgx.Tx, q *db.Queries, _ store.Workspace) error {
		if err := fn(tx); err != nil {
			return err
		}
		if action != "" {
			return store.Audit(ctx, q, p, ws, action, id)
		}
		return nil
	}
	if action == "operation.cancel" || action == "server.revoke" || action == "upgrade.recover" || action == "backup.recover" || action == "workspace.transfer" || action == "preview.cleanup" {
		return s.Store.InWorkspaceRecovery(ctx, p, ws, owner, recent, fnScope)
	}
	return s.Store.InWorkspaceTx(ctx, p, ws, write, owner, recent, fnScope)
}
func serverScan(row pgx.Row) (api.Server, error) {
	var v api.Server
	var cap, pre []byte
	var revoked bool
	err := row.Scan(&v.Id, &v.Name, &v.Role, &v.LastSeen, &cap, &pre, &revoked, &v.RotationRequested, &v.TransferRequired, &v.PreviewOnly)
	if err != nil {
		return v, err
	}
	if len(cap) > 0 {
		if err = json.Unmarshal(cap, &v.Capabilities); err != nil {
			return v, err
		}
	}
	if len(pre) > 0 {
		if err = json.Unmarshal(pre, &v.Preflight); err != nil {
			return v, err
		}
	}
	switch {
	case revoked:
		v.Status = "revoked"
	case v.LastSeen == nil:
		v.Status = "pending"
	case time.Since(*v.LastSeen) > OfflineAfter:
		v.Status = "offline"
	default:
		v.Status = "online"
	}
	return v, nil
}

const serverColumns = "id,name,role,last_seen,capabilities,preflight,revoked,rotation_requested,awaiting_transfer,preview_only"

func (s *Service) Servers(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.Server, error) {
	out := []api.Server{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+serverColumns+" FROM servers WHERE workspace_id=$1 ORDER BY name,id LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := serverScan(rows)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) CreateServer(ctx context.Context, p store.Principal, ws uuid.UUID, in api.CreateServer) (api.Enrollment, error) {
	out := api.Enrollment{ExpiresAt: time.Now().Add(10 * time.Minute)}
	name, err := domain.Name(in.Name)
	if err != nil {
		return out, err
	}
	if in.PreviewOnly != nil && *in.PreviewOnly && in.Role != "combined" {
		return out, domain.Invalid("preview.serverRole")
	}
	if in.Role != "deployment" && in.Role != "build" && in.Role != "combined" {
		return out, domain.Invalid("role")
	}
	out.Token, err = cryptobox.Token()
	if err != nil {
		return out, err
	}
	id := uuid.New()
	err = s.scope(ctx, p, ws, true, true, true, "server.create", id, func(tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM servers WHERE workspace_id=$1", ws).Scan(&count); e != nil {
			return e
		}
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, uuid.Nil); e != nil {
			return e
		}
		if count >= 100 {
			return domain.Conflict
		}
		_, e := tx.Exec(ctx, `INSERT INTO servers(id,workspace_id,name,role,enrollment_hash,enrollment_expires,preview_only) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, ws, name, in.Role, hash(out.Token), out.ExpiresAt, in.PreviewOnly != nil && *in.PreviewOnly)
		if e != nil {
			return e
		}
		out.Server, e = serverScan(tx.QueryRow(ctx, "SELECT "+serverColumns+" FROM servers WHERE id=$1", id))
		return e
	})
	return out, err
}
func (s *Service) RenewEnrollment(ctx context.Context, p store.Principal, ws, id uuid.UUID) (api.Enrollment, error) {
	out := api.Enrollment{ExpiresAt: time.Now().Add(10 * time.Minute)}
	var err error
	out.Token, err = cryptobox.Token()
	if err != nil {
		return out, err
	}
	err = s.scope(ctx, p, ws, true, true, true, "server.enrollment", id, func(tx pgx.Tx) error {
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, id); e != nil {
			return e
		}
		if e := invalidateNetworkMember(ctx, tx, ws, id); e != nil {
			return e
		}
		row := tx.QueryRow(ctx, `UPDATE servers SET enrollment_hash=$3,enrollment_expires=$4,credential_hash=NULL,previous_hash=NULL,previous_until=NULL,revoked=false,rotation_requested=false WHERE workspace_id=$1 AND id=$2 RETURNING `+serverColumns, ws, id, hash(out.Token), out.ExpiresAt)
		out.Server, err = serverScan(row)
		return err
	})
	return out, err
}
func (s *Service) Control(ctx context.Context, p store.Principal, ws, id uuid.UUID, revoke bool) error {
	action := "server.rotate"
	if revoke {
		action = "server.revoke"
	}
	return s.scope(ctx, p, ws, true, true, true, action, id, func(tx pgx.Tx) error {
		if revoke {
			if e := invalidateNetworkMember(ctx, tx, ws, id); e != nil {
				return e
			}
			if e := store.LockServerDNS(ctx, tx, id); e != nil {
				return e
			}
		}
		command := `UPDATE servers SET rotation_requested=true WHERE workspace_id=$1 AND id=$2 AND NOT revoked AND credential_hash IS NOT NULL`
		if revoke {
			command = `UPDATE servers SET revoked=true,credential_hash=NULL,previous_hash=NULL,enrollment_hash=NULL,rotation_requested=false WHERE workspace_id=$1 AND id=$2`
		}
		n, e := tx.Exec(ctx, command, ws, id)
		if e != nil {
			return e
		}
		if n.RowsAffected() != 1 {
			return domain.NotFound
		}
		return nil
	})
}

// Agent identity is bound by a server ID plus a random 256-bit credential, never a tenant selector.
type Identity struct {
	ID, Workspace              uuid.UUID
	Role                       api.ServerRole
	Previous                   bool
	Token                      string
	CredentialFreeVisualCancel bool
}

func (s *Service) Authenticate(ctx context.Context, id uuid.UUID, token string) (Identity, error) {
	var v Identity
	v.ID = id
	v.Token = token
	if len(token) != 43 {
		return v, domain.Unauthenticated
	}
	err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id,role,(credential_hash<>$2),COALESCE((capabilities->>'credentialFreeVisualCancel')::boolean,false) FROM servers WHERE id=$1 AND NOT revoked AND NOT awaiting_transfer AND (credential_hash=$2 OR (previous_hash=$2 AND previous_until>now()))`, id, hash(token)).Scan(&v.Workspace, &v.Role, &v.Previous, &v.CredentialFreeVisualCancel)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.Unauthenticated
	}
	return v, err
}
func (s *Service) Enroll(ctx context.Context, in api.AgentEnrollment) (api.AgentStatus, error) {
	var out api.AgentStatus
	if len(in.Credential) != 43 || in.MachineId == uuid.Nil {
		return out, domain.Invalid("credential")
	}
	return s.enroll(ctx, in)
}

func (s *Service) Heartbeat(ctx context.Context, a Identity, in api.AgentHeartbeat) (api.AgentStatus, error) {
	var out api.AgentStatus
	var previewOnly bool
	caps, _ := json.Marshal(in.Capabilities)
	pre, _ := json.Marshal(in.Preflight)
	err := s.Store.Pool.QueryRow(ctx, `UPDATE servers SET last_seen=now(),capabilities=$3,preflight=$4 WHERE id=$1 AND NOT revoked AND NOT awaiting_transfer AND (credential_hash=$2 OR (previous_hash=$2 AND previous_until>now())) RETURNING role,rotation_requested,preview_only`, a.ID, hash(a.Token), caps, pre).Scan(&out.Role, &out.RotationRequested, &previewOnly)
	if previewOnly {
		out.PreviewOnly = &previewOnly
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.Unauthenticated
	}
	return out, err
}
func (s *Service) Rotate(ctx context.Context, a Identity, candidate string) error {
	if len(candidate) != 43 {
		return domain.Invalid("credential")
	}
	// The same candidate is an idempotent retry. A previous credential cannot initiate a new exchange.
	n, err := s.Store.Pool.Exec(ctx, `UPDATE servers SET previous_hash=CASE WHEN credential_hash=$3 THEN previous_hash ELSE credential_hash END,previous_until=CASE WHEN credential_hash=$3 THEN previous_until ELSE now()+interval '2 minutes' END,credential_hash=$3,rotation_requested=false WHERE id=$1 AND NOT revoked AND NOT awaiting_transfer AND ((credential_hash=$2 AND rotation_requested) OR (credential_hash=$3 AND (credential_hash=$2 OR (previous_hash=$2 AND previous_until>now()))))`, a.ID, hash(a.Token), hash(candidate))
	if err == nil && n.RowsAffected() != 1 {
		return domain.Unauthenticated
	}
	return err
}
