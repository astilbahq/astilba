package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAutomationCLIAndRealStdioMCP(t *testing.T) {
	f := setup(t)
	b, _, env, path := automationEnvironment(t, f)
	grant := issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "astilba-cli")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cli")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build failed: %v %s", err, out)
	}
	configuration := []string{"PATH=" + os.Getenv("PATH"), "ASTILBA_ORIGIN=" + f.server.URL, "ASTILBA_TOKEN=" + *grant.Token}
	command := exec.CommandContext(ctx, binary, "--development", "draft", "get")
	command.Env = configuration
	out, err := command.CombinedOutput()
	var draft api.Revision
	if err != nil || json.Unmarshal(out, &draft) != nil || draft.Id != env.DraftRevisionId {
		t.Fatalf("real CLI draft read failed: %v %s", err, out)
	}
	// Token files are an alternative to putting credentials in process arguments.
	file := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(file, []byte(*grant.Token), 0644); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(ctx, binary, "--development", "--token-file", file, "context")
	command.Env = configuration[:2]
	out, err = command.CombinedOutput()
	if err == nil || bytes.Contains(out, []byte(*grant.Token)) {
		t.Fatal("publicly readable token file accepted or echoed")
	}
	if err = os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(ctx, binary, "--development", "--token-file", file, "context")
	command.Env = configuration[:2]
	if out, err = command.CombinedOutput(); err != nil || bytes.Contains(out, []byte(*grant.Token)) {
		t.Fatal("private token file failed or was echoed", err)
	}
	var diagnostics bytes.Buffer
	command = exec.CommandContext(ctx, binary, "--development", "mcp")
	command.Env = configuration
	command.Stderr = &diagnostics
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal("real stdio handshake failed", err)
	}
	defer session.Close()
	if session.InitializeResult().ProtocolVersion != "2025-11-25" {
		t.Fatal("legacy client negotiation changed")
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 9 {
		t.Fatal("explicit MCP tools unavailable", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "request" || tool.Name == "shell" {
			t.Fatal("generic authority exposed")
		}
	}
	call := func(name string, input any, failure bool) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		if err != nil || result.IsError != failure {
			t.Fatal("MCP tool result violates API outcome", name, err, result)
		}
		data, _ := json.Marshal(result)
		if strings.Contains(string(data), *grant.Token) {
			t.Fatal("tool output exposed bearer credential")
		}
		return result
	}
	listing := call("build_targets", map[string]any{}, false)
	listingJSON, _ := json.Marshal(listing.StructuredContent)
	var targetOutput struct {
		Targets []api.ConfiguredBuildTarget `json:"targets"`
	}
	if json.Unmarshal(listingJSON, &targetOutput) != nil || targetOutput.Targets == nil {
		t.Fatal("legacy MCP target result must be an object containing targets")
	}
	read := call("draft_get", map[string]any{}, false)
	raw, _ := json.Marshal(read.StructuredContent)
	if json.Unmarshal(raw, &draft) != nil || draft.Id != env.DraftRevisionId {
		t.Fatal("stdio output lost the real revision")
	}
	var spec map[string]any
	json.Unmarshal(draft.Specification, &spec)
	update := map[string]any{"baseRevisionId": draft.Id, "specification": spec}
	call("draft_update", update, false)
	call("draft_update", update, true)
	call("build_run", map[string]any{"applicationId": uuid.NewString(), "baseRevisionId": draft.Id, "idempotencyKey": uuid.NewString(), "targetHash": strings.Repeat("a", 64)}, true)
	b.call("DELETE", path+"/automation-credentials/"+grant.Credential.Id.String(), nil, 204, nil)
	call("context", map[string]any{}, true)
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), *grant.Token) {
		t.Fatal("stdio diagnostics exposed credential")
	}
}
