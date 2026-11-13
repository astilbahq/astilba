package secretbroker

import (
	"fmt"
	"slices"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

// Purpose says why keys are given out. It is recorded with every use, and decides who may ask
// and which kinds of value the session may open or seal.
type Purpose string

const (
	Seal         Purpose = "seal"         // storing new values
	Deliver      Purpose = "deliver"      // deciding and preparing a release to an agent
	Operate      Purpose = "operate"      // management using a credential itself
	Inspect      Purpose = "inspect"      // reading values without using them outside
	Copy         Purpose = "copy"         // copying values to new rows
	Export       Purpose = "export"       // a workspace export
	Recover      Purpose = "recover"      // recovery envelopes, archives and their checks
	Rotate       Purpose = "rotate"       // rewrapping under a new key
	Authenticate Purpose = "authenticate" // checking a second factor
	Token        Purpose = "token"        // the resident key for self-issued tokens
)

// Kind is a kind of stored value. Its identity, bound into the ciphertext, is built here from
// the kind, the request's workspace and the row's ID, so a session cannot be pointed at another
// workspace's value.
type Kind string

const (
	SecretVersion       Kind = "secret-version"
	BuildSecret         Kind = "build-secret"
	Git                 Kind = "git"
	GitHubAuthorization Kind = "github-authorization"
	Registry            Kind = "registry"
	BackupDestination   Kind = "backup-destination"
	BackupPolicy        Kind = "backup-policy"
	AlertWebhook        Kind = "alert-webhook"
	RecoveryCheckpoint  Kind = "recovery-checkpoint"
	TOTP                Kind = "totp"
	TOTPPending         Kind = "totp-pending"
)

// Integration kinds share one identity scheme with the kind's own name in it.
var integrationKinds = []Kind{BuildSecret, Git, GitHubAuthorization, Registry, BackupDestination, BackupPolicy}

// Item is one stored value: its kind and the ID of its row.
type Item struct {
	Kind Kind      `json:"kind"`
	ID   uuid.UUID `json:"id"`
	// A recovery checkpoint's identity is its descriptor's, and names its own workspace.
	checkpoint string
	workspace  uuid.UUID
}

// Checkpoint is the item for a recovery checkpoint's envelope.
func Checkpoint(d deployment.RecoveryDescriptor) Item {
	return Item{
		Kind:       RecoveryCheckpoint,
		ID:         d.CheckpointID,
		checkpoint: fmt.Sprintf("cross-host-recovery:v1:%s:%s:%s:%d:%s", d.WorkspaceID, d.ParentID, d.CheckpointID, d.Sequence, d.RootHash),
		workspace:  d.WorkspaceID,
	}
}

func (i Item) installation() bool { return i.Kind == TOTP || i.Kind == TOTPPending }

// identity is what the value's ciphertext is bound to. It returns "" for an item that does not
// belong to the workspace.
func (i Item) identity(ws uuid.UUID) string {
	switch {
	case i.installation():
		if ws != uuid.Nil {
			return ""
		}
		if i.Kind == TOTPPending {
			return "totp-pending:v1:" + i.ID.String()
		}
		return "totp:v1:" + i.ID.String()
	case ws == uuid.Nil:
		return ""
	case i.Kind == SecretVersion:
		return "secret:v1:" + ws.String() + ":" + i.ID.String()
	case i.Kind == AlertWebhook:
		// One per workspace, named by the workspace's own ID.
		if i.ID != ws {
			return ""
		}
		return "astilba/alert-webhook/" + ws.String()
	case i.Kind == RecoveryCheckpoint:
		if i.workspace != ws || i.checkpoint == "" {
			return ""
		}
		return i.checkpoint
	case slices.Contains(integrationKinds, i.Kind):
		return "pipeline:" + string(i.Kind) + ":" + ws.String() + ":" + i.ID.String()
	}
	return ""
}

var workspaceKinds = append([]Kind{SecretVersion, AlertWebhook, RecoveryCheckpoint}, integrationKinds...)
var installationKinds = []Kind{TOTP, TOTPPending}

// RecoveryKinds is what recovery may open: every workspace value, envelopes included.
var RecoveryKinds = slices.Clone(workspaceKinds)

// CredentialKinds are the values a workspace export carries.
var CredentialKinds = []Kind{SecretVersion, Git, Registry, BuildSecret, BackupDestination, BackupPolicy}

// rule is what a purpose allows.
type rule struct {
	principals []PrincipalKind
	open, seal []Kind
	// owner needs an account principal to own the workspace.
	owner bool
	// limit is how long a session may last. Longer sessions wait for locks with keys in hand.
	limit time.Duration
}

const (
	defaultLimit = 2 * time.Minute
	batchLimit   = 15 * time.Minute
)

var people = []PrincipalKind{AccountPrincipal, AutomationPrincipal, SystemPrincipal}

// rules is the purpose table. A purpose not here cannot begin a session.
var rules = map[Purpose]rule{
	// A recovery checkpoint is opened again straight after sealing, to prove it reads back.
	Seal:    {principals: people, seal: append(slices.Clone(workspaceKinds), installationKinds...), open: []Kind{RecoveryCheckpoint}, limit: defaultLimit},
	Deliver: {principals: []PrincipalKind{AgentPrincipal}, open: []Kind{SecretVersion, BuildSecret, Git, GitHubAuthorization, Registry, BackupDestination, BackupPolicy, RecoveryCheckpoint}, limit: defaultLimit},
	// Operating with a GitHub grant or connection can refresh and store it again.
	Operate:      {principals: people, open: []Kind{Git, GitHubAuthorization, Registry, AlertWebhook}, seal: []Kind{Git, GitHubAuthorization}, limit: defaultLimit},
	Inspect:      {principals: people, open: []Kind{SecretVersion, BuildSecret}, limit: defaultLimit},
	Copy:         {principals: people, open: []Kind{SecretVersion}, seal: []Kind{SecretVersion}, limit: defaultLimit},
	Export:       {principals: []PrincipalKind{AccountPrincipal}, owner: true, open: workspaceKinds, limit: batchLimit},
	Recover:      {principals: []PrincipalKind{AccountPrincipal, SystemPrincipal, OperatorPrincipal}, owner: true, open: workspaceKinds, seal: workspaceKinds, limit: batchLimit},
	Rotate:       {principals: []PrincipalKind{OperatorPrincipal}, open: append(slices.Clone(workspaceKinds), installationKinds...), seal: append(slices.Clone(workspaceKinds), installationKinds...), limit: batchLimit},
	Authenticate: {principals: []PrincipalKind{AccountPrincipal}, open: installationKinds, seal: installationKinds, limit: defaultLimit},
}

// Reasons recorded when a request gives none.
var defaultReasons = map[Purpose]string{Seal: "write", Deliver: "first-release", Operate: "operate", Inspect: "inspect", Copy: "copy", Export: "export", Recover: "recover", Rotate: "rewrap", Authenticate: "sign-in", Token: "start-up"}
