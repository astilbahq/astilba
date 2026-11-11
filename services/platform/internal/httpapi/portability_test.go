package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A request that waits out the archive turn is told the server is busy, not that its account
// went over its budget, and gets the turn once it is free.
func TestArchiveTurnAnswersBusyAfterItsWait(t *testing.T) {
	turns := newArchiveTurns()
	turns.wait = 20 * time.Millisecond
	release, err := turns.take(context.Background(), turns.slot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = turns.take(context.Background(), turns.slot); !errors.Is(err, archivesBusy) {
		t.Fatal("a waiter that waited out the turn was not told the server is busy", err)
	}
	release()
	release, err = turns.take(context.Background(), turns.slot)
	if err != nil {
		t.Fatal("a free turn was refused", err)
	}
	release()
}

// An account holds one archive request at a time, and releasing it frees the account.
func TestArchiveHoldIsOnePerAccount(t *testing.T) {
	turns := newArchiveTurns()
	account, other := uuid.New(), uuid.New()
	release, held := turns.hold(account)
	if !held {
		t.Fatal("first request refused")
	}
	if _, again := turns.hold(account); again {
		t.Fatal("an account held two archive requests")
	}
	if done, held := turns.hold(other); !held {
		t.Fatal("another account was refused")
	} else {
		done()
	}
	release()
	if done, held := turns.hold(account); !held {
		t.Fatal("a released account stayed held")
	} else {
		done()
	}
}
