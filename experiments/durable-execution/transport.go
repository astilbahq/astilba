// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pollDuration = 25 * time.Second
const heartbeatInterval = 15 * time.Second
const offlineAfter = 45 * time.Second
const rotationOverlap = 2 * time.Minute
const transportBodyLimit = 16 << 10

// These tables and their trusted setup helpers belong only to the experiment.
// The clock offset and response faults let tests reach exchange boundaries
// without accepting test controls from an agent request.
const transportSchema = `
CREATE TABLE transport_clock (shift interval NOT NULL);
INSERT INTO transport_clock VALUES ('0 seconds');
CREATE TABLE transport_servers (
 id text PRIMARY KEY, role text NOT NULL, current_hash bytea,
 previous_hash bytea, previous_until timestamptz,
 revoked boolean NOT NULL DEFAULT false, last_seen timestamptz
);
CREATE TABLE transport_enrollments (
 digest bytea PRIMARY KEY, server_id text NOT NULL REFERENCES transport_servers,
 expires timestamptz NOT NULL, used boolean NOT NULL DEFAULT false
);
CREATE TABLE transport_commands (
 id text PRIMARY KEY, server_id text NOT NULL REFERENCES transport_servers,
 payload jsonb NOT NULL, result jsonb, observations integer NOT NULL DEFAULT 0
);
CREATE TABLE transport_events (
 server_id text NOT NULL, kind text NOT NULL, at timestamptz NOT NULL
);
CREATE TABLE transport_faults (
 server_id text NOT NULL, phase text NOT NULL, hit boolean NOT NULL DEFAULT false,
 PRIMARY KEY (server_id, phase)
);`

type envelope struct {
	Version  int     `json:"version"`
	ServerID string  `json:"server_id"`
	Kind     string  `json:"kind"`
	Command  command `json:"command"`
}

func (e envelope) validate(id, role string) error {
	if e.Version != 1 || e.ServerID != id || e.Kind != "fixture.create" || role != "deployment" ||
		e.Command.ID == "" || e.Command.Run == "" || e.Command.Environment == "" ||
		e.Command.Generation == 0 || e.Command.Fence == 0 || e.Command.PlanHash == "" {
		return errors.New("unsupported or misassigned command")
	}
	return nil
}

type agentIdentity struct {
	ServerID string `json:"server_id"`
	Role     string `json:"role"`
	Current  string `json:"credential"`
	Pending  string `json:"pending,omitempty"`
}

func newCredential() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func credentialHash(s string) []byte {
	digest := sha256.Sum256([]byte(s))
	return digest[:]
}

func decodeRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, transportBodyLimit))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid or oversized JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

type transportController struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	polling map[string]bool
}

func controllerNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()+shift FROM transport_clock`).Scan(&now)
	return now, err
}

// Authentication and mutation share the server row lock. A revocation that
// commits first prevents the following mutation or command selection.
func (c *transportController) authenticate(ctx context.Context, tx pgx.Tx, id, credential string, touch bool) (string, bool, error) {
	var role string
	var current, previous []byte
	var until *time.Time
	var revoked bool
	err := tx.QueryRow(ctx, `SELECT role,current_hash,previous_hash,previous_until,revoked
 FROM transport_servers WHERE id=$1 FOR UPDATE`, id).Scan(&role, &current, &previous, &until, &revoked)
	if err != nil {
		return "", false, err
	}
	now, err := controllerNow(ctx, tx)
	if err != nil {
		return "", false, err
	}
	hash := credentialHash(credential)
	isCurrent := subtle.ConstantTimeCompare(hash, current) == 1
	if credential == "" || revoked || (!isCurrent && !(subtle.ConstantTimeCompare(hash, previous) == 1 && until != nil && now.Before(*until))) {
		return "", false, errors.New("unauthorized")
	}
	if touch {
		_, err = tx.Exec(ctx, `UPDATE transport_servers SET last_seen=$2 WHERE id=$1`, id, now)
	}
	return role, isCurrent, err
}

func (c *transportController) event(ctx context.Context, tx pgx.Tx, id, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO transport_events SELECT $1,$2,clock_timestamp()+shift FROM transport_clock`, id, kind)
	return err
}

func (c *transportController) dropResponse(ctx context.Context, id, phase string) bool {
	row, err := c.pool.Exec(ctx, `UPDATE transport_faults SET hit=true WHERE server_id=$1 AND phase=$2 AND NOT hit`, id, phase)
	return err == nil && row.RowsAffected() == 1
}

func (c *transportController) enroll(w http.ResponseWriter, r *http.Request) {
	var body struct{ ServerID, Token string }
	if !decodeRequest(w, r, &body) {
		return
	}
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	defer tx.Rollback(context.Background())
	// Lock the identity before consuming its token, also serializing reissue.
	var role string
	err = tx.QueryRow(r.Context(), `SELECT role FROM transport_servers WHERE id=$1 FOR UPDATE`, body.ServerID).Scan(&role)
	if err != nil {
		http.Error(w, "invalid enrollment", 401)
		return
	}
	changed, err := tx.Exec(r.Context(), `UPDATE transport_enrollments SET used=true WHERE digest=$1 AND server_id=$2
 AND NOT used AND expires>(SELECT clock_timestamp()+shift FROM transport_clock)`, credentialHash(body.Token), body.ServerID)
	if err != nil || changed.RowsAffected() != 1 {
		http.Error(w, "invalid enrollment", 401)
		return
	}
	identity := agentIdentity{ServerID: body.ServerID, Role: role, Current: newCredential()}
	_, err = tx.Exec(r.Context(), `UPDATE transport_servers SET current_hash=$2,previous_hash=NULL,previous_until=NULL,revoked=false WHERE id=$1`, body.ServerID, credentialHash(identity.Current))
	if err != nil || tx.Commit(r.Context()) != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	if c.dropResponse(r.Context(), body.ServerID, "enroll") {
		panic(http.ErrAbortHandler)
	}
	writeJSON(w, identity)
}

func (c *transportController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	if r.URL.Path == "/v1/enroll" {
		c.enroll(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "servers" {
		http.NotFound(w, r)
		return
	}
	id, action := parts[2], parts[3]
	credential, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		http.Error(w, "unauthorized", 401)
		return
	}
	var raw json.RawMessage
	if !decodeRequest(w, r, &raw) {
		return
	}
	if action == "poll" {
		c.poll(w, r, id, credential)
		return
	}
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	defer tx.Rollback(context.Background())
	role, current, err := c.authenticate(r.Context(), tx, id, credential, true)
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	switch action {
	case "heartbeat":
		err = c.event(r.Context(), tx, id, "heartbeat")
	case "rotate":
		var body struct{ Candidate string }
		if json.Unmarshal(raw, &body) != nil {
			http.Error(w, "invalid rotation", 400)
			return
		}
		candidate, decodeErr := hex.DecodeString(body.Candidate)
		if decodeErr != nil || len(candidate) != 32 {
			http.Error(w, "invalid rotation", 400)
			return
		}
		var hash []byte
		if err = tx.QueryRow(r.Context(), `SELECT current_hash FROM transport_servers WHERE id=$1`, id).Scan(&hash); err != nil {
			break
		}
		if !bytes.Equal(hash, credentialHash(body.Candidate)) {
			if !current {
				http.Error(w, "previous credential cannot start another rotation", 403)
				return
			}
			_, err = tx.Exec(r.Context(), `UPDATE transport_servers SET previous_hash=current_hash,
 previous_until=(SELECT clock_timestamp()+shift FROM transport_clock)+$3::interval,current_hash=$2 WHERE id=$1`, id, credentialHash(body.Candidate), rotationOverlap.String())
		}
	case "report":
		var reported result
		if json.Unmarshal(raw, &reported) != nil {
			http.Error(w, "invalid report", 400)
			return
		}
		var payload, stored []byte
		err = tx.QueryRow(r.Context(), `SELECT payload,result FROM transport_commands WHERE id=$1 AND server_id=$2 FOR UPDATE`, reported.Command.ID, id).Scan(&payload, &stored)
		var assigned envelope
		if err != nil || json.Unmarshal(payload, &assigned) != nil || assigned.validate(id, role) != nil ||
			!assigned.Command.samePlan(reported.Command) || assigned.Command.Fence != reported.Command.Fence ||
			(reported.State != "succeeded" && reported.State != "cancelled" && reported.State != "needs_attention") {
			http.Error(w, "unassigned report", 403)
			return
		}
		if stored != nil {
			var previous result
			if json.Unmarshal(stored, &previous) != nil || !reflect.DeepEqual(previous, reported) {
				http.Error(w, "conflicting report", 409)
				return
			}
		} else {
			_, err = tx.Exec(r.Context(), `UPDATE transport_commands SET result=$2,observations=observations+1 WHERE id=$1`, reported.Command.ID, raw)
		}
		if err == nil {
			err = c.event(r.Context(), tx, id, "report")
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	if c.dropResponse(r.Context(), id, action) {
		panic(http.ErrAbortHandler)
	}
	w.WriteHeader(204)
}

func (c *transportController) poll(w http.ResponseWriter, r *http.Request, id, credential string) {
	c.mu.Lock()
	if c.polling[id] {
		c.mu.Unlock()
		http.Error(w, "poll already active", 409)
		return
	}
	c.polling[id] = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.polling, id); c.mu.Unlock() }()
	deadline := time.NewTimer(pollDuration)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	first := true
	for {
		tx, err := c.pool.Begin(r.Context())
		if err != nil {
			return
		}
		role, _, err := c.authenticate(r.Context(), tx, id, credential, first)
		if err != nil {
			tx.Rollback(context.Background())
			http.Error(w, "unauthorized", 401)
			return
		}
		if first {
			err = c.event(r.Context(), tx, id, "poll")
			first = false
		}
		var payload []byte
		if err == nil {
			err = tx.QueryRow(r.Context(), `SELECT payload FROM transport_commands WHERE server_id=$1 AND result IS NULL ORDER BY id LIMIT 1`, id).Scan(&payload)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			tx.Rollback(context.Background())
			http.Error(w, "unavailable", 503)
			return
		}
		if tx.Commit(r.Context()) != nil {
			return
		}
		if payload != nil {
			var e envelope
			if json.Unmarshal(payload, &e) != nil || e.validate(id, role) != nil {
				http.Error(w, "unsupported or misassigned command", 409)
				return
			}
			if c.dropResponse(r.Context(), id, "delivery") {
				panic(http.ErrAbortHandler)
			}
			// Selection never dequeues work: only an authenticated terminal
			// report removes it from subsequent polls.
			writeJSON(w, e)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			w.WriteHeader(204)
			return
		case <-tick.C:
		}
	}
}

func saveIdentity(path string, identity agentIdentity) error {
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type transportAgent struct {
	client   *http.Client
	baseURL  string
	identity agentIdentity
	path     string
	local    *agent
}

func (a *transportAgent) post(ctx context.Context, action, credential string, body, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	url := a.baseURL + "/v1/servers/" + a.identity.ServerID + "/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, errors.New("agent request rejected")
	}
	if out != nil && resp.StatusCode != 204 {
		err = json.NewDecoder(io.LimitReader(resp.Body, transportBodyLimit)).Decode(out)
	}
	return resp.StatusCode, err
}

func (a *transportAgent) rotate(ctx context.Context, point func(string)) error {
	if a.identity.Pending == "" {
		a.identity.Pending = newCredential()
		if err := saveIdentity(a.path, a.identity); err != nil {
			return err
		}
	}
	point("prepared")
	// The candidate may already be current after a lost response, including
	// after the old credential's overlap has expired.
	status, err := a.post(ctx, "rotate", a.identity.Pending, struct{ Candidate string }{a.identity.Pending}, nil)
	if status == http.StatusUnauthorized {
		_, err = a.post(ctx, "rotate", a.identity.Current, struct{ Candidate string }{a.identity.Pending}, nil)
	}
	if err != nil {
		return err
	}
	point("exchanged")
	a.identity.Current, a.identity.Pending = a.identity.Pending, ""
	if err := saveIdentity(a.path, a.identity); err != nil {
		return err
	}
	point("promoted")
	return nil
}

func contactOnline(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	var now time.Time
	var lastSeen *time.Time
	err := pool.QueryRow(ctx, `SELECT clock_timestamp()+shift,last_seen FROM transport_servers CROSS JOIN transport_clock WHERE id=$1`, id).Scan(&now, &lastSeen)
	return lastSeen != nil && now.Sub(*lastSeen) < offlineAfter, err
}

// Heartbeats have their own loop. Local execution uses an operation deadline,
// never the poll request or transport lifetime; report delivery can reconnect.
func (a *transportAgent) run(ctx context.Context) {
	ctx, stop := context.WithCancel(ctx)
	var heartbeat sync.WaitGroup
	heartbeat.Add(1)
	go func() {
		defer heartbeat.Done()
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.post(ctx, "heartbeat", a.identity.Current, struct{}{}, nil)
			}
		}
	}()
	defer func() { stop(); heartbeat.Wait() }()
	for ctx.Err() == nil {
		var e envelope
		status, err := a.post(ctx, "poll", a.identity.Current, struct{}{}, &e)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
				continue
			}
		}
		if status == 204 {
			continue
		}
		if e.validate(a.identity.ServerID, a.identity.Role) != nil {
			return
		}
		operation, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		r, err := a.local.execute(operation, e.Command)
		cancel()
		if err != nil {
			continue
		}
		for ctx.Err() == nil {
			if _, err := a.post(ctx, "report", a.identity.Current, r, nil); err == nil {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}
