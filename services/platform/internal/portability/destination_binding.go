package portability

import (
	"encoding/json/v2"

	"github.com/google/uuid"
)

// destinationUse is what an archive's backup policies say about one destination's servers.
type destinationUse struct{ servers map[uuid.UUID]bool }

// destinationUses reads which servers the policies using each destination are on. A container
// job uses none.
func destinationUses(a Archive) (map[uuid.UUID]*destinationUse, error) {
	out := map[uuid.UUID]*destinationUse{}
	for _, row := range a.Tables["backup_policies"] {
		var policy struct {
			Method        string    `json:"method"`
			DestinationID uuid.UUID `json:"destinationId"`
		}
		var server uuid.UUID
		if json.Unmarshal(row["configuration"], &policy) != nil || json.Unmarshal(row["server_id"], &server) != nil {
			return nil, ErrArchive
		}
		if policy.Method == "container-job" {
			continue
		}
		if out[policy.DestinationID] == nil {
			out[policy.DestinationID] = &destinationUse{servers: map[uuid.UUID]bool{}}
		}
		out[policy.DestinationID].servers[server] = true
	}
	return out, nil
}

// destinationBinding is a destination row's id, kind, server and shared flag.
type destinationBinding struct {
	id     uuid.UUID
	kind   string
	server *uuid.UUID
	shared bool
}

func readDestinationBinding(row Row, bound bool) (destinationBinding, error) {
	var b destinationBinding
	var configuration struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(row["id"], &b.id) != nil || json.Unmarshal(row["configuration"], &configuration) != nil {
		return b, ErrArchive
	}
	b.kind = configuration.Kind
	if bound && (json.Unmarshal(row["server_id"], &b.server) != nil || json.Unmarshal(row["shared"], &b.shared) != nil) {
		return b, ErrArchive
	}
	return b, nil
}

// bindArchiveDestinations binds the destinations of an archive from before version 19 as
// migration 00075 binds a database's: an S3 destination to the one server its policies are on, or
// shared when they are on several. One no policy uses binds at its first policy.
func bindArchiveDestinations(a *Archive) error {
	uses, err := destinationUses(*a)
	if err != nil {
		return err
	}
	for _, row := range a.Tables["backup_destinations"] {
		if _, exists := row["server_id"]; exists {
			return ErrArchive
		}
		if _, exists := row["shared"]; exists {
			return ErrArchive
		}
		b, err := readDestinationBinding(row, false)
		if err != nil {
			return err
		}
		var server *uuid.UUID
		shared := false
		if use := uses[b.id]; use != nil && b.kind == "s3" {
			for s := range use.servers {
				server = &s
			}
			if len(use.servers) > 1 {
				server, shared = nil, true
			}
		}
		setValue(row, "server_id", server)
		setValue(row, "shared", shared)
	}
	return nil
}

// checkDestinationBindings refuses an archive whose destinations' bindings a database couldn't
// hold, or that its policies don't keep: a policy uses only an S3 destination bound to its own
// server, or a shared one.
func checkDestinationBindings(a Archive) error {
	uses, err := destinationUses(a)
	if err != nil {
		return err
	}
	for _, row := range a.Tables["backup_destinations"] {
		b, err := readDestinationBinding(row, true)
		if err != nil {
			return err
		}
		if b.shared && b.server != nil || b.kind != "s3" && (b.server != nil || b.shared) {
			return ErrArchive
		}
		use := uses[b.id]
		if use == nil || b.kind != "s3" || b.shared {
			continue
		}
		if b.server == nil || len(use.servers) != 1 || !use.servers[*b.server] {
			return ErrArchive
		}
	}
	return nil
}
