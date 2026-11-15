package integration

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/google/uuid"
)

// A submission is sent on its submitter's say-so, and can wait up to an hour before it is. Losing
// write access fails it, so getting it back sends nothing, and each attempt checks again
// that the submitter has the credentials they submitted with and meets the workspace's two-factor
// requirement, before it asks the provider for anything.
func TestSubmissionIsSentOnlyWhileItsSubmitterMayWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	x := newSubmissionFixture(t, ctx)
	members := x.prefix + "/members/"
	const withdrawn, refused = "the member who submitted it lost write access", "the member who submitted it can no longer send it"
	for i, test := range []struct {
		name string
		// change happens while the submission waits for its next attempt.
		change func(t *testing.T, submitter *browser, account, submission uuid.UUID)
		// refusal is in the note of a submission that is refused, and empty for one still sent.
		refusal string
	}{
		{"an operator still in the workspace is sent", func(*testing.T, *browser, uuid.UUID, uuid.UUID) {}, ""},
		{"a removed submitter's is withdrawn", func(t *testing.T, _ *browser, account, _ uuid.UUID) {
			x.owner.call("DELETE", members+account.String(), nil, 204, nil)
		}, withdrawn},
		{"a submitter demoted and promoted again has theirs withdrawn", func(t *testing.T, _ *browser, account, _ uuid.UUID) {
			x.owner.call("PUT", members+account.String(), map[string]string{"role": "viewer"}, 204, nil)
			x.owner.call("PUT", members+account.String(), map[string]string{"role": "operator"}, 204, nil)
		}, withdrawn},
		{"a submitter whose password changed has theirs refused", func(t *testing.T, submitter *browser, _, _ uuid.UUID) {
			submitter.call("POST", "/api/v1/auth/password", map[string]any{"password": "a changed password here", "current": map[string]string{"password": x.f.password}}, 200, nil)
		}, refused},
		// Turning the requirement on needs an owner's own two-factor authentication, which this
		// fixture's owner doesn't have, and changes nothing else.
		{"a submitter without the two-factor authentication the workspace now requires has theirs refused", func(t *testing.T, _ *browser, _, submission uuid.UUID) {
			require := func(on bool) {
				if _, err := x.f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=$2 WHERE id=(SELECT workspace_id FROM source_submissions WHERE id=$1)", submission, on); err != nil {
					t.Fatal(err)
				}
			}
			require(true)
			t.Cleanup(func() { require(false) })
		}, refused},
		// A release before the check records no version, so what it queued is not sent on a guess.
		{"one queued by an earlier release is refused", func(t *testing.T, _ *browser, _, submission uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE source_submissions SET actor_auth_version=NULL WHERE id=$1", submission); err != nil {
				t.Fatal(err)
			}
		}, "an earlier release of Astilba queued it"},
	} {
		t.Run(test.name, func(t *testing.T) {
			email := "submitter-" + string(rune('a'+i)) + "@example.test"
			submitter := inviteMember(t, x.f, x.owner, x.prefix, email, "operator")
			submitter.t = t
			var id uuid.UUID
			if err := x.f.st.Pool.QueryRow(ctx, "SELECT id FROM accounts WHERE email=$1", email).Scan(&id); err != nil {
				t.Fatal(err)
			}
			path, draft := x.prepare(t, "Submitter "+string(rune('a'+i)), func(bundle *composeproject.Bundle) {
				bundle.Files[0].Content += "# proposed\n"
			})
			queued := x.submit(t, submitter, path, draft)
			x.await(t, queued.Id, submissionRetrying)
			held, attempt := holdSubmission(t, x, queued.Id)
			test.change(t, submitter, id, queued.Id)
			// Losing write access fails the submission at once, but an attempt may be sending it, so
			// another for the revision waits until that one must have ended.
			if test.refusal == withdrawn {
				if again := x.submit(t, nil, path, draft); again.Id != queued.Id || again.State != "failed" {
					t.Fatal("a submission started beside one that may still be sending", again)
				}
				if _, err := x.f.st.Pool.Exec(ctx, "UPDATE source_submissions SET withdrawn_at=withdrawn_at-interval '1 minute' WHERE id=$1", queued.Id); err != nil {
					t.Fatal(err)
				}
				again := x.submit(t, nil, path, draft)
				if again.Id == queued.Id || again.State != "queued" {
					t.Fatal("the draft could not be submitted again once the withdrawn attempt had ended", again)
				}
				x.await(t, again.Id, submissionRetrying)
				holdSubmission(t, x, again.Id)
			}
			requests, writes := x.requests.Load(), x.writes.Load()
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE river_job SET scheduled_at=now() WHERE id=$1", held); err != nil {
				t.Fatal(err)
			}
			if test.refusal == "" {
				next := "Attempt " + strconv.Itoa(attempt+1) + " of"
				x.await(t, queued.Id, func(_, note string) bool { return strings.HasPrefix(note, next) })
				if x.writes.Load() == writes {
					t.Fatal("the next attempt did not write")
				}
				// Its later attempts would reach the provider while the next case is watching it.
				holdSubmission(t, x, queued.Id)
				return
			}
			state, note := x.await(t, queued.Id, submissionFinished)
			if state != "failed" || !strings.HasPrefix(note, "Not submitted: ") || !strings.Contains(note, test.refusal) {
				t.Fatal("the submission was not refused for its submitter", state, note)
			}
			// The held attempt runs even for a submission already failed, and must stop short of
			// the provider.
			for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				var job string
				if err := x.f.st.Pool.QueryRow(ctx, "SELECT state FROM river_job WHERE id=$1", held).Scan(&job); err != nil {
					t.Fatal(err)
				}
				if job == "completed" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the refused attempt did not finish its job", job)
				}
			}
			if x.requests.Load() != requests {
				t.Fatal("a refused attempt reached the provider")
			}
		})
	}
}

// holdSubmission keeps the submission's next attempt from starting until the test schedules it,
// and returns its job and the attempts it has made. River may already have started the retry it
// set, so the attempt held is whichever comes next.
func holdSubmission(t *testing.T, x *submissionFixture, id uuid.UUID) (int64, int) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var job int64
		var attempt int
		err := x.f.st.Pool.QueryRow(x.ctx, "UPDATE river_job SET scheduled_at=now()+interval '1 hour' WHERE kind='platform_source_submission_v1' AND state IN ('available','retryable') AND args->>'id'=$1 RETURNING id,attempt", id.String()).Scan(&job, &attempt)
		if err == nil {
			return job, attempt
		}
		if time.Now().After(deadline) {
			t.Fatal("the submission did not wait to be retried:", err)
		}
	}
}

// An attempt already sending a submission when its submitter loses write access was started on
// their say-so, so it finishes and records what the provider did, and no other attempt for the
// draft starts beside it.
func TestSubmissionWithdrawnWhileSendingRecordsWhatTheProviderDid(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	x := newSubmissionFixtureWith(t, ctx, true)
	submitter := inviteMember(t, x.f, x.owner, x.prefix, "submitter@example.test", "operator")
	var account uuid.UUID
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT id FROM accounts WHERE email='submitter@example.test'").Scan(&account); err != nil {
		t.Fatal(err)
	}
	path, draft := x.prepare(t, "Withdrawn while sending", func(bundle *composeproject.Bundle) {
		bundle.Files[0].Content += "# proposed\n"
	})
	// Gitea's submission starts by making the branch, so holding that holds the attempt after
	// it has decided to send.
	entered, release := x.forge.Hold("POST", "/branches")
	defer release()
	sending := x.submit(t, submitter, path, draft)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the attempt did not start sending")
	}
	x.owner.call("DELETE", x.prefix+"/members/"+account.String(), nil, 204, nil)
	if state, note := x.await(t, sending.Id, submissionFinished); state != "failed" || !strings.Contains(note, "lost write access") {
		t.Fatal("removing the submitter did not fail the submission", state, note)
	}
	if again := x.submit(t, nil, path, draft); again.Id != sending.Id {
		t.Fatal("a submission started beside the attempt still sending", again)
	}
	release()
	state, note := x.await(t, sending.Id, func(state, _ string) bool { return state == "succeeded" })
	var status struct {
		Status     string                `json:"status"`
		Submission *api.SourceSubmission `json:"submission"`
	}
	x.owner.call("GET", path+"/source", nil, 200, &status)
	recorded := status.Submission
	if recorded == nil || recorded.Id != sending.Id || recorded.ReviewNumber == nil || *recorded.ReviewNumber != 1 || recorded.Commit == nil || status.Status != "awaiting_merge" || !strings.Contains(note, "already started") {
		t.Fatalf("the provider's answer was not recorded: %s %s %+v %s", state, note, recorded, status.Status)
	}
	unlock := x.forge.Lock()
	writes := slices.Clone(x.forge.Writes)
	unlock()
	if !slices.Equal(writes, []string{"commit", "review"}) {
		t.Fatal("the provider was written to other than by the one attempt", writes)
	}
}
