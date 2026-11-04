package domain

import "time"

// Initial limits from the interactive troubleshooting design. These are constants,
// not settings; measured use can tune them.
const (
	TroubleshootingPerActor        = 2
	TroubleshootingPerHost         = 4
	TroubleshootingPerWorkspace    = 8
	TroubleshootingPerInstallation = 64
	TroubleshootingMaxDuration     = 60 * time.Minute
	TroubleshootingTicketLifetime  = 30 * time.Second
	TroubleshootingIdle            = 15 * time.Minute
	TroubleshootingReview          = 5 * time.Second
)

// Refusal reasons, reported to the dashboard and recorded in the audit action.
const (
	TroubleshootingInstallation = "installation"
	TroubleshootingEnvironment  = "environment"
	TroubleshootingNotDeployed  = "not_deployed"
	TroubleshootingAgentUpdate  = "agent_update"
	TroubleshootingRuntime      = "runtime"
	TroubleshootingHostNotReady = "host_not_ready"
	TroubleshootingHostOptIn    = "host_opt_in"
	TroubleshootingLimit        = "limit"
	TroubleshootingTicket       = "ticket"
)

var troubleshootingMessages = map[string]string{
	TroubleshootingInstallation: "Terminal sessions are not enabled for this installation.",
	TroubleshootingEnvironment:  "An owner has not enabled terminal sessions for this environment.",
	TroubleshootingNotDeployed:  "This service is not deployed, so there is no container to open a session in.",
	TroubleshootingAgentUpdate:  "Update this server's agent to open terminal sessions.",
	TroubleshootingRuntime:      "Update this server's container runtime to open terminal sessions.",
	TroubleshootingHostNotReady: "This server is not ready for deployments. Check its preflight report.",
	TroubleshootingHostOptIn:    "This server's owner has not enabled terminal sessions on the host.",
	TroubleshootingLimit:        "Too many terminal sessions are open. Close one and try again.",
	TroubleshootingTicket:       "This terminal session's ticket is not valid here. Open a new session.",
}

// TroubleshootingRefused names why a session cannot open; Fields carries the reason.
func TroubleshootingRefused(reason string) error {
	return &Error{Code: "troubleshooting_refused", Message: troubleshootingMessages[reason], Fields: []string{reason}}
}
