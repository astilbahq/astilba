package httpapi

import "github.com/astilbahq/astilba/services/platform/internal/agentidentity"

// failureStatus is the HTTP status for a domain error code. An agent's refusals are 401s whose code
// says why, so the agent knows whether to retry, start a new session or enroll again.
func failureStatus(code string) int {
	switch code {
	case "unauthenticated", agentidentity.KeyNotCurrent, agentidentity.NonceInvalid, agentidentity.CounterStale, agentidentity.RefreshReused, agentidentity.CodeNotAccepted:
		return 401
	case "forbidden", "mfa_required", "recent_auth_required", "invitation_signin_required":
		return 403
	case "not_found":
		return 404
	case "conflict", "troubleshooting_refused":
		return 409
	case "rate_limited":
		return 429
	case "temporarily_busy":
		return 503
	default:
		return 400
	}
}
