// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

type transportRoundTripFunc func(*http.Request) (*http.Response, error)

func (f transportRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestAgentHTTPS(t *testing.T) {
	h := newTransportHarness(t)
	t.Run("tls", func(t *testing.T) {
		untrusted := &http.Client{Timeout: 3 * time.Second}
		if resp, err := untrusted.Get(h.url + "/health"); err == nil {
			resp.Body.Close()
			t.Fatal("untrusted certificate accepted")
		}
		resp, err := h.client.Get(h.url + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.TLS == nil || resp.StatusCode != 204 {
			t.Fatal("verified HTTPS did not reach controller")
		}
		t.Logf("Traefik v3.7.16, TLS=%x, HTTP=%s", resp.TLS.Version, resp.Proto)
	})

	t.Run("enrollment_binding_single_use_expiry", func(t *testing.T) {
		token := h.issue(t, "enroll-a", "deployment")
		h.issue(t, "enroll-b", "deployment")
		if h.request(t, "/v1/enroll", "", struct{ ServerID, Token string }{"enroll-b", token}, nil) != 401 {
			t.Fatal("token enrolled another server")
		}
		type outcome struct {
			status int
			err    error
		}
		statuses := make(chan outcome, 2)
		for range 2 {
			go func() {
				status, err := h.sendRequest("/v1/enroll", "", struct{ ServerID, Token string }{"enroll-a", token}, nil)
				statuses <- outcome{status, err}
			}()
		}
		a, b := <-statuses, <-statuses
		if a.err != nil || b.err != nil {
			t.Fatalf("enrollment requests failed: %v; %v", a.err, b.err)
		}
		if !((a.status == 200 && b.status == 401) || (a.status == 401 && b.status == 200)) {
			t.Fatalf("enrollment race: statuses %d, %d", a.status, b.status)
		}
		token = h.issue(t, "enroll-expired", "deployment")
		h.shift(t, "6 minutes")
		defer h.shift(t, "0 seconds")
		if h.request(t, "/v1/enroll", "", struct{ ServerID, Token string }{"enroll-expired", token}, nil) != 401 {
			t.Fatal("expired enrollment accepted")
		}
	})

	t.Run("lost_enrollment_response", func(t *testing.T) {
		token := h.issue(t, "enroll-lost", "deployment")
		h.fault(t, "enroll-lost", "enroll")
		if h.request(t, "/v1/enroll", "", struct{ ServerID, Token string }{"enroll-lost", token}, nil) != 502 {
			t.Fatal("enrollment response was not dropped")
		}
		if h.request(t, "/v1/enroll", "", struct{ ServerID, Token string }{"enroll-lost", token}, nil) != 401 {
			t.Fatal("used enrollment token replayed after lost response")
		}
		a := h.enroll(t, "enroll-lost", "deployment")
		mustPost(t, a, "heartbeat", struct{}{}, nil, 204)
	})

	for _, boundary := range []string{"prepared", "exchanged", "promoted"} {
		t.Run("rotation_interrupt_"+boundary, func(t *testing.T) {
			a := h.enroll(t, "rotation-"+boundary, "deployment")
			old := a.identity.Current
			interrupted := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						if r != boundary {
							panic(r)
						}
						interrupted = true
					}
				}()
				if err := a.rotate(context.Background(), func(p string) {
					if p == boundary {
						panic(boundary)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}()
			if !interrupted {
				t.Fatal("rotation boundary not reached")
			}
			a = reloadIdentity(t, a)
			if a.identity.Pending != "" {
				if err := a.rotate(context.Background(), noPoint); err != nil {
					t.Fatal(err)
				}
			}
			mustPost(t, a, "heartbeat", struct{}{}, nil, 204)
			assertIdentityPrivate(t, a)
			h.shift(t, "121 seconds")
			defer h.shift(t, "0 seconds")
			status, _ := a.post(context.Background(), "heartbeat", old, struct{}{}, nil)
			if status != 401 {
				t.Fatal("old credential survived overlap")
			}
			mustPost(t, a, "heartbeat", struct{}{}, nil, 204)
		})
	}

	t.Run("rotation_lost_response_controller_restart", func(t *testing.T) {
		a := h.enroll(t, "rotation-lost", "deployment")
		old := a.identity.Current
		h.fault(t, a.identity.ServerID, "rotate")
		if err := a.rotate(context.Background(), noPoint); err == nil {
			t.Fatal("rotation response was not dropped")
		}
		a = reloadIdentity(t, a)
		candidate := a.identity.Pending
		if candidate == "" {
			t.Fatal("candidate was not persisted before exchange")
		}
		zero := 0
		if _, err := h.h.docker.ContainerRestart(context.Background(), h.controller, client.ContainerRestartOptions{Timeout: &zero}); err != nil {
			t.Fatal(err)
		}
		wait(t, 10*time.Second, func() bool {
			r, err := h.client.Get(h.url + "/health")
			if err != nil {
				return false
			}
			r.Body.Close()
			return r.StatusCode == 204
		})
		if status, _ := a.post(context.Background(), "rotate", old, struct{ Candidate string }{newCredential()}, nil); status != 403 {
			t.Fatal("previous credential started another rotation")
		}
		h.shift(t, "60 seconds")
		if err := a.rotate(context.Background(), noPoint); err != nil {
			t.Fatal(err)
		}
		if a.identity.Current != candidate {
			t.Fatal("retry replaced the persisted candidate")
		}
		h.shift(t, "121 seconds")
		defer h.shift(t, "0 seconds")
		if status, _ := a.post(context.Background(), "heartbeat", old, struct{}{}, nil); status != 401 {
			t.Fatal("rotation retry extended the old credential deadline")
		}
		mustPost(t, a, "heartbeat", struct{}{}, nil, 204)
	})

	t.Run("rotation_expired_recovery_and_revocation", func(t *testing.T) {
		a := h.enroll(t, "rotation-expired", "deployment")
		old := a.identity.Current
		h.fault(t, a.identity.ServerID, "rotate")
		if err := a.rotate(context.Background(), noPoint); err == nil {
			t.Fatal("expected lost response")
		}
		a = reloadIdentity(t, a)
		h.shift(t, "121 seconds")
		defer h.shift(t, "0 seconds")
		candidate := a.identity.Pending
		if status, _ := a.post(context.Background(), "heartbeat", a.identity.Current, struct{}{}, nil); status != 401 {
			t.Fatal("expired old identity remained usable")
		}
		if err := a.rotate(context.Background(), noPoint); err != nil {
			t.Fatal("persisted promoted candidate could not recover")
		}
		h.shift(t, "0 seconds")
		if _, err := h.pool.Exec(context.Background(), `UPDATE transport_servers SET revoked=true WHERE id=$1`, a.identity.ServerID); err != nil {
			t.Fatal(err)
		}
		for _, credential := range []string{old, candidate} {
			for _, action := range []string{"heartbeat", "poll", "report", "rotate"} {
				if status, _ := a.post(context.Background(), action, credential, struct{}{}, nil); status != 401 {
					t.Fatalf("revoked identity accepted by %s", action)
				}
			}
		}
	})

	t.Run("server_role_version_and_report_isolation", func(t *testing.T) {
		a := h.enroll(t, "isolation-a", "deployment")
		b := h.enroll(t, "isolation-b", "deployment")
		build := h.enroll(t, "build-only", "build")
		assigned := h.command(t, b.identity.ServerID)
		if h.request(t, "/v1/servers/"+b.identity.ServerID+"/poll", a.identity.Current, struct{}{}, nil) != 401 {
			t.Fatal("another server polled commands")
		}
		mustPost(t, a, "report", result{Command: assigned.Command, State: "succeeded"}, nil, 403)
		mustPost(t, a, "../admin", struct{}{}, nil, 404)
		h.command(t, build.identity.ServerID)
		mustPost(t, build, "poll", struct{}{}, nil, 409)
		for _, invalid := range []string{"server", "version", "kind"} {
			i := h.enroll(t, "invalid-"+invalid, "deployment")
			e := envelope{Version: 1, ServerID: i.identity.ServerID, Kind: "fixture.create", Command: h.c}
			e.Command.ID = token()
			switch invalid {
			case "server":
				e.ServerID = b.identity.ServerID
			case "version":
				e.Version = 2
			case "kind":
				e.Kind = "shell"
			}
			data, _ := json.Marshal(e)
			if _, err := h.pool.Exec(context.Background(), `INSERT INTO transport_commands(id,server_id,payload) VALUES($1,$2,$3)`, e.Command.ID, i.identity.ServerID, data); err != nil {
				t.Fatal(err)
			}
			mustPost(t, i, "poll", struct{}{}, nil, 409)
			if e.validate(i.identity.ServerID, i.identity.Role) == nil {
				t.Fatal("agent would accept invalid envelope")
			}
		}
	})

	t.Run("poll_reconnect_limits_and_revocation", func(t *testing.T) {
		a := h.enroll(t, "poll-controls", "deployment")
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan int, 1)
		go func() { status, _ := a.post(ctx, "poll", a.identity.Current, struct{}{}, nil); finished <- status }()
		wait(t, 3*time.Second, func() bool { return h.eventCount(t, a.identity.ServerID, "poll") == 1 })
		mustPost(t, a, "poll", struct{}{}, nil, 409)
		cancel()
		<-finished
		// A canceled request relinquishes the poll slot without consuming work.

		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		go func() {
			for {
				status, _ := a.post(ctx, "poll", a.identity.Current, struct{}{}, nil)
				if status != 409 || ctx.Err() != nil {
					finished <- status
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		wait(t, 3*time.Second, func() bool { return h.eventCount(t, a.identity.ServerID, "poll") == 2 })
		if _, err := h.pool.Exec(context.Background(), `UPDATE transport_servers SET revoked=true WHERE id=$1`, a.identity.ServerID); err != nil {
			t.Fatal(err)
		}
		select {
		case status := <-finished:
			if status != 401 {
				t.Fatalf("revoked open poll returned %d", status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("revocation did not end outstanding poll")
		}
		a = h.enroll(t, "body-limit", "deployment")
		mustPost(t, a, "heartbeat", map[string]string{"payload": string(make([]byte, transportBodyLimit+1))}, nil, 400)
	})

	t.Run("reconnect_effect_and_live_timing", func(t *testing.T) {
		a := h.enroll(t, "live-agent", "deployment")
		effect := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		local, err := openAgent(filepath.Join(t.TempDir(), "journal.db"), h.h.host, effectImage, func(p string) {
			if p == "effect" {
				close(effect)
				<-release
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(local.close)
		a.local = local
		type pollCompletion struct {
			status        int
			serverElapsed string
			err           error
		}
		firstPoll := make(chan pollCompletion, 1)
		var firstPollOnce sync.Once
		observedClient := *a.client
		transport := observedClient.Transport
		pollPath := "/v1/servers/" + a.identity.ServerID + "/poll"
		observedClient.Transport = transportRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp, err := transport.RoundTrip(r)
			if r.URL.Path == pollPath {
				firstPollOnce.Do(func() {
					completion := pollCompletion{err: err}
					if resp != nil {
						completion.status = resp.StatusCode
						completion.serverElapsed = resp.Header.Get("X-Astilba-Fixture-Poll-Elapsed")
					}
					select {
					case firstPoll <- completion:
					default:
					}
				})
			}
			return resp, err
		})
		a.client = &observedClient
		// The controller and client can run in different clock domains. Check
		// the timer minimum there and the reconnect deadline on the client.
		pollStarted := time.Now()
		pollDeadline := pollStarted.Add(pollDuration + 4*time.Second)
		stop, stopped := startTransportAgent(t, a)
		var completion pollCompletion
		select {
		case completion = <-firstPoll:
		case <-time.After(time.Until(pollDeadline)):
			t.Fatal("empty poll did not complete before the reconnect deadline")
		}
		serverElapsed, parseErr := time.ParseDuration(completion.serverElapsed)
		if completion.err != nil || completion.status != http.StatusNoContent || parseErr != nil || serverElapsed < pollDuration {
			t.Fatalf("empty poll failed: status=%d controller elapsed=%q error=%v parse error=%v", completion.status, completion.serverElapsed, completion.err, parseErr)
		}
		wait(t, time.Until(pollDeadline), func() bool { return h.eventCount(t, a.identity.ServerID, "poll") >= 2 })
		pollElapsed := time.Since(pollStarted)
		if pollElapsed > pollDuration+4*time.Second || h.eventCount(t, a.identity.ServerID, "heartbeat") == 0 {
			t.Fatalf("poll/heartbeat overlap failed, poll and reconnect took %s", pollElapsed)
		}
		h.fault(t, a.identity.ServerID, "delivery")
		e := h.command(t, a.identity.ServerID)
		select {
		case <-effect:
		case <-time.After(10 * time.Second):
			t.Fatal("lost delivery did not recover")
		}
		pausedAt := time.Now()
		wait(t, 50*time.Second, func() bool {
			var count int
			err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM transport_events WHERE server_id=$1 AND kind='heartbeat' AND at>$2`, a.identity.ServerID, pausedAt).Scan(&count)
			return err == nil && count >= 3
		})
		var minGap, maxGap float64
		if err := h.pool.QueryRow(context.Background(), `SELECT min(gap),max(gap) FROM (
 SELECT extract(epoch FROM at-lag(at) OVER (ORDER BY at)) gap FROM transport_events WHERE server_id=$1 AND kind='heartbeat') s`, a.identity.ServerID).Scan(&minGap, &maxGap); err != nil {
			t.Fatal(err)
		}
		if minGap < 14 || maxGap > 20 {
			t.Fatalf("heartbeat interval outside tolerance: %.3f..%.3fs", minGap, maxGap)
		}
		if online, err := contactOnline(context.Background(), h.pool, a.identity.ServerID); err != nil || !online {
			t.Fatal("active agent became offline during execution")
		}
		stop()
		releaseOnce.Do(func() { close(release) })
		<-stopped
		wait(t, 5*time.Second, func() bool {
			var terminal bool
			local.db.View(func(tx *bolt.Tx) error {
				var r result
				json.Unmarshal(tx.Bucket([]byte("operations")).Get([]byte(e.Command.ID)), &r)
				terminal = r.State == "succeeded"
				return nil
			})
			return terminal
		})
		var stored bool
		if err := h.pool.QueryRow(context.Background(), `SELECT result IS NOT NULL FROM transport_commands WHERE id=$1`, e.Command.ID).Scan(&stored); err != nil || stored {
			t.Fatal("disconnected transport reported unexpectedly")
		}
		h.fault(t, a.identity.ServerID, "report")
		stop, stopped = startTransportAgent(t, reloadIdentity(t, a))
		wait(t, 5*time.Second, func() bool {
			return h.eventCount(t, a.identity.ServerID, "report") >= 2
		})
		stop()
		<-stopped
		var data []byte
		if err := h.pool.QueryRow(context.Background(), `SELECT result FROM transport_commands WHERE id=$1`, e.Command.ID).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var recorded result
		if err := json.Unmarshal(data, &recorded); err != nil {
			t.Fatal(err)
		}
		f := *h.fixture
		f.c = e.Command
		if f.originalEffect(t, recorded.ContainerID) != recorded.ContainerID {
			t.Fatal("effect identity changed")
		}
		mustPost(t, a, "report", recorded, nil, 204)
		recorded.Note = "conflict"
		mustPost(t, a, "report", recorded, nil, 409)
		var observations, faults int
		if err := h.pool.QueryRow(context.Background(), `SELECT observations FROM transport_commands WHERE id=$1`, e.Command.ID).Scan(&observations); err != nil || observations != 1 {
			t.Fatal("duplicate report changed the recorded outcome")
		}
		if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM transport_faults WHERE server_id=$1 AND hit`, a.identity.ServerID).Scan(&faults); err != nil || faults != 2 {
			t.Fatal("delivery and report response faults were not both exercised")
		}
		// Make the boundary exact without another minute of wall-clock delay.
		for _, offset := range []struct {
			age    time.Duration
			online bool
		}{{offlineAfter - time.Second, true}, {offlineAfter, false}, {offlineAfter + time.Second, false}} {
			if _, err := h.pool.Exec(context.Background(), `UPDATE transport_servers SET last_seen=clock_timestamp()-$2::interval WHERE id=$1`, a.identity.ServerID, offset.age.String()); err != nil {
				t.Fatal(err)
			}
			if got, err := contactOnline(context.Background(), h.pool, a.identity.ServerID); err != nil || got != offset.online {
				t.Fatal("offline boundary mismatch")
			}
		}
		t.Logf("controller empty poll %.3fs; client poll and reconnect %.3fs; live heartbeat gaps %.3f..%.3fs; local execution survived transport stop; one reported Docker effect", serverElapsed.Seconds(), pollElapsed.Seconds(), minGap, maxGap)
	})
}
