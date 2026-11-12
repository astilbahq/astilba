package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"net"
	"os"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
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
	// Troubleshooting declares that one instance serves this installation, which
	// terminal sessions require until a multi-instance relay exists.
	Troubleshooting bool
	// ResetMail shares this River client, since each client must know every job kind it may rescue.
	ResetMail *auth.ResetMailWorker
	// ForkFencing clears a server's credential when management sees proof that a second holder has
	// its key. Off, the proof only raises the owner's alert.
	ForkFencing bool
	nonce       nonceSecret
}

// River stops a job at 10 seconds. A pass ends itself first so that running out of time
// is not a job failure, and no one item can take more than a share of it. Items keep
// their turn order, so the next pass carries on where this one stopped.
const (
	sweepPassBudget = 8 * time.Second
	sweepItemLimit  = 3 * time.Second
)

// sweepFailure drops the outcomes a workspace already shows its owner, leaving only
// failures of the sweep's own work.
func sweepFailure(err error) error {
	var problem *domain.Error
	if errors.As(err, &problem) || errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func New(st *store.Store) (*Service, error) {
	s := &Service{Store: st, Policy: pipeline.Policy{Origins: strings.FieldsFunc(os.Getenv("ASTILBA_INTEGRATION_ORIGINS"), func(r rune) bool { return r == ',' })}}
	workers := river.NewWorkers()
	river.AddWorker(workers, &domainWorker{service: s})
	river.AddWorker(workers, &networkSweepWorker{service: s})
	river.AddWorker(workers, &coordinatedScheduleWorker{service: s})
	river.AddWorker(workers, &previewWorker{service: s})
	river.AddWorker(workers, &previewSweepWorker{service: s})
	river.AddWorker(workers, &troubleshootingSweepWorker{service: s})
	river.AddWorker(workers, &worker{service: s})
	river.AddWorker(workers, &refreshWorker{service: s})
	river.AddWorker(workers, &automaticWorker{service: s})
	river.AddWorker(workers, &githubFanoutWorker{service: s})
	river.AddWorker(workers, &githubReconcileWorker{service: s})
	s.ResetMail = &auth.ResetMailWorker{}
	river.AddWorker(workers, s.ResetMail)
	q, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{PeriodicJobs: []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(5*time.Second), func() (river.JobArgs, *river.InsertOpts) { return networkSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}), river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) { return coordinatedScheduleArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}), river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) { return previewSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}), river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) { return troubleshootingSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true})}, Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}, gitQueue: {MaxWorkers: gitQueueWorkers}, auth.ResetMailQueue: {MaxWorkers: 4}}, JobTimeout: 10 * time.Second})
	s.Queue = q
	return s, err
}
func (s *Service) Start(ctx context.Context) error { return s.Queue.Start(ctx) }
func (s *Service) Stop(ctx context.Context) error  { return s.Queue.Stop(ctx) }
func hash(s string) []byte                         { h := sha256.Sum256([]byte(s)); return h[:] }
func Hash(v any) string                            { return stablejson.Hash(v) }
func Terminal(s api.ExecutionState) bool {
	return s == "succeeded" || s == "failed" || s == "cancelled"
}

type workspaceAccess struct {
	Write, Owner, Recent, Recovery bool
}

func (s *Service) scope(ctx context.Context, p store.Principal, ws uuid.UUID, access workspaceAccess, action string, id uuid.UUID, fn func(pgx.Tx) error) error {
	return s.scopeIn(ctx, p, ws, access, action, id, func(tx pgx.Tx, _ store.Workspace) error { return fn(tx) })
}

// scopeIn is scope for work that needs the admitted workspace, such as the caller's role in it.
func (s *Service) scopeIn(ctx context.Context, p store.Principal, ws uuid.UUID, access workspaceAccess, action string, id uuid.UUID, fn func(pgx.Tx, store.Workspace) error) error {
	fnScope := func(tx pgx.Tx, q *db.Queries, w store.Workspace) error {
		if err := fn(tx, w); err != nil {
			return err
		}
		if action != "" {
			return store.Audit(ctx, q, p, ws, action, id)
		}
		return nil
	}
	admission := store.WorkspaceAccess{Write: access.Write, Owner: access.Owner, Recent: access.Recent}
	if access.Recovery {
		return s.Store.InWorkspaceRecovery(ctx, p, ws, admission, fnScope)
	}
	return s.Store.InWorkspaceTx(ctx, p, ws, admission, fnScope)
}

// serverView maps the server columns shared by ListServers, InsertServer and
// RenewServerEnrollment; their generated row types convert to ListServersRow.
func serverView(row db.ListServersRow) (api.Server, error) {
	v := api.Server{Id: row.ID, Name: row.Name, Role: api.ServerRole(row.Role), LastSeen: row.LastSeen, RotationRequested: row.RotationRequested, TransferRequired: &row.AwaitingTransfer, PreviewOnly: &row.PreviewOnly}
	if len(row.Capabilities) > 0 {
		if err := json.Unmarshal(row.Capabilities, &v.Capabilities); err != nil {
			return v, err
		}
	}
	if len(row.Preflight) > 0 {
		if err := json.Unmarshal(row.Preflight, &v.Preflight); err != nil {
			return v, err
		}
	}
	switch {
	case row.Revoked:
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

func (s *Service) Servers(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.Server, error) {
	out := []api.Server{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		q := db.New(tx)
		rows, err := q.ListServers(ctx, ws)
		if err != nil {
			return err
		}
		identities, err := identityViews(ctx, q, ws)
		if err != nil {
			return err
		}
		for _, row := range rows {
			v, e := serverView(row)
			if e != nil {
				return e
			}
			v.Identity = identities[row.ID]
			out = append(out, v)
		}
		return nil
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
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "server.create", id, func(tx pgx.Tx) error {
		q := db.New(tx)
		count, e := q.CountServers(ctx, ws)
		if e != nil {
			return e
		}
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, uuid.Nil); e != nil {
			return e
		}
		if count >= 100 {
			return domain.Conflict
		}
		row, e := q.InsertServer(ctx, db.InsertServerParams{ID: id, WorkspaceID: ws, Name: name, Role: string(in.Role), EnrollmentHash: hash(out.Token), EnrollmentExpires: &out.ExpiresAt, EnrollmentIssuerID: p.Account.ID, EnrollmentIssuerAuthVersion: p.Account.AuthVersion, PreviewOnly: in.PreviewOnly != nil && *in.PreviewOnly})
		if e != nil {
			return e
		}
		out.Server, e = serverView(db.ListServersRow(row))
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
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "server.enrollment", id, func(tx pgx.Tx) error {
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, id); e != nil {
			return e
		}
		if held, e := db.New(tx).RevokedServerInActiveRoot(ctx, db.RevokedServerInActiveRootParams{WorkspaceID: ws, ServerID: id}); e != nil {
			return e
		} else if held {
			return domain.Conflict
		}
		if e := invalidateNetworkMember(ctx, tx, ws, id, memberRenewedNote); e != nil {
			return e
		}
		row, e := db.New(tx).RenewServerEnrollment(ctx, db.RenewServerEnrollmentParams{WorkspaceID: ws, ID: id, EnrollmentHash: hash(out.Token), EnrollmentExpires: &out.ExpiresAt, EnrollmentIssuerID: p.Account.ID, EnrollmentIssuerAuthVersion: p.Account.AuthVersion})
		if e != nil {
			return e
		}
		out.Server, e = serverView(db.ListServersRow(row))
		return e
	})
	return out, err
}
func (s *Service) Control(ctx context.Context, p store.Principal, ws, id uuid.UUID, revoke bool) error {
	action := "server.rotate"
	if revoke {
		action = "server.revoke"
	}
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: revoke}, action, id, func(tx pgx.Tx) error {
		if revoke {
			if e := invalidateNetworkMember(ctx, tx, ws, id, memberRevokedNote); e != nil {
				return e
			}
			if e := store.LockServerDNS(ctx, tx, id); e != nil {
				return e
			}
		}
		q := db.New(tx)
		if !revoke {
			// An owner's rotation replaces the agent's key. A server still on a bearer can't rotate it
			// safely, so it is told to update its agent, which then registers a key.
			bound, err := q.RequestKeyRotation(ctx, db.RequestKeyRotationParams{WorkspaceID: ws, ID: id})
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.NotFound
			} else if err != nil {
				return err
			} else if !bound {
				return keyRotationNeedsUpdate
			}
			return nil
		}
		n, e := q.RevokeServer(ctx, db.RevokeServerParams{WorkspaceID: ws, ID: id})
		if e != nil {
			return e
		}
		if n != 1 {
			return domain.NotFound
		}
		return s.settleRevokedWork(ctx, tx, ws, id)
	})
}

// lockAgentCredential locks the agent's server row and reports whether its
// credential is still valid and the server is not awaiting controller transfer.
func lockAgentCredential(ctx context.Context, tx pgx.Tx, a Identity) (bool, error) {
	return db.New(tx).LockAgentCredential(ctx, db.LockAgentCredentialParams{WorkspaceID: a.Workspace, ServerID: a.ID, CredentialHash: a.Credential, Lineage: a.lineage()})
}

const (
	identityBearer = "bearer"
	identityToken  = "token"
)

// Agent identity is bound by a server ID plus a credential, never a tenant selector: a legacy
// 256-bit bearer, or an access token issued to a session of the server's key.
type Identity struct {
	ID, Workspace uuid.UUID
	Role          api.ServerRole
	Previous      bool
	// Token is what the agent presented, so a long request can check it again.
	Token string
	Kind  string
	// Credential is the server's identity this request authenticated as: the bearer's hash, or the
	// hash of the key the token was issued for.
	Credential                 []byte
	Lineage                    uuid.UUID
	CredentialFreeVisualCancel bool
}

// lineage scopes credential checks to a token's session; a bearer has none.
func (a Identity) lineage() *uuid.UUID {
	if a.Kind != identityToken {
		return nil
	}
	return &a.Lineage
}

func (s *Service) Authenticate(ctx context.Context, id uuid.UUID, token string) (Identity, error) {
	var v Identity
	v.ID = id
	v.Token = token
	if strings.HasPrefix(token, agentidentity.TokenPrefix) {
		if len(token) != len(agentidentity.TokenPrefix)+43 {
			return v, domain.Unauthenticated
		}
		row, err := s.Store.Queries.AuthenticateAgentToken(ctx, db.AuthenticateAgentTokenParams{ServerID: id, TokenHash: hash(token)})
		if errors.Is(err, pgx.ErrNoRows) {
			return v, keyNotCurrent
		}
		v.Kind, v.Workspace, v.Role, v.Credential, v.Lineage, v.CredentialFreeVisualCancel = identityToken, row.WorkspaceID, api.ServerRole(row.Role), row.KeyHash, row.Lineage, row.CredentialFreeVisualCancel
		return v, err
	}
	if len(token) != 43 {
		return v, domain.Unauthenticated
	}
	row, err := s.Store.Queries.AuthenticateAgent(ctx, db.AuthenticateAgentParams{ServerID: id, CredentialHash: hash(token)})
	v.Kind, v.Credential = identityBearer, hash(token)
	v.Workspace, v.Role, v.Previous, v.CredentialFreeVisualCancel = row.WorkspaceID, api.ServerRole(row.Role), row.Previous, row.CredentialFreeVisualCancel
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.Unauthenticated
	}
	return v, err
}
func (s *Service) Enroll(ctx context.Context, in api.AgentEnrollment, source string) (api.AgentStatus, error) {
	var out api.AgentStatus
	keyed := in.PublicKey != nil || in.Proof != nil
	legacy := in.Credential != nil
	if in.MachineId == uuid.Nil || keyed == legacy || (legacy && len(*in.Credential) != 43) || (keyed && (in.PublicKey == nil || in.Proof == nil)) {
		return out, domain.Invalid("credential")
	}
	return s.enroll(ctx, in, source)
}

func (s *Service) Heartbeat(ctx context.Context, a Identity, in api.AgentHeartbeat) (api.AgentStatus, error) {
	var out api.AgentStatus
	caps, _ := stablejson.Marshal(in.Capabilities)
	pre, _ := stablejson.Marshal(in.Preflight)
	row, err := s.Store.Queries.RecordAgentHeartbeat(ctx, db.RecordAgentHeartbeatParams{WorkspaceID: a.Workspace, ServerID: a.ID, CredentialHash: a.Credential, Capabilities: caps, Preflight: pre, Lineage: a.lineage()})
	out.Role, out.RotationRequested = api.ServerRole(row.Role), row.RotationRequested
	if row.PreviewOnly {
		out.PreviewOnly = &row.PreviewOnly
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.Unauthenticated
	}
	return out, err
}

// Rotate exchanges the agent's credential for a candidate it chose. The caller must also name the
// machine that enrolled. That stops someone holding only a leaked credential, but the machine ID
// isn't secret, so this is routine upkeep: after a suspected leak an owner revokes the server and
// enrolls a new one.
func (s *Service) Rotate(ctx context.Context, a Identity, in api.AgentRotation) error {
	// A key-bound server rotates only through RotateKey, which needs both keys.
	if a.Kind != identityBearer {
		return domain.Forbidden
	}
	if len(in.Credential) != 43 {
		return domain.Invalid("credential")
	}
	n, err := s.Store.Queries.RotateAgentCredential(ctx, db.RotateAgentCredentialParams{WorkspaceID: a.Workspace, ServerID: a.ID, CredentialHash: a.Credential, CandidateHash: hash(in.Credential), MachineID: &in.MachineId})
	if err == nil && n != 1 {
		return domain.Unauthenticated
	}
	return err
}
