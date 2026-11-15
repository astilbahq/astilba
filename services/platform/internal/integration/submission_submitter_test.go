package integration

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/google/uuid"
)

// A submission is sent on its submitter's say-so, and can wait up to an hour before it is. Losing
// write access fails it at once, so getting it back sends nothing, and each attempt checks again
// that the submitter has the credentials they submitted with and meets the workspace's two-factor
// requirement, before it asks the provider for anything.
func TestSubmissionIsSentOnlyWhileItsSubmitterMayWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	x := newSubmissionFixture(t, ctx)
	members := x.prefix + "/members/"
	const withdrawn, refused = "lost write access before it was sent", "the member who submitted it can no longer send it"
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
			job, attempt := holdSubmission(t, x, queued.Id)
			test.change(t, submitter, id, queued.Id)
			requests, writes := x.requests.Load(), x.writes.Load()
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE river_job SET scheduled_at=now() WHERE id=$1", job); err != nil {
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
