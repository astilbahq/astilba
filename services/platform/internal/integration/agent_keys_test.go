package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
)

// keyAgent is a real agent client over a real state directory.
type keyAgent struct {
	dir  string
	j    *agent.Journal
	c    *agent.Client
	keys agent.KeyStore
	in   api.AgentEnrollment
	tls  *httptest.Server
}

func newKeyAgent(t *testing.T, tlsServer *httptest.Server, in api.AgentEnrollment, keys agent.KeyStore) *keyAgent {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := agent.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.SetConfig(agent.Config{Origin: tlsServer.URL, ServerID: in.ServerId, MachineID: in.MachineId}); err != nil {
		t.Fatal(err)
	}
	a := &keyAgent{dir: dir, j: j, keys: keys, in: in, tls: tlsServer}
	a.client()
	t.Cleanup(func() { a.j.Close() })
	return a
}

// client starts a new agent process on the same directory: no session yet.
func (a *keyAgent) client() {
	a.c = agent.NewClient(a.j, nil)
	a.c.HTTP = a.tls.Client()
	a.c.HTTP.Timeout = 35 * time.Second
	a.c.Keys = a.keys
	in := a.in
	a.c.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	}
}

// copyOf copies the state directory, as a backup, a snapshot or a thief would. keys says whether
// the copy can unseal: the same store models a whole-disk copy of a host without a TPM.
func (a *keyAgent) copyOf(t *testing.T, keys agent.KeyStore) *keyAgent {
	t.Helper()
	if err := a.j.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"journal.db", "keyring.json"} {
		src, err := os.Open(filepath.Join(a.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		src.Close()
		dst.Close()
	}
	var err error
	if a.j, err = agent.Open(a.dir); err != nil {
		t.Fatal(err)
	}
	a.client()
	j, err := agent.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	copied := &keyAgent{dir: dir, j: j, keys: keys, in: a.in, tls: a.tls}
	copied.client()
	t.Cleanup(func() { copied.j.Close() })
	return copied
}

func keyedServer(t *testing.T) (*fixture, *browser, uuid.UUID, api.Enrollment, *keyAgent) {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, _ := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	t.Cleanup(tlsServer.Close)
	a := newKeyAgent(t, tlsServer, in, agent.InsecureKeyStore())
	if err := a.c.Enroll(context.Background(), v.Token); err != nil {
		t.Fatal(err)
	}
	return f, b, ws, v, a
}

func identityConflict(t *testing.T, f *fixture, server uuid.UUID) (string, bool) {
	t.Helper()
	var signal *string
	var credential []byte
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT identity_conflict->>'signal',credential_hash FROM servers WHERE id=$1`, server).Scan(&signal, &credential); err != nil {
		t.Fatal(err)
	}
	if signal == nil {
		return "", credential == nil
	}
	return *signal, credential == nil
}

func TestAgentEnrollsWithAKeyAndHoldsNoBearer(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	cfg, err := a.j.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Key == nil || cfg.Credential != "" || !cfg.Enrolled {
		t.Fatal("the agent kept a bearer instead of a key", cfg.Key == nil, cfg.Credential != "")
	}
	if _, err = a.c.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var bound bool
	if err = f.st.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM servers s JOIN agent_keys k ON k.server_id=s.id AND k.key_hash=s.credential_hash WHERE s.id=$1)`, v.Server.Id).Scan(&bound); err != nil || !bound {
		t.Fatal("management doesn't hold the agent's key", err)
	}
}

func TestStateDirectoryCopyWithoutTheHostKeyCannotAuthenticate(t *testing.T) {
	_, _, _, _, a := keyedServer(t)
	copied := a.copyOf(t, agent.InsecureKeyStore())
	if _, err := copied.c.Heartbeat(context.Background()); err == nil {
		t.Fatal("a copy of the state directory authenticated without the host's key store")
	}
	if _, err := a.c.Heartbeat(context.Background()); err != nil {
		t.Fatal("the host lost its connection", err)
	}
}

func TestCloneRaisesAnIdentityConflict(t *testing.T) {
	for _, fencing := range []bool{false, true} {
		t.Run(map[bool]string{false: "alert", true: "fence"}[fencing], func(t *testing.T) {
			f, _, _, v, a := keyedServer(t)
			f.execution.ForkFencing = fencing
			ctx := context.Background()
			if _, err := a.c.Heartbeat(ctx); err != nil {
				t.Fatal(err)
			}
			// A whole-disk snapshot of a host without a TPM: the copy can unseal.
			copied := a.copyOf(t, a.keys)
			if _, err := copied.c.Heartbeat(ctx); err != nil {
				t.Fatal("the clone couldn't start a session", err)
			}
			_, _ = a.c.Heartbeat(ctx)
			signal, fenced := identityConflict(t, f, v.Server.Id)
			if signal != "start_counter" {
				t.Fatal("two holders of one key went unnoticed", signal)
			}
			if fenced != fencing {
				t.Fatal("fencing didn't follow the installation setting", fenced)
			}
		})
	}
}

func TestRestoredBackupRaisesAnIdentityConflict(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	ctx := context.Background()
	backup := a.copyOf(t, a.keys)
	for range 3 {
		a.client()
		if _, err := a.c.Heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = backup.c.Heartbeat(ctx)
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "start_counter" {
		t.Fatal("a restored backup went unnoticed", signal)
	}
}

func TestRotationRaceWonByACopyRaisesAConflict(t *testing.T) {
	f, b, ws, v, a := keyedServer(t)
	ctx := context.Background()
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	copied := a.copyOf(t, a.keys)
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+v.Server.Id.String()+"/rotate", nil, 204, nil)
	// The copy starts first, is asked to rotate, and uses its new key.
	if _, err := copied.c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := copied.j.Config(); cfg.PendingRotation != nil {
		t.Fatal("the copy didn't finish the rotation")
	}
	_, _ = a.c.Heartbeat(ctx)
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "superseded_key" && signal != "start_counter" {
		t.Fatal("a rotation won by a copy took the server silently", signal)
	}
}

func TestRoutineRotationKeepsTheAgentConnected(t *testing.T) {
	f, b, ws, v, a := keyedServer(t)
	ctx := context.Background()
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := a.j.Config()
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+v.Server.Id.String()+"/rotate", nil, 204, nil)
	a.client()
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := a.j.Config()
	if bytes.Equal(before.Key, after.Key) || after.PendingRotation != nil {
		t.Fatal("the agent didn't rotate its key")
	}
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("routine rotation raised a conflict", signal)
	}
	a.client()
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal("the rotated agent lost its connection", err)
	}
}

// coldStart makes the agent's token request by hand.
func coldStart(t *testing.T, tlsServer *httptest.Server, server uuid.UUID, key *ecdsa.PrivateKey, counter int64, status int) api.AgentToken {
	t.Helper()
	var challenge api.AgentChallenge
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/challenge", "", api.AgentChallengeRequest{ServerId: server}, 200, &challenge)
	public, _ := agentidentity.PublicKey(key)
	signature, _ := agentidentity.Sign(key, agentidentity.TokenMessage(server, challenge.Nonce, counter))
	var out api.AgentToken
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/token", "", api.AgentTokenRequest{ServerId: server, PublicKey: public, Nonce: challenge.Nonce, StartCounter: counter, Signature: signature}, status, &out)
	return out
}

func TestStrangerWithTheServerIDCanRecordNothing(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	stranger, _ := agentidentity.GenerateKey()
	for range 50 {
		coldStart(t, a.tls, v.Server.Id, stranger, 1, 401)
	}
	var events int
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_identity_events WHERE server_id=$1 AND kind<>'enrolled'`, v.Server.Id).Scan(&events); err != nil || events != 0 {
		t.Fatal("a stranger's keys were recorded", events, err)
	}
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("a stranger raised a conflict", signal)
	}
	if _, err := a.c.Heartbeat(context.Background()); err != nil {
		t.Fatal("a stranger blocked the server", err)
	}
}

func TestReenrollmentWithoutACodeClearsNothing(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, _ := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	key, _ := agentidentity.GenerateKey()
	public, _ := agentidentity.PublicKey(key)
	proof, _ := agentidentity.Sign(key, agentidentity.EnrollMessage(in.ServerId, v.Token, in.MachineId))
	in.Credential, in.PublicKey, in.Proof = nil, &public, &proof
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	var epoch int64
	_ = f.st.Pool.QueryRow(context.Background(), `SELECT enrollment_epoch FROM servers WHERE id=$1`, in.ServerId).Scan(&epoch)
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE servers SET identity_conflict_at=now(),identity_conflict_epoch=enrollment_epoch,identity_conflict='{"signal":"start_counter"}' WHERE id=$1`, in.ServerId); err != nil {
		t.Fatal(err)
	}
	// A retry through the key the code registered is answered and changes nothing.
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	var after int64
	_ = f.st.Pool.QueryRow(context.Background(), `SELECT enrollment_epoch FROM servers WHERE id=$1`, in.ServerId).Scan(&after)
	if after != epoch {
		t.Fatal("a retry without a fresh code started a new epoch")
	}
	if signal, _ := identityConflict(t, f, in.ServerId); signal != "start_counter" {
		t.Fatal("a retry without a fresh code cleared the conflict")
	}
	// A valid proof for the consumed code, with another key, is a second holder of the code.
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE servers SET identity_conflict_at=NULL,identity_conflict_epoch=NULL,identity_conflict=NULL WHERE id=$1`, in.ServerId); err != nil {
		t.Fatal(err)
	}
	other, _ := agentidentity.GenerateKey()
	otherPublic, _ := agentidentity.PublicKey(other)
	otherProof, _ := agentidentity.Sign(other, agentidentity.EnrollMessage(in.ServerId, v.Token, in.MachineId))
	in.PublicKey, in.Proof = &otherPublic, &otherProof
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/enroll", "", in, 401, nil)
	if signal, _ := identityConflict(t, f, in.ServerId); signal != "code_other_key" {
		t.Fatal("a second key for a consumed code went unnoticed", signal)
	}
}

func TestLegacyAgentBindsAKey(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	a := newKeyAgent(t, tlsServer, in, agent.InsecureKeyStore())
	if err := a.j.SetConfig(agent.Config{Origin: tlsServer.URL, ServerID: in.ServerId, MachineID: in.MachineId, Credential: *in.Credential, Enrolled: true, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	// The first request learns that management takes keys; the next one binds.
	for range 2 {
		if _, err := a.c.Heartbeat(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := a.j.Config()
	if cfg.Key == nil || cfg.Credential != "" || cfg.BindKey != nil {
		t.Fatal("the agent didn't replace its bearer with a key")
	}
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 401, nil)
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("the agent's own retired bearer raised a conflict right after binding", signal)
	}
}

func TestThiefWhoBindsFirstIsCaughtByTheRetiredBearer(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	thief, _ := agentidentity.GenerateKey()
	public, _ := agentidentity.PublicKey(thief)
	_, der, _ := agentidentity.ParsePublicKey(public)
	proof, _ := agentidentity.Sign(thief, agentidentity.BindMessage(in.ServerId, in.MachineId, agentidentity.KeyHash(der)))
	bind := api.AgentKeyBind{PublicKey: public, MachineId: in.MachineId, Proof: proof}
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/bind", token, bind, 204, nil)
	// A lost answer is retried with the same key.
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/bind", token, bind, 204, nil)
	coldStart(t, tlsServer, in.ServerId, thief, 1, 200)
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE agent_keys SET first_used_at=now()-interval '3 minutes' WHERE server_id=$1`, in.ServerId); err != nil {
		t.Fatal(err)
	}
	// The real agent, still the earlier release, keeps presenting its bearer.
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 401, nil)
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "retired_bearer" {
		t.Fatal("a thief who bound first held the server silently", signal)
	}
}

func TestAccessTokenCannotUseTheLegacyRotation(t *testing.T) {
	_, _, _, v, a := keyedServer(t)
	cfg, _ := a.j.Config()
	sealed, _ := a.keys.Unseal(context.Background(), agent.KeyCredentialName, cfg.Key)
	key, _ := agentidentity.ParsePrivate(sealed)
	out := coldStart(t, a.tls, v.Server.Id, key, 100, 200)
	candidate, _ := randtoken.New()
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/rotate", v.Server.Id.String()+"."+out.Token, api.AgentRotation{Credential: candidate, MachineId: a.in.MachineId}, 403, nil)
}

func TestSupersededSessionIsRefusedAtOnce(t *testing.T) {
	_, _, _, v, a := keyedServer(t)
	cfg, _ := a.j.Config()
	sealed, _ := a.keys.Unseal(context.Background(), agent.KeyCredentialName, cfg.Key)
	key, _ := agentidentity.ParsePrivate(sealed)
	first := coldStart(t, a.tls, v.Server.Id, key, 100, 200)
	coldStart(t, a.tls, v.Server.Id, key, 101, 200)
	heartbeat := api.AgentHeartbeat{Capabilities: a.in.Capabilities, Preflight: a.in.Preflight}
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/heartbeat", v.Server.Id.String()+"."+first.Token, heartbeat, 401, nil)
}
