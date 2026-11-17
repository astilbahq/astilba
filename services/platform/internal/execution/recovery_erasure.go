package execution

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// RecoveryErasedValue is a secret version recovery restores erased because the management
// database was restored from a backup taken before its environment's key existed. It names the
// value for re-entry, never what it held.
type RecoveryErasedValue struct {
	EnvironmentID uuid.UUID `json:"environmentId"`
	Project       string    `json:"project"`
	Environment   string    `json:"environment"`
	Secret        string    `json:"secret"`
	VersionID     uuid.UUID `json:"versionId"`
}

// RecoveryMissingEnvironment is an environment whose key the restored management database never
// had.
type RecoveryMissingEnvironment struct {
	ID      uuid.UUID
	Project string
	Name    string
	Values  int
}

// RecoveryKeysMissing stops a recovery whose environments' keys the restored management database
// never had, until the operator restores a newer backup or accepts, naming exactly those
// environments, that their values come back erased.
type RecoveryKeysMissing struct {
	Environments []RecoveryMissingEnvironment
	// Accepted is what the operator accepted, when it is not exactly Environments.
	Accepted []uuid.UUID
}

func (e *RecoveryKeysMissing) Error() string {
	var b strings.Builder
	ids := make([]string, len(e.Environments))
	for i, env := range e.Environments {
		ids[i] = env.ID.String()
	}
	if len(e.Accepted) > 0 {
		b.WriteString("--accept-erased-environments must name exactly the environments whose keys are missing, so an acceptance cannot erase more than this recovery needs")
		if len(e.Environments) == 0 {
			b.WriteString("; none is missing, so run recovery without it")
			return b.String()
		}
		b.WriteString(": --accept-erased-environments=" + strings.Join(ids, ","))
		return b.String()
	}
	b.WriteString("recovery stopped: the management database was restored from a backup taken before these environments' keys existed, so their secret values cannot be opened:")
	for _, env := range e.Environments {
		fmt.Fprintf(&b, "\n  %s / %s (%s): %d values", env.Project, env.Name, env.ID, env.Values)
	}
	b.WriteString("\nRestore a management backup taken after their first secrets were saved, or run recovery again with --accept-erased-environments=" + strings.Join(ids, ",") + " to restore those values erased; the recovery report lists each one to enter again.")
	return b.String()
}

// recoveryErasure checks the operator's acceptance against the values recovery cannot open. With
// none, it is no acceptance; otherwise the acceptance must name exactly their environments, and
// it returns them, with the values the report lists.
func recoveryErasure(a portability.Archive, unopened []portability.UnopenedValue, accepted []uuid.UUID) (map[uuid.UUID]bool, []RecoveryErasedValue, error) {
	accept := map[uuid.UUID]bool{}
	for _, id := range accepted {
		accept[id] = true
	}
	names := archiveNames(a)
	missing := map[uuid.UUID]*RecoveryMissingEnvironment{}
	values := make([]RecoveryErasedValue, 0, len(unopened))
	for _, v := range unopened {
		env := names.environments[v.Environment]
		if missing[v.Environment] == nil {
			missing[v.Environment] = &RecoveryMissingEnvironment{ID: v.Environment, Project: names.projects[env.project], Name: env.name}
		}
		missing[v.Environment].Values++
		values = append(values, RecoveryErasedValue{EnvironmentID: v.Environment, Project: names.projects[env.project], Environment: env.name, Secret: names.secrets[v.Secret], VersionID: v.Version})
	}
	exact := len(accept) == len(missing)
	for id := range missing {
		exact = exact && accept[id]
	}
	if exact {
		if len(missing) == 0 {
			return nil, []RecoveryErasedValue{}, nil
		}
		slices.SortFunc(values, func(a, b RecoveryErasedValue) int {
			return strings.Compare(a.Project+"\x00"+a.Environment+"\x00"+a.Secret+"\x00"+a.VersionID.String(), b.Project+"\x00"+b.Environment+"\x00"+b.Secret+"\x00"+b.VersionID.String())
		})
		return accept, values, nil
	}
	refusal := &RecoveryKeysMissing{}
	for _, env := range missing {
		refusal.Environments = append(refusal.Environments, *env)
	}
	slices.SortFunc(refusal.Environments, func(a, b RecoveryMissingEnvironment) int {
		return strings.Compare(a.Project+"\x00"+a.Name+"\x00"+a.ID.String(), b.Project+"\x00"+b.Name+"\x00"+b.ID.String())
	})
	if len(accept) > 0 {
		refusal.Accepted = slices.Clone(accepted)
	}
	return nil, nil, refusal
}

// unopenedToRestore leaves out of unopened the versions the workspace already stores erased: the
// merge keeps a stored erasure, so they need no key, and a later recovery does not stop for a loss
// an earlier one recorded.
func unopenedToRestore(ctx context.Context, q *db.Queries, ws uuid.UUID, unopened []portability.UnopenedValue) ([]portability.UnopenedValue, error) {
	if len(unopened) == 0 {
		return unopened, nil
	}
	ids := make([]uuid.UUID, len(unopened))
	for i, v := range unopened {
		ids[i] = v.Version
	}
	erased, err := q.ErasedSecretVersions(ctx, db.ErasedSecretVersionsParams{WorkspaceID: ws, Ids: ids})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(unopened, func(v portability.UnopenedValue) bool { return slices.Contains(erased, v.Version) }), nil
}

// recordRecoveryErasure records in the audit log each environment whose values the operator
// accepted recovery restores erased.
func recordRecoveryErasure(ctx context.Context, q *db.Queries, in RecoveryImport, erase map[uuid.UUID]bool) error {
	ids := make([]uuid.UUID, 0, len(erase))
	for id := range erase {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, bytesCompareUUID)
	for _, id := range ids {
		if err := q.InsertAudit(ctx, db.InsertAuditParams{WorkspaceID: in.WorkspaceID, ID: uuid.New(), ActorID: in.OwnerID, Action: "workspace.recover_erased_environment_values", ResourceID: id}); err != nil {
			return err
		}
	}
	return nil
}

type archiveEnvironment struct {
	project uuid.UUID
	name    string
}

type recoveryNames struct {
	projects     map[uuid.UUID]string
	environments map[uuid.UUID]archiveEnvironment
	secrets      map[uuid.UUID]string
}

// archiveNames reads the names an archive gives its projects, environments and secrets.
func archiveNames(a portability.Archive) recoveryNames {
	n := recoveryNames{projects: map[uuid.UUID]string{}, environments: map[uuid.UUID]archiveEnvironment{}, secrets: map[uuid.UUID]string{}}
	for _, row := range a.Tables["projects"] {
		var id uuid.UUID
		var name string
		if json.Unmarshal(row["id"], &id) == nil && json.Unmarshal(row["name"], &name) == nil {
			n.projects[id] = name
		}
	}
	for _, row := range a.Tables["environments"] {
		var id, project uuid.UUID
		var name string
		if json.Unmarshal(row["id"], &id) == nil && json.Unmarshal(row["project_id"], &project) == nil && json.Unmarshal(row["name"], &name) == nil {
			n.environments[id] = archiveEnvironment{project, name}
		}
	}
	for _, row := range a.Tables["secrets"] {
		var id uuid.UUID
		var name string
		if json.Unmarshal(row["id"], &id) == nil && json.Unmarshal(row["name"], &name) == nil {
			n.secrets[id] = name
		}
	}
	return n
}
