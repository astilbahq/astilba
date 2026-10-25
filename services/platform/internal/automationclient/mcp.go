package automationclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type IDInput struct {
	ID string `json:"id" jsonschema:"Plan or operation UUID"`
}
type UpdateInput struct {
	BaseRevisionID string         `json:"baseRevisionId"`
	Specification  map[string]any `json:"specification" jsonschema:"Complete specification with secret references; never secret values"`
}
type PrepareInput struct {
	BaseRevisionID string `json:"baseRevisionId"`
	ServerID       string `json:"serverId"`
}
type ApplyInput struct {
	ID  string `json:"id"`
	Key string `json:"idempotencyKey" jsonschema:"Explicit stable UUID for this apply request"`
}
type BuildInput struct {
	ApplicationID  string `json:"applicationId"`
	BaseRevisionID string `json:"baseRevisionId"`
	TargetHash     string `json:"targetHash" jsonschema:"Configuration hash from build_targets"`
	Key            string `json:"idempotencyKey" jsonschema:"Explicit stable UUID for this build request"`
}

func ID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("an explicit nonzero UUID is required")
	}
	return id, nil
}

func tool[I any](server *mcp.Server, name, description string, read, idempotent bool, run func(context.Context, I) (any, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, IdempotentHint: idempotent}}, func(ctx context.Context, _ *mcp.CallToolRequest, in I) (*mcp.CallToolResult, any, error) {
		out, err := run(ctx, in)
		return nil, out, err
	})
}

func (c *Client) MCP(v api.AutomationContext, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "astilba", Version: version}, &mcp.ServerOptions{SetCacheable: func(_ context.Context, _ mcp.Request, cache *mcp.Cacheable) {
		cache.CacheScope, cache.TTLMs = "private", 0
	}})
	tool(server, "context", "Read this credential's exact environment scope and capabilities.", true, true, func(ctx context.Context, _ struct{}) (any, error) { return c.Context(ctx) })
	tool(server, "draft_get", "Read the saved environment draft; secret values are excluded.", true, true, func(ctx context.Context, _ struct{}) (any, error) { return c.Draft(ctx, v) })
	tool(server, "draft_update", "Save a complete draft against an explicit base revision. This does not deploy it.", false, false, func(ctx context.Context, in UpdateInput) (any, error) {
		base, err := ID(in.BaseRevisionID)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(in.Specification)
		if err != nil {
			return nil, err
		}
		return c.UpdateDraft(ctx, v, api.DraftUpdate{BaseRevisionId: base, Specification: raw})
	})
	tool(server, "plan_prepare", "Prepare a reviewable deployment plan for an explicit draft and server. Poll the returned operation; inspect plan_get before applying.", false, false, func(ctx context.Context, in PrepareInput) (any, error) {
		base, err := ID(in.BaseRevisionID)
		if err != nil {
			return nil, err
		}
		server, err := ID(in.ServerID)
		if err != nil {
			return nil, err
		}
		return c.PreparePlan(ctx, v, api.VisualPlanRequest{BaseRevisionId: base, ServerId: server})
	})
	tool(server, "plan_get", "Inspect a prepared deployment plan and its changes.", true, true, func(ctx context.Context, in IDInput) (any, error) {
		id, err := ID(in.ID)
		if err != nil {
			return nil, err
		}
		return c.Plan(ctx, v, id)
	})
	tool(server, "plan_apply", "Apply an explicitly reviewed plan using a stable idempotency key. This changes running workloads and requires deployment:apply.", false, true, func(ctx context.Context, in ApplyInput) (any, error) {
		id, err := ID(in.ID)
		if err != nil {
			return nil, err
		}
		key, err := ID(in.Key)
		if err != nil {
			return nil, err
		}
		return c.ApplyPlan(ctx, v, id, key)
	})
	tool(server, "build_targets", "List owner-configured build targets, current base revision and configuration hashes in this environment.", true, true, func(ctx context.Context, _ struct{}) (any, error) {
		targets, err := c.Targets(ctx, v)
		return struct {
			Targets []api.ConfiguredBuildTarget `json:"targets"`
		}{targets}, err
	})
	tool(server, "build_run", "Re-run an existing owner-configured target with its expected configuration hash and stable idempotency key. This cannot configure a build or deploy its result.", false, true, func(ctx context.Context, in BuildInput) (any, error) {
		app, err := ID(in.ApplicationID)
		if err != nil {
			return nil, err
		}
		base, err := ID(in.BaseRevisionID)
		if err != nil {
			return nil, err
		}
		key, err := ID(in.Key)
		if err != nil {
			return nil, err
		}
		return c.RunBuild(ctx, v, app, api.RunConfiguredBuild{BaseRevisionId: base, IdempotencyKey: key, TargetHash: in.TargetHash})
	})
	tool(server, "operation_get", "Read a deployment or build operation permitted by this credential in this environment.", true, true, func(ctx context.Context, in IDInput) (any, error) {
		id, err := ID(in.ID)
		if err != nil {
			return nil, err
		}
		return c.Operation(ctx, v, id)
	})
	return server
}
