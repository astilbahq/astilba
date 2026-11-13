package secretbroker

import (
	"regexp"

	"github.com/google/uuid"
)

type PrincipalKind string

const (
	AccountPrincipal    PrincipalKind = "account"
	AutomationPrincipal PrincipalKind = "automation"
	AgentPrincipal      PrincipalKind = "agent"
	SystemPrincipal     PrincipalKind = "system"
	OperatorPrincipal   PrincipalKind = "operator"
)

// Principal is who keys are given out for. Build one with the constructors below.
type Principal struct {
	Kind       PrincipalKind
	Account    *uuid.UUID
	Automation *uuid.UUID
	Server     *uuid.UUID
	// Instance is the agent instance, or the credential generation, a release went to.
	Instance string
	// Job names a system principal's work, such as "alert-delivery".
	Job string
	// Operator is a reference to an operator's own record, never shown to tenants.
	Operator string
	// automationWorkspace is the workspace an automation grant was issued for.
	automationWorkspace uuid.UUID
}

func Account(id uuid.UUID) Principal { return Principal{Kind: AccountPrincipal, Account: &id} }

// Automation is an automation credential acting for its account, within the one workspace it
// was issued for.
func Automation(account, grant, workspace uuid.UUID) Principal {
	return Principal{Kind: AutomationPrincipal, Account: &account, Automation: &grant, automationWorkspace: workspace}
}

func Agent(server uuid.UUID, instance string) Principal {
	return Principal{Kind: AgentPrincipal, Server: &server, Instance: instance}
}

func System(job string) Principal { return Principal{Kind: SystemPrincipal, Job: job} }

func Operator(ref string) Principal { return Principal{Kind: OperatorPrincipal, Operator: ref} }

var jobName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// wellFormed checks the principal carries what its kind needs, and nothing that belongs to
// another kind.
func (p Principal) wellFormed() bool {
	switch p.Kind {
	case AccountPrincipal:
		return p.Account != nil && p.Automation == nil && p.Server == nil && p.Job == "" && p.Operator == ""
	case AutomationPrincipal:
		return p.Account != nil && p.Automation != nil && p.automationWorkspace != uuid.Nil && p.Server == nil && p.Job == "" && p.Operator == ""
	case AgentPrincipal:
		return p.Server != nil && p.Account == nil && len(p.Instance) <= 200 && p.Job == "" && p.Operator == ""
	case SystemPrincipal:
		return jobName.MatchString(p.Job) && p.Account == nil && p.Server == nil && p.Operator == ""
	case OperatorPrincipal:
		return p.Operator != "" && len(p.Operator) <= 200 && p.Account == nil && p.Server == nil && p.Job == ""
	}
	return false
}
