package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

const laggingNote = "The admission deadline passed before execution by management's clock. This host's clock is behind management's."

// lagging makes a command timely by this host's clock but expired by management's, as a host whose
// clock lags management's receives a command delivered after its deadline.
func lagging(c *api.ExecutionCommand) {
	expired := true
	c.Deadline, c.Expired = time.Now().Add(time.Hour), &expired
}

// PLAT-138. Management's clock decides a command's deadline. On a host whose clock lags, a command
// management marks expired that would start something ends unrun, saying the host's clock is behind,
// and one that ends work the host holds still runs. The fixture's Docker daemon fails the test on
// any change.
func TestManagementClockDecidesTheDeadline(t *testing.T) {
	for _, phase := range []string{"resolve", "observe", "reserve", "prepare", "hook", "probe", "publish"} {
		t.Run(phase, func(t *testing.T) {
			f := lateDeployment(t, phase, phase != "reserve" && phase != "resolve" && phase != "observe")
			lagging(&f.command)
			if r := f.outcome(t); r.State != "cancelled" || r.Note != laggingNote {
				t.Fatalf("expired %s ended %s: %s", phase, r.State, r.Note)
			}
		})
	}
	t.Run("maintenance-restore", func(t *testing.T) {
		f := expiredMaintenance(t, true)
		f.body.Phase = "restore"
		f.freezeBody()
		lagging(&f.command)
		if r := f.outcome(t); r.State != "cancelled" || r.Note != laggingNote {
			t.Fatalf("expired restore ended %s: %s", r.State, r.Note)
		}
	})
	t.Run("complete-held", func(t *testing.T) {
		f := lateDeployment(t, "complete", true)
		lagging(&f.command)
		if r := f.outcome(t); r.Note == laggingNote || r.Note == deadlineNote {
			t.Fatalf("expired completion of a held reservation ended unrun: %s", r.Note)
		}
	})
	t.Run("timely", func(t *testing.T) {
		f := lateDeployment(t, "resolve", false)
		f.command.Deadline = time.Now().Add(time.Hour)
		if r := f.outcome(t); r.Note == laggingNote || r.Note == deadlineNote {
			t.Fatalf("a timely resolve ended unrun: %s", r.Note)
		}
	})
}

// A command management once delivered as expired stays expired in the journal, whatever a later
// delivery says, and the mark is not part of what the command is.
func TestExpiryStaysWithTheJournaledCommand(t *testing.T) {
	j, p := testVisualJournal(t)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: uuid.New(), Kind: "test", Action: "inspect", Generation: 1, PlanHash: strings.Repeat("a", 64), Deadline: time.Now().Add(time.Hour)}
	if r, err := j.Accept(c); err != nil || r.Command.Expired != nil {
		t.Fatal("a timely command was journaled as expired", err)
	}
	late := c
	lagging(&late)
	late.Deadline = c.Deadline
	if r, err := j.Accept(late); err != nil || r.Command.Expired == nil || !*r.Command.Expired {
		t.Fatal("a later delivery's expiry did not reach the journaled command", err)
	}
	if r, err := j.Accept(c); err != nil || r.Command.Expired == nil || !*r.Command.Expired {
		t.Fatal("a delivery without the mark cleared it", err)
	}
	if !sameCommand(c, late) {
		t.Fatal("the expiry mark changed the command's identity")
	}
}
