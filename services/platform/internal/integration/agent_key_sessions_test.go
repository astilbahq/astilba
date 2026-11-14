package integration

import (
	"context"
	"crypto/ecdsa"
	"net/http/httptest"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// agentKey unseals the key the agent holds now.
func agentKey(t *testing.T, a *keyAgent) *ecdsa.PrivateKey {
	t.Helper()
	cfg, err := a.j.Config()
	if err != nil {
		t.Fatal(err)
	}
	der, err := a.keys.Unseal(context.Background(), agent.KeyCredentialName, cfg.Key)
	if err != nil {
		t.Fatal(err)
	}
	key, err := agentidentity.ParsePrivate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func refresh(t *testing.T, tlsServer *httptest.Server, server uuid.UUID, key *ecdsa.PrivateKey, secret string, status int) api.AgentToken {
	t.Helper()
	signature, _ := agentidentity.Sign(key, agentidentity.RefreshMessage(server, secret))
	var out api.AgentToken
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/token/refresh", "", api.AgentRefreshRequest{ServerId: server, Refresh: secret, Signature: signature}, status, &out)
	return out
}

func identityEvents(t *testing.T, f *fixture, server uuid.UUID, kind, signal string) int {
	t.Helper()
	var n int
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_identity_events WHERE server_id=$1 AND kind=$2 AND signal=$3`, server, kind, signal).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefreshRenewsTheSessionOncePerSecret(t *testing.T) {
	_, _, _, v, a := keyedServer(t)
	key := agentKey(t, a)
	first := coldStart(t, a.tls, v.Server.Id, key, 100, 200)
	if first.ExpiresIn < 60 {
		t.Fatal("the token's lifetime is too short to use", first.ExpiresIn)
	}
	renewed := refresh(t, a.tls, v.Server.Id, key, first.Refresh, 200)
	heartbeat := api.AgentHeartbeat{Capabilities: a.in.Capabilities, Preflight: a.in.Preflight}
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/heartbeat", v.Server.Id.String()+"."+renewed.Token, heartbeat, 200, nil)
	// Requests in flight across a refresh still succeed with the token it replaced.
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/heartbeat", v.Server.Id.String()+"."+first.Token, heartbeat, 200, nil)
	// A secret renews the session once. The agent starts a new session after any failure, so it
	// never presents a secret again.
	refresh(t, a.tls, v.Server.Id, key, first.Refresh, 401)
	again := refresh(t, a.tls, v.Server.Id, key, renewed.Refresh, 200)
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/heartbeat", v.Server.Id.String()+"."+again.Token, heartbeat, 200, nil)
}

func TestDelayedSessionStartIsStaleNotAConflict(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	key := agentKey(t, a)
	public, _ := agentidentity.PublicKey(key)
	request := func(nonce string, counter int64, status int) {
		signature, _ := agentidentity.Sign(key, agentidentity.TokenMessage(v.Server.Id, nonce, counter))
		agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/token", "", api.AgentTokenRequest{ServerId: v.Server.Id, PublicKey: public, Nonce: nonce, StartCounter: counter, Signature: signature}, status, nil)
	}
	var earlier, later api.AgentChallenge
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/challenge", "", api.AgentChallengeRequest{ServerId: v.Server.Id}, 200, &earlier)
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/challenge", "", api.AgentChallengeRequest{ServerId: v.Server.Id}, 200, &later)
	// One holder's attempts, the later arriving first.
	request(later.Nonce, 101, 200)
	request(earlier.Nonce, 100, 401)
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("one holder's delayed request raised a conflict", signal)
	}
	// A higher counter with an older nonce can't come from the holder that used the newer one.
	var older, newer api.AgentChallenge
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/challenge", "", api.AgentChallengeRequest{ServerId: v.Server.Id}, 200, &older)
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/challenge", "", api.AgentChallengeRequest{ServerId: v.Server.Id}, 200, &newer)
	request(newer.Nonce, 200, 200)
	request(older.Nonce, 201, 200)
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "start_counter" {
		t.Fatal("counters that fell as nonces rose went unnoticed", signal)
	}
}

func TestKeyReplacedByANewCodeIsRecordedOnceWithoutAnAlert(t *testing.T) {
	f, b, ws, v, a := keyedServer(t)
	ctx := context.Background()
	old := agentKey(t, a)
	var next api.Enrollment
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+v.Server.Id.String()+"/enrollment", nil, 201, &next)
	if err := a.c.Enroll(ctx, next.Token); err != nil {
		t.Fatal(err)
	}
	for counter := range int64(3) {
		coldStart(t, a.tls, v.Server.Id, old, 1000+counter, 401)
	}
	if n := identityEvents(t, f, v.Server.Id, "refused", "earlier_epoch_key"); n != 1 {
		t.Fatal("a replaced key's use wasn't recorded exactly once", n)
	}
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("a key the owner replaced raised a conflict", signal)
	}
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	// Retention keeps a replaced key for 30 days from its replacement, however old the key is,
	// then drops it and its epoch's events. It keeps the current key, and drops expired nonces.
	count := func(sql string) int {
		var n int
		if err := f.st.Pool.QueryRow(ctx, sql, v.Server.Id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE agent_keys SET created_at=now()-interval '90 days' WHERE server_id=$1`, v.Server.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE agent_nonces_consumed SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.PruneHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if keys := count(`SELECT count(*) FROM agent_keys WHERE server_id=$1`); keys != 2 {
		t.Fatal("retention dropped a key replaced less than 30 days ago", keys)
	}
	if count(`SELECT count(*) FROM agent_nonces_consumed WHERE $1::uuid IS NOT NULL`) != 0 {
		t.Fatal("retention kept expired nonces")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE servers SET identity_history_until=now()-interval '1 second' WHERE id=$1`, v.Server.Id); err != nil {
		t.Fatal(err)
	}
	if err := f.st.PruneHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if keys := count(`SELECT count(*) FROM agent_keys WHERE server_id=$1`); keys != 1 {
		t.Fatal("retention kept a replaced key past its 30 days, or dropped the current one", keys)
	}
	if count(`SELECT count(*) FROM agent_identity_events e JOIN servers s ON s.id=e.server_id WHERE e.server_id=$1 AND e.epoch<s.enrollment_epoch`) != 0 {
		t.Fatal("retention kept an earlier epoch's events past its 30 days")
	}
	a.client()
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal("retention dropped the current key", err)
	}
}

func TestAgentNeverResendsTheCodeThatEnrolledIt(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	before, _ := a.j.Config()
	// Rerunning the install command with the same code would send it with a new key.
	if err := a.c.Enroll(context.Background(), v.Token); err == nil {
		t.Fatal("the agent sent a code it had already used")
	}
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("the agent's own retry looked like a second holder", signal)
	}
	after, _ := a.j.Config()
	if string(before.Key) != string(after.Key) {
		t.Fatal("the agent replaced its key")
	}
}

func TestConflictSurvivesABind(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, _ := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	agentRequest(t, tlsServer.Client(), tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE servers SET identity_conflict_at=now(),identity_conflict_epoch=enrollment_epoch,identity_conflict='{"signal":"retired_bearer"}' WHERE id=$1`, in.ServerId); err != nil {
		t.Fatal(err)
	}
	a := newKeyAgent(t, tlsServer, in, agent.InsecureKeyStore())
	if err := a.j.SetConfig(agent.Config{Origin: tlsServer.URL, ServerID: in.ServerId, MachineID: in.MachineId, Credential: *in.Credential, Enrolled: true, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := a.c.Heartbeat(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if cfg, _ := a.j.Config(); cfg.Key == nil {
		t.Fatal("the agent didn't bind")
	}
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "retired_bearer" {
		t.Fatal("binding a key cleared a conflict only a code may clear", signal)
	}
	var servers []api.Server
	b.call("GET", "/api/v1/workspaces/"+ws.String()+"/servers", nil, 200, &servers)
	for _, s := range servers {
		if s.Id == v.Server.Id && (s.Identity == nil || s.Identity.ConflictAt == nil) {
			t.Fatal("the dashboard stopped showing the conflict after the bind")
		}
	}
}

func TestStrangerCannotBindOrEnrollAKey(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	stranger, _ := agentidentity.GenerateKey()
	public, _ := agentidentity.PublicKey(stranger)
	_, der, _ := agentidentity.ParsePublicKey(public)
	guess := v.Server.Id.String() + "." + "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
	proof, _ := agentidentity.Sign(stranger, agentidentity.BindMessage(v.Server.Id, a.in.MachineId, agentidentity.KeyHash(der)))
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/bind", guess, api.AgentKeyBind{PublicKey: public, MachineId: a.in.MachineId, Proof: proof}, 401, nil)
	code := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
	enrollProof, _ := agentidentity.Sign(stranger, agentidentity.EnrollMessage(v.Server.Id, code, a.in.MachineId))
	in := a.in
	in.Token, in.Credential, in.PublicKey, in.Proof = code, nil, &public, &enrollProof
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/enroll", "", in, 401, nil)
	var events int
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_identity_events WHERE server_id=$1 AND kind<>'enrolled'`, v.Server.Id).Scan(&events); err != nil || events != 0 {
		t.Fatal("a stranger's requests were recorded", events, err)
	}
	if signal, _ := identityConflict(t, f, v.Server.Id); signal != "" {
		t.Fatal("a stranger raised a conflict", signal)
	}
	if _, err := a.c.Heartbeat(context.Background()); err != nil {
		t.Fatal("a stranger blocked the server", err)
	}
}

func TestServersPageKeepsTheKeyThroughReKeyingAndRevocation(t *testing.T) {
	f, b, ws, v, a := keyedServer(t)
	ctx := context.Background()
	route := "/api/v1/workspaces/" + ws.String() + "/servers/" + v.Server.Id.String()
	view := func() *api.ServerIdentity {
		t.Helper()
		var servers []api.Server
		b.call("GET", "/api/v1/workspaces/"+ws.String()+"/servers", nil, 200, &servers)
		for _, s := range servers {
			if s.Id == v.Server.Id && s.Identity != nil {
				return s.Identity
			}
		}
		t.Fatal("the server has no identity view")
		return nil
	}
	if _, err := a.c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	b.call("POST", route+"/rotate", nil, 204, nil)
	if live := view(); live.RotateKeyRequested == nil || live.KeyCurrent == nil || live.LineageStartedAt == nil {
		t.Fatal("a live key, its session or a requested rotation isn't shown")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE servers SET identity_conflict_at=now(),identity_conflict_epoch=enrollment_epoch,identity_conflict='{"signal":"start_counter"}' WHERE id=$1`, v.Server.Id); err != nil {
		t.Fatal(err)
	}
	// A new code clears the credential, but the server still held a key: it isn't a bearer agent.
	b.call("POST", route+"/enrollment", nil, 201, nil)
	identity := view()
	if !identity.KeyBound || identity.ConflictAt == nil {
		t.Fatal("a new code made a key-bound server look like a bearer agent, or hid its conflict", identity.KeyBound)
	}
	// Nothing on the page says the cleared key, its session or the requested rotation is live.
	if identity.KeyCurrent != nil || identity.LineageStartedAt != nil || identity.RotateKeyRequested != nil {
		t.Fatal("the page still shows a cleared key as live")
	}
	b.call("POST", route+"/revoke", nil, 204, nil)
	if identity := view(); !identity.KeyBound || identity.ConflictAt != nil {
		t.Fatal("revocation hid the key, or kept a conflict whose alert it ended", identity.KeyBound)
	}
}

func TestRefreshSecretLeftUnusedStopsWorking(t *testing.T) {
	f, _, _, v, a := keyedServer(t)
	ctx := context.Background()
	key := agentKey(t, a)
	idle := func(minutes int) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `UPDATE agent_lineages SET token_expires_at=now()-make_interval(mins => $2) WHERE server_id=$1 AND superseded_at IS NULL`, v.Server.Id, minutes); err != nil {
			t.Fatal(err)
		}
	}
	session := coldStart(t, a.tls, v.Server.Id, key, 100, 200)
	// An agent keeps trying its secret through an outage, and an hour past its token's expiry
	// covers ordinary deploys.
	idle(50)
	renewed := refresh(t, a.tls, v.Server.Id, key, session.Refresh, 200)
	// Past that, a secret copied from the agent's memory is useless, and nothing is recorded.
	idle(61)
	refresh(t, a.tls, v.Server.Id, key, renewed.Refresh, 401)
	if n := identityEvents(t, f, v.Server.Id, "refused", "refresh"); n != 0 {
		t.Fatal("an expired refresh secret was recorded as a refusal", n)
	}
}
