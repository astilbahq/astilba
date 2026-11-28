package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// PLAT-619. A writer a backup cannot start again keeps no other writer stopped, and every
// writer stays recorded until each runs, so resuming again starts the one left stopped.
func TestBackupWriterThatCannotStartKeepsNoOtherStopped(t *testing.T) {
	j, p := testVisualJournal(t)
	stuck, other := "stuck", "other"
	// The transport serves each request in the caller's goroutine.
	running, refuse, started := map[string]bool{}, true, []string{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[strings.Index(r.URL.Path, "/containers/")+len("/containers/"):]
		id, action, _ := strings.Cut(path, "/")
		switch {
		case r.Method == http.MethodGet && action == "json":
			stablejson.Encode(w, map[string]any{"Id": id, "Config": map[string]any{"Labels": visualLabels(p, uuid.New())}, "State": map[string]any{"Running": running[id]}})
		case r.Method == http.MethodPost && action == "start":
			started = append(started, id)
			if id == stuck && refuse {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"message":"the daemon did not answer"}`)
				return
			}
			running[id] = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"absent"}`)
		}
	})
	transport := scheduleTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	docker, err := client.New(client.WithHost("http://docker-fixture"), client.WithHTTPClient(&http.Client{Transport: transport}), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	e := &Engine{Journal: j, Docker: docker}
	policy := backup.Policy{ID: uuid.New(), Plan: p}
	s := scheduleState{Installation: backup.Installation{Policy: policy}, Runs: []backup.Run{}, Writers: []string{stuck, other}}
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if err = e.resumeBackupWriters(context.Background(), &s); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("resuming past a writer that cannot start gave %v", err)
	}
	saved, err := j.schedule(policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !running[other] || !slices.Equal(s.Writers, []string{stuck, other}) || !slices.Equal(saved.Writers, s.Writers) {
		t.Fatalf("the other writer runs %t, and %v stay recorded, %v saved", running[other], s.Writers, saved.Writers)
	}
	refuse = false
	if err = e.resumeBackupWriters(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if saved, err = j.schedule(policy.ID); err != nil || len(saved.Writers) != 0 || !running[stuck] {
		t.Fatalf("resuming again left %v recorded: %v", saved.Writers, err)
	}
	// The writer already running is not started again.
	if !slices.Equal(started, []string{stuck, other, stuck}) {
		t.Fatalf("started %v", started)
	}
}

// Once the agent is asked to stop, resuming the writers a backup stopped keeps to a grace,
// so the agent stops before systemd kills it, however the run ended. Until then it has the
// whole bound, even when the run ended at its time limit or was cancelled.
func TestResumeKeepsToItsGraceOnceTheAgentStops(t *testing.T) {
	const long, grace = time.Minute, 50 * time.Millisecond
	// ended is how long after start resume ended, or -1 if it did not.
	ended := func(start time.Time, resume context.Context) time.Duration {
		select {
		case <-resume.Done():
			return time.Since(start)
		case <-time.After(5 * time.Second):
			return -1
		}
	}
	for _, test := range []struct {
		name string
		run  func() (context.Context, context.CancelFunc)
	}{
		{"a run that ended at its time limit", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), time.Nanosecond)
		}},
		{"a cancelled run", func() (context.Context, context.CancelFunc) {
			run, cancel := context.WithCancel(context.Background())
			cancel()
			return run, cancel
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, stopRun := test.run()
			defer stopRun()
			<-run.Done()
			stopping, stop := context.WithCancel(context.Background())
			defer stop()
			resume, cancel := resumeContext(run, stopping, long, grace)
			defer cancel()
			time.Sleep(2 * grace)
			if resume.Err() != nil {
				t.Fatal("the resume ended before the agent stopped")
			}
			start := time.Now()
			stop()
			if took := ended(start, resume); took < grace || took > 20*grace {
				t.Fatalf("the resume ended %s after the agent stopped", took)
			}
		})
	}
	t.Run("an agent already stopping", func(t *testing.T) {
		stopping, stop := context.WithCancel(context.Background())
		stop()
		start := time.Now()
		resume, cancel := resumeContext(context.Background(), stopping, long, grace)
		defer cancel()
		if took := ended(start, resume); took < grace || took > 20*grace {
			t.Fatalf("the resume ended after %s", took)
		}
	})
	t.Run("an engine never stopped", func(t *testing.T) {
		resume, cancel := resumeContext(context.Background(), nil, long, grace)
		time.Sleep(4 * grace)
		if resume.Err() != nil {
			t.Fatal("the resume ended before its bound")
		}
		cancel()
		if resume.Err() == nil {
			t.Fatal("cancelling the resume did not end it")
		}
	})
}
