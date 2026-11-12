package domain

import "strings"

// EnvironmentInactive refuses a change to an environment that is retiring or retired. Reads,
// history and configuration export stay available; restoring it allows changes again.
var EnvironmentInactive = &Error{Code: "conflict", Message: "This environment is retired or being retired. Restore it before changing it.", Fields: []string{"environment.state"}}

// Reasons a retirement or restore is refused, reported to the dashboard as retirement.<reason>.
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
}

// RetirementRefused names why an environment cannot be retired or restored now.
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
