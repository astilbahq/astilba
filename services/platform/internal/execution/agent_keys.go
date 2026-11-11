package execution

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"sync"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	agentTokenLifetime = 15 * time.Minute
	agentNonceLifetime = 60 * time.Second
	agentKeyMaxAge     = 30 * 24 * time.Hour
	// A retired bearer proves a second holder only once the bound key has been in use for longer
	// than any request can take, and only for as long as an agent that never updated would still
	// be presenting it.
	retiredBearerFrom  = 2 * time.Minute
	retiredBearerUntil = 24 * time.Hour
)

// Refusals tell an agent why its credential was refused. Each is a 401.
var (
	keyNotCurrent          = &domain.Error{Code: agentidentity.KeyNotCurrent, Message: "This key is not the server's current key."}
	nonceInvalid           = &domain.Error{Code: agentidentity.NonceInvalid, Message: "Ask for a new challenge."}
	counterStale           = &domain.Error{Code: agentidentity.CounterStale, Message: "Start a new session."}
	refreshReused          = &domain.Error{Code: agentidentity.RefreshReused, Message: "Start a new session."}
	codeNotAccepted        = &domain.Error{Code: agentidentity.CodeNotAccepted, Message: "This enrollment code was not accepted."}
	keyRotationNeedsUpdate = &domain.Error{Code: "conflict", Message: "This server's agent authenticates with a credential that can't be rotated safely. Update the agent; it then registers a key."}
)

// Fork signals: each is proof that a second holder has this server's key in the current epoch.
const (
	signalSupersededKey = "superseded_key"
	signalCounter       = "start_counter"
	signalRetiredBearer = "retired_bearer"
	signalBindKey       = "bind_other_key"
	signalCode          = "code_other_key"
)

type nonceSecret struct {
	once   sync.Mutex
	secret []byte
}

func (s *Service) nonceKey(ctx context.Context) ([]byte, error) {
	s.nonce.once.Lock()
	defer s.nonce.once.Unlock()
	if s.nonce.secret != nil {
		return s.nonce.secret, nil
	}
	q := s.Store.Queries
	secret, err := q.InstallationSecret(ctx, "agent_nonce")
	if errors.Is(err, pgx.ErrNoRows) {
		fresh := make([]byte, 32)
		if _, err = rand.Read(fresh); err != nil {
			return nil, err
		}
		// Every replica keeps whichever secret was stored first.
		if err = q.InsertInstallationSecret(ctx, db.InsertInstallationSecretParams{Name: "agent_nonce", Secret: fresh}); err != nil {
			return nil, err
		}
		secret, err = q.InstallationSecret(ctx, "agent_nonce")
	}
	if err != nil {
		return nil, err
	}
	s.nonce.secret = secret
	return secret, nil
}

type agentNonce struct {
	server uuid.UUID
	seq    int64
	issued time.Time
	hash   []byte
}

const nonceBody = 16 + 8 + 8 + 16

func nonceMAC(secret, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("astilba agent nonce v1"))
	mac.Write(body)
	return mac.Sum(nil)
}

// Challenge issues a nonce for a server. It stores nothing and checks nothing, so a stranger who
// asks gains nothing and costs the server nothing.
func (s *Service) Challenge(ctx context.Context, server uuid.UUID) (api.AgentChallenge, error) {
	secret, err := s.nonceKey(ctx)
	if err != nil {
		return api.AgentChallenge{}, err
	}
	row, err := s.Store.Queries.NextAgentNonce(ctx)
	if err != nil {
		return api.AgentChallenge{}, err
	}
	body := make([]byte, nonceBody)
	copy(body, server[:])
	binary.BigEndian.PutUint64(body[16:], uint64(row.Seq))
	binary.BigEndian.PutUint64(body[24:], uint64(row.NowMs))
	if _, err = rand.Read(body[32:]); err != nil {
		return api.AgentChallenge{}, err
	}
	issued := time.UnixMilli(row.NowMs)
	return api.AgentChallenge{Nonce: base64.RawURLEncoding.EncodeToString(append(body, nonceMAC(secret, body)...)), ExpiresAt: issued.Add(agentNonceLifetime)}, nil
}

func (s *Service) parseNonce(ctx context.Context, server uuid.UUID, encoded string) (agentNonce, error) {
	secret, err := s.nonceKey(ctx)
	if err != nil {
		return agentNonce{}, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != nonceBody+sha256.Size || !hmac.Equal(raw[nonceBody:], nonceMAC(secret, raw[:nonceBody])) || !bytes.Equal(raw[:16], server[:]) {
		return agentNonce{}, nonceInvalid
	}
	sum := sha256.Sum256(raw)
	return agentNonce{server: server, seq: int64(binary.BigEndian.Uint64(raw[16:])), issued: time.UnixMilli(int64(binary.BigEndian.Uint64(raw[24:]))), hash: sum[:]}, nil
}

// redeem consumes a nonce inside the transaction that acts on it. Only a known key's valid
// signature gets this far, so strangers can't fill the table.
func redeem(ctx context.Context, q *db.Queries, n agentNonce) error {
	now, err := q.DatabaseNowMillis(ctx)
	if err != nil {
		return err
	}
	expires := n.issued.Add(agentNonceLifetime)
	if time.UnixMilli(now).After(expires) {
		return nonceInvalid
	}
	consumed, err := q.ConsumeAgentNonce(ctx, db.ConsumeAgentNonceParams{NonceHash: n.hash, ExpiresAt: expires})
	if err != nil {
		return err
	}
	if consumed != 1 {
		return nonceInvalid
	}
	return nil
}

// identityEvent records what happened to a server's identity. Refusals are recorded once per key
// and epoch, so a host that keeps retrying doesn't flood the record.
func identityEvent(ctx context.Context, q *db.Queries, ws, server uuid.UUID, epoch int64, kind, signal string, key []byte, source string) error {
	if key == nil {
		key = []byte{}
	}
	return q.InsertAgentIdentityEvent(ctx, db.InsertAgentIdentityEventParams{WorkspaceID: ws, ID: uuid.New(), ServerID: server, Epoch: epoch, Kind: kind, Signal: signal, KeyHash: key, Source: truncate(source, 100)})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// conflict records proof of a second holder. The owner is alerted. Fencing, which also clears the
// credential so neither holder can act until the owner re-keys, happens only where the
// installation has turned it on.
func (s *Service) conflict(ctx context.Context, q *db.Queries, ws, server uuid.UUID, epoch int64, signal string, key []byte, source string) error {
	detail, err := stablejson.Marshal(map[string]string{"signal": signal, "source": truncate(source, 100)})
	if err != nil {
		return err
	}
	if err = q.RecordIdentityConflict(ctx, db.RecordIdentityConflictParams{WorkspaceID: ws, ServerID: server, Detail: detail}); err != nil {
		return err
	}
	if err = identityEvent(ctx, q, ws, server, epoch, "conflict", signal, key, source); err != nil {
		return err
	}
	if !s.ForkFencing {
		return nil
	}
	if err = q.FenceServerIdentity(ctx, db.FenceServerIdentityParams{WorkspaceID: ws, ServerID: server}); err != nil {
		return err
	}
	if err = q.SupersedeAgentLineages(ctx, server); err != nil {
		return err
	}
	return identityEvent(ctx, q, ws, server, epoch, "fenced", signal, key, source)
}

// commitRefusal keeps what a refused request recorded, then refuses it.
func commitRefusal(ctx context.Context, tx pgx.Tx, refusal error) error {
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return refusal
}

// ColdStart starts a session for a key the server holds: one live lineage per server.
func (s *Service) ColdStart(ctx context.Context, in api.AgentTokenRequest, source string) (api.AgentToken, error) {
	var out api.AgentToken
	public, der, err := agentidentity.ParsePublicKey(in.PublicKey)
	if err != nil {
		return out, keyNotCurrent
	}
	n, err := s.parseNonce(ctx, in.ServerId, in.Nonce)
	if err != nil {
		return out, err
	}
	keyHash := agentidentity.KeyHash(der)
	// A key this server never had costs nothing: no lock, no record.
	key, err := s.Store.Queries.AgentKey(ctx, db.AgentKeyParams{ServerID: in.ServerId, KeyHash: keyHash})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !agentidentity.Verify(public, agentidentity.TokenMessage(in.ServerId, in.Nonce, in.StartCounter), in.Signature)) {
		return out, keyNotCurrent
	}
	if err != nil {
		return out, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	server, err := q.LockServerIdentity(ctx, in.ServerId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, keyNotCurrent
		}
		return out, err
	}
	if err = redeem(ctx, q, n); err != nil {
		return out, err
	}
	live := !server.Revoked && !server.AwaitingTransfer && server.CredentialHash != nil
	if !live {
		return out, keyNotCurrent
	}
	ws, epoch := server.WorkspaceID, server.EnrollmentEpoch
	if !bytes.Equal(server.CredentialHash, keyHash) {
		// F1: a key rotation superseded, presented after its successor was used, by a request
		// whose nonce came after that use. An honest agent holds the successor by then.
		if key.Epoch == epoch && key.SupersededAt != nil && key.SuccessorSeenSeq.Valid && n.seq > key.SuccessorSeenSeq.Int64 {
			if err = s.conflict(ctx, q, ws, in.ServerId, epoch, signalSupersededKey, keyHash, source); err != nil {
				return out, err
			}
			return out, commitRefusal(ctx, tx, keyNotCurrent)
		}
		if key.Epoch == epoch {
			if err = identityEvent(ctx, q, ws, in.ServerId, epoch, "refused", signalSupersededKey, keyHash, source); err != nil {
				return out, err
			}
			return out, commitRefusal(ctx, tx, keyNotCurrent)
		}
		return out, keyNotCurrent
	}
	// F2: one holder's start counters rise with its nonces.
	counter, err := q.CounterConflict(ctx, db.CounterConflictParams{ServerID: in.ServerId, Epoch: epoch, Value: in.StartCounter, NonceSeq: n.seq, NonceHash: n.hash})
	if err != nil {
		return out, err
	}
	if counter.Conflict {
		if err = s.conflict(ctx, q, ws, in.ServerId, epoch, signalCounter, keyHash, source); err != nil {
			return out, err
		}
		if s.ForkFencing {
			return out, commitRefusal(ctx, tx, keyNotCurrent)
		}
	} else if counter.Stale {
		// A delayed duplicate from the same holder: its later attempt already arrived.
		if err = identityEvent(ctx, q, ws, in.ServerId, epoch, "refused", signalCounter, keyHash, source); err != nil {
			return out, err
		}
		return out, commitRefusal(ctx, tx, counterStale)
	}
	if err = q.RecordAgentCounter(ctx, db.RecordAgentCounterParams{ServerID: in.ServerId, Epoch: epoch, Value: in.StartCounter, NonceSeq: n.seq, NonceHash: n.hash}); err != nil {
		return out, err
	}
	if err = q.PruneAgentCounters(ctx, db.PruneAgentCountersParams{ServerID: in.ServerId, Epoch: epoch}); err != nil {
		return out, err
	}
	if key.FirstUsedAt == nil {
		if err = q.MarkAgentKeyUsed(ctx, db.MarkAgentKeyUsedParams{ServerID: in.ServerId, KeyHash: keyHash}); err != nil {
			return out, err
		}
		if err = q.MarkSuccessorSeen(ctx, db.MarkSuccessorSeenParams{Seq: pgtype.Int8{Int64: n.seq, Valid: true}, ServerID: in.ServerId, Epoch: epoch}); err != nil {
			return out, err
		}
	}
	if err = q.SupersedeAgentLineages(ctx, in.ServerId); err != nil {
		return out, err
	}
	if err = q.PruneAgentLineages(ctx, in.ServerId); err != nil {
		return out, err
	}
	out, err = s.issueTokens(ctx, q, ws, in.ServerId, keyHash, epoch, server.Role, server.PreviewOnly, server.KeyRotatedAt, server.RotateKeyRequested)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Service) issueTokens(ctx context.Context, q *db.Queries, ws, server uuid.UUID, keyHash []byte, epoch int64, role string, previewOnly bool, rotatedAt *time.Time, rotateRequested bool) (api.AgentToken, error) {
	out := api.AgentToken{Role: api.ServerRole(role), ExpiresAt: time.Now().Add(agentTokenLifetime)}
	secret, err := cryptobox.Token()
	if err != nil {
		return out, err
	}
	refresh, err := cryptobox.Token()
	if err != nil {
		return out, err
	}
	out.Token, out.Refresh = agentidentity.TokenPrefix+secret, refresh
	if previewOnly {
		out.PreviewOnly = &previewOnly
	}
	if err = q.InsertAgentLineage(ctx, db.InsertAgentLineageParams{ID: uuid.New(), WorkspaceID: ws, ServerID: server, KeyHash: keyHash, Epoch: epoch, RefreshHash: hash(refresh), TokenHash: hash(out.Token), TokenExpiresAt: out.ExpiresAt}); err != nil {
		return out, err
	}
	due := rotateRequested || rotatedAt == nil || time.Since(*rotatedAt) > agentKeyMaxAge
	if due {
		// A rotation ends any terminal session attached through the old key, so it waits for them.
		attached, err := q.TroubleshootingAttached(ctx, server)
		if err != nil {
			return out, err
		}
		out.RotateKey = !attached
	}
	return out, nil
}

// Refresh renews a live session's token. Anything ambiguous, such as a reused or superseded
// secret, is refused without proving anything: the agent starts a new session instead.
func (s *Service) Refresh(ctx context.Context, in api.AgentRefreshRequest, source string) (api.AgentToken, error) {
	var out api.AgentToken
	presented := hash(in.Refresh)
	lineage, err := s.Store.Queries.RefreshLineage(ctx, db.RefreshLineageParams{ServerID: in.ServerId, RefreshHash: presented})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, refreshReused
	}
	if err != nil {
		return out, err
	}
	public, _, err := agentidentity.ParsePublicKey(base64.RawURLEncoding.EncodeToString(lineage.PublicKey))
	if err != nil || !agentidentity.Verify(public, agentidentity.RefreshMessage(in.ServerId, in.Refresh), in.Signature) {
		return out, refreshReused
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	server, err := q.LockServerIdentity(ctx, in.ServerId)
	if err != nil {
		return out, err
	}
	live := !server.Revoked && !server.AwaitingTransfer && bytes.Equal(server.CredentialHash, lineage.KeyHash) && lineage.Epoch == server.EnrollmentEpoch
	if !live || lineage.SupersededAt != nil || !(lineage.Current || lineage.Grace) {
		if live {
			if err = identityEvent(ctx, q, server.WorkspaceID, in.ServerId, server.EnrollmentEpoch, "refused", "refresh", lineage.KeyHash, source); err != nil {
				return out, err
			}
			return out, commitRefusal(ctx, tx, refreshReused)
		}
		return out, refreshReused
	}
	secret, err := cryptobox.Token()
	if err != nil {
		return out, err
	}
	refresh, err := cryptobox.Token()
	if err != nil {
		return out, err
	}
	out = api.AgentToken{Token: agentidentity.TokenPrefix + secret, Refresh: refresh, ExpiresAt: time.Now().Add(agentTokenLifetime), Role: api.ServerRole(server.Role)}
	if server.PreviewOnly {
		out.PreviewOnly = &server.PreviewOnly
	}
	n, err := q.RotateLineageTokens(ctx, db.RotateLineageTokensParams{ID: lineage.ID, Presented: presented, RefreshHash: hash(refresh), TokenHash: hash(out.Token), TokenExpiresAt: out.ExpiresAt})
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, refreshReused
	}
	if server.RotateKeyRequested || server.KeyRotatedAt == nil || time.Since(*server.KeyRotatedAt) > agentKeyMaxAge {
		attached, err := q.TroubleshootingAttached(ctx, in.ServerId)
		if err != nil {
			return out, err
		}
		out.RotateKey = !attached
	}
	return out, tx.Commit(ctx)
}

// RotateKey replaces a server's key. The caller proves it holds both the current key and its
// successor, through a live session.
func (s *Service) RotateKey(ctx context.Context, a Identity, in api.AgentKeyRotation, source string) error {
	if a.Kind != identityToken {
		return domain.Forbidden
	}
	newPublic, newDER, err := agentidentity.ParsePublicKey(in.PublicKey)
	if err != nil {
		return domain.Invalid("publicKey")
	}
	n, err := s.parseNonce(ctx, a.ID, in.Nonce)
	if err != nil {
		return err
	}
	current, err := s.Store.Queries.AgentKey(ctx, db.AgentKeyParams{ServerID: a.ID, KeyHash: a.Credential})
	if err != nil {
		return keyNotCurrent
	}
	oldPublic, _, err := agentidentity.ParsePublicKey(base64.RawURLEncoding.EncodeToString(current.PublicKey))
	newHash := agentidentity.KeyHash(newDER)
	if err != nil || !agentidentity.Verify(oldPublic, agentidentity.KeysMessage(false, a.ID, in.Nonce, newHash), in.OldSignature) ||
		!agentidentity.Verify(newPublic, agentidentity.KeysMessage(true, a.ID, in.Nonce, newHash), in.NewSignature) {
		return keyNotCurrent
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	server, err := q.LockServerIdentity(ctx, a.ID)
	if err != nil {
		return err
	}
	if server.Revoked || server.AwaitingTransfer || !bytes.Equal(server.CredentialHash, a.Credential) || server.EnrollmentEpoch != current.Epoch || bytes.Equal(newHash, a.Credential) {
		return keyNotCurrent
	}
	if valid, err := q.LockAgentCredential(ctx, db.LockAgentCredentialParams{WorkspaceID: server.WorkspaceID, ServerID: a.ID, CredentialHash: a.Credential, Lineage: &a.Lineage}); err != nil || !valid {
		if err == nil {
			err = keyNotCurrent
		}
		return err
	}
	if err = redeem(ctx, q, n); err != nil {
		return err
	}
	if err = q.InsertAgentKey(ctx, db.InsertAgentKeyParams{WorkspaceID: server.WorkspaceID, ServerID: a.ID, KeyHash: newHash, PublicKey: newDER, Epoch: server.EnrollmentEpoch}); err != nil {
		return err
	}
	if err = q.SupersedeAgentKey(ctx, db.SupersedeAgentKeyParams{Seq: pgtype.Int8{Int64: n.seq, Valid: true}, ServerID: a.ID, KeyHash: a.Credential}); err != nil {
		return err
	}
	if err = q.SetServerKey(ctx, db.SetServerKeyParams{KeyHash: newHash, WorkspaceID: server.WorkspaceID, ServerID: a.ID}); err != nil {
		return err
	}
	if err = identityEvent(ctx, q, server.WorkspaceID, a.ID, server.EnrollmentEpoch, "rotated", "", newHash, source); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Bind registers a key for a server that has so far held a bearer credential. The current bearer
// itself authenticates it, so a bound server's retired bearer can never bind again.
func (s *Service) Bind(ctx context.Context, server uuid.UUID, bearer string, in api.AgentKeyBind, source string) error {
	if len(bearer) != 43 {
		return domain.Unauthenticated
	}
	public, der, err := agentidentity.ParsePublicKey(in.PublicKey)
	if err != nil {
		return domain.Invalid("publicKey")
	}
	keyHash := agentidentity.KeyHash(der)
	if !agentidentity.Verify(public, agentidentity.BindMessage(server, in.MachineId, keyHash), in.Proof) {
		return domain.Unauthenticated
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	row, err := q.LockServerIdentity(ctx, server)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Unauthenticated
	}
	if err != nil {
		return err
	}
	presented := hash(bearer)
	if row.Revoked || row.AwaitingTransfer || row.CredentialHash == nil || row.MachineID == nil || *row.MachineID != in.MachineId {
		return domain.Unauthenticated
	}
	if row.RetiredBearerHash != nil && bytes.Equal(presented, row.RetiredBearerHash) {
		if bytes.Equal(row.CredentialHash, keyHash) {
			// A retry of the bind that retired this bearer.
			return nil
		}
		// The honest agent's bind key is fixed before it sends: another key is another holder.
		if err = s.conflict(ctx, q, row.WorkspaceID, server, row.EnrollmentEpoch, signalBindKey, keyHash, source); err != nil {
			return err
		}
		return commitRefusal(ctx, tx, keyNotCurrent)
	}
	if !bytes.Equal(row.CredentialHash, presented) {
		return domain.Unauthenticated
	}
	if err = q.BindServerKey(ctx, db.BindServerKeyParams{KeyHash: keyHash, BearerHash: presented, WorkspaceID: row.WorkspaceID, ServerID: server}); err != nil {
		return err
	}
	epoch := row.EnrollmentEpoch + 1
	if err = q.InsertAgentKey(ctx, db.InsertAgentKeyParams{WorkspaceID: row.WorkspaceID, ServerID: server, KeyHash: keyHash, PublicKey: der, Epoch: epoch}); err != nil {
		return err
	}
	if err = identityEvent(ctx, q, row.WorkspaceID, server, epoch, "bound", "", keyHash, source); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RetiredBearer checks a refused bearer against the one its server's bind retired. Inside the
// window where only another holder can present it, that proves a second holder.
func (s *Service) RetiredBearer(ctx context.Context, server uuid.UUID, bearer, source string) error {
	if len(bearer) != 43 {
		return nil
	}
	row, err := s.Store.Queries.RetiredBearer(ctx, server)
	if err != nil || row.RetiredBearerHash == nil || !bytes.Equal(row.RetiredBearerHash, hash(bearer)) || row.CredentialHash == nil {
		return nil
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	locked, err := q.LockServerIdentity(ctx, server)
	if err != nil || locked.RetiredBearerHash == nil || !bytes.Equal(locked.RetiredBearerHash, hash(bearer)) || locked.CredentialHash == nil || locked.BoundAt == nil {
		return err
	}
	now := time.Now()
	inWindow := row.FirstUsedAt != nil && now.After(row.FirstUsedAt.Add(retiredBearerFrom)) && now.Before(locked.BoundAt.Add(retiredBearerUntil))
	if inWindow {
		err = s.conflict(ctx, q, locked.WorkspaceID, server, locked.EnrollmentEpoch, signalRetiredBearer, nil, source)
	} else {
		err = identityEvent(ctx, q, locked.WorkspaceID, server, locked.EnrollmentEpoch, "refused", signalRetiredBearer, nil, source)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveFromNetworks takes a server out of every private network, so its peers drop its key. It
// is the owner's response to a copied state directory, which holds the server's network key.
func (s *Service) RemoveFromNetworks(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: true}, "server.network_removed", id, func(tx pgx.Tx) error {
		if _, err := db.New(tx).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: id}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.NotFound
			}
			return err
		}
		return invalidateNetworkMember(ctx, tx, ws, id)
	})
}

// identityViews reads what the dashboard shows about each server's agent identity.
func identityViews(ctx context.Context, q *db.Queries, ws uuid.UUID) (map[uuid.UUID]*api.ServerIdentity, error) {
	rows, err := q.ServerIdentities(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*api.ServerIdentity, len(rows))
	for _, r := range rows {
		v := &api.ServerIdentity{KeyBound: r.KeyCreatedAt != nil, KeyCreatedAt: r.KeyCreatedAt, KeyRotatedAt: r.KeyRotatedAt, LineageStartedAt: r.LineageStartedAt}
		if r.IdentityConflictAt != nil && r.IdentityConflictEpoch.Valid && r.IdentityConflictEpoch.Int64 == r.EnrollmentEpoch {
			v.ConflictAt = r.IdentityConflictAt
			var detail struct {
				Signal string `json:"signal"`
			}
			if json.Unmarshal(r.IdentityConflict, &detail) == nil && detail.Signal != "" {
				v.ConflictSignal = &detail.Signal
			}
		}
		out[r.ID] = v
	}
	return out, nil
}
