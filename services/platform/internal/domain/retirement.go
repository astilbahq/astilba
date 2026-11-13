package domain

import "strings"

// EnvironmentInactive refuses a change to an environment that is not active: retiring, retired or
// purged. Reads, history and configuration export stay available; restoring a retired environment
// allows changes again, and a purged one is never restored.
var EnvironmentInactive = &Error{Code: "conflict", Message: "This environment is not active, so it cannot be changed. Restore it first if it is retired; one being retired, purging or purged cannot be restored.", Fields: []string{"environment.state"}}

// SecretInUse refuses to retire a secret while something that may still deploy names one of its
// versions.
var SecretInUse = &Error{Code: "conflict", Message: "A version of this secret is still used by the draft, the running deployment, a plan or an operation. Replace it there first.", Fields: []string{"secret.inUse"}}

// Reasons a retirement, restore or purge is refused, reported to the dashboard as
// retirement.<reason>.
const (
	RetirementActiveOperation = "active_operation"
	RetirementServerBusy      = "server_busy"
	RetirementUpgrade         = "upgrade"
	RetirementCutover         = "cutover"
	RetirementCrossHost       = "cross_host"
	RetirementReplacement     = "replacement"
	RetirementAgentUpdate     = "agent_update"
	RetirementServerTransfer  = "server_transfer"
	RetirementState           = "state"
	RetirementVolumes         = "volumes"
	RetirementPurgeAgent      = "purge_agent_update"
)

var retirementMessages = map[string]string{
	RetirementActiveOperation: "Another operation is running for this environment. Wait for it to finish or cancel it, then try again.",
	RetirementServerBusy:      "Another operation is running on this environment's server. Try again when it finishes.",
	RetirementUpgrade:         "A database upgrade holds this environment's data. Finish or return the upgrade and wait for its retention period to end.",
	RetirementCutover:         "A backup restore cutover is waiting to be deployed. Deploy it or replace the draft first.",
	RetirementCrossHost:       "This environment runs on more than one server. Consolidate it onto its primary server and deploy it without a placement first.",
	RetirementReplacement:     "This environment's replacement deployment did not finish, and the server it replaced is no longer revoked, so both may run it. Finish the replacement deployment or revoke the former server first.",
	RetirementAgentUpdate:     "Update this server's agent before retiring environments on it.",
	RetirementServerTransfer:  "This environment's server is waiting for a verified agent transfer. Complete the transfer first.",
	RetirementState:           "This environment is not in a state that allows this. Reload it to see its current state.",
	RetirementVolumes:         "The volumes this environment keeps have changed. Reload it and confirm the current list.",
	RetirementPurgeAgent:      "Update this server's agent before deleting retained data on it.",
}

// RetirementRefused names why an environment cannot be retired, restored or purged now.
func RetirementRefused(reason string) error {
	return &Error{Code: "conflict", Message: retirementMessages[reason], Fields: []string{"retirement." + reason}}
}

// RouteClaimed refuses a plan whose host route or port another environment on the server serves,
// such as one that took a retired environment's hostname before it was restored.
func RouteClaimed(claim string) error {
	what := claim
	if host, ok := strings.CutPrefix(claim, "http:"); ok {
		what = host
	} else if port, ok := strings.CutPrefix(claim, "tcp:"); ok {
		what = "TCP port " + port
	}
	return &Error{Code: "conflict", Message: "Another environment on this server already serves " + what + ". Remove it from one of them first.", Fields: []string{"routes.claimed"}}
}
