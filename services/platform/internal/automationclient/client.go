package automationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

const responseLimit = 2 << 20

type Client struct {
	origin string
	token  string
	http   *http.Client
}

func New(origin, token string, development bool) (*Client, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "" && u.Path != "/" {
		return nil, errors.New("origin must be an exact management HTTPS origin")
	}
	loopback, err := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && !(development && u.Scheme == "http" && err == nil && loopback.IsLoopback()) {
		return nil, errors.New("HTTPS is required; development HTTP requires an explicit loopback IP")
	}
	if !strings.HasPrefix(token, "astilba_auto_") || len(token) > 128 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("a scoped automation token is required")
	}
	u.Path = ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{origin: u.String(), token: token, http: &http.Client{Timeout: 60 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return errors.New("invalid request input")
		}
		if len(raw) > 150000 {
			return errors.New("request exceeds the API size limit")
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return errors.New("cannot prepare API request")
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("management request failed; check connectivity and TLS")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return errors.New("management response is incomplete or exceeds the size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure api.Error
		if json.Unmarshal(raw, &failure) == nil && failure.Code != "" {
			// Do not echo response bodies or remote diagnostics containing tokens.
			return fmt.Errorf("management rejected request (HTTP %d, %s)", response.StatusCode, safeCode(failure.Code))
		}
		return fmt.Errorf("management rejected request (HTTP %d)", response.StatusCode)
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		return errors.New("management returned an invalid response")
	}
	return nil
}

func safeCode(code string) string {
	switch code {
	case "unauthenticated", "forbidden", "not_found", "conflict", "invalid_input", "mfa_required", "recent_auth_required", "subscription_required", "email_verification_required", "rate_limited", "internal_error":
		return code
	}
	return "unknown_error"
}

func (c *Client) Context(ctx context.Context) (api.AutomationContext, error) {
	var out api.AutomationContext
	err := c.request(ctx, "GET", "/api/v1/automation/context", nil, &out)
	return out, err
}

func scope(v api.AutomationContext) string {
	return "/api/v1/workspaces/" + v.WorkspaceId.String() + "/projects/" + v.ProjectId.String() + "/environments/" + v.EnvironmentId.String()
}

func (c *Client) Draft(ctx context.Context, v api.AutomationContext) (api.Revision, error) {
	var out api.Revision
	err := c.request(ctx, "GET", scope(v)+"/draft", nil, &out)
	return out, err
}

func (c *Client) UpdateDraft(ctx context.Context, v api.AutomationContext, in api.DraftUpdate) (api.Revision, error) {
	var out api.Revision
	err := c.request(ctx, "PUT", scope(v)+"/draft", in, &out)
	return out, err
}

func (c *Client) PreparePlan(ctx context.Context, v api.AutomationContext, in api.VisualPlanRequest) (api.VisualPlanStatus, error) {
	var out api.VisualPlanStatus
	err := c.request(ctx, "POST", scope(v)+"/deployment-plans", in, &out)
	return out, err
}

func (c *Client) Plan(ctx context.Context, v api.AutomationContext, id uuid.UUID) (api.VisualPlanStatus, error) {
	var out api.VisualPlanStatus
	err := c.request(ctx, "GET", scope(v)+"/deployment-plans/"+id.String(), nil, &out)
	return out, err
}

func (c *Client) ApplyPlan(ctx context.Context, v api.AutomationContext, id, key uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := c.request(ctx, "POST", scope(v)+"/deployment-plans/"+id.String()+"/apply", api.ApplyPlan{IdempotencyKey: key}, &out)
	return out, err
}

func (c *Client) Targets(ctx context.Context, v api.AutomationContext) ([]api.ConfiguredBuildTarget, error) {
	out := []api.ConfiguredBuildTarget{}
	err := c.request(ctx, "GET", scope(v)+"/build-targets", nil, &out)
	return out, err
}

func (c *Client) RunBuild(ctx context.Context, v api.AutomationContext, app uuid.UUID, in api.RunConfiguredBuild) (api.Operation, error) {
	var out api.Operation
	err := c.request(ctx, "POST", scope(v)+"/build-targets/"+app.String()+"/runs", in, &out)
	return out, err
}

func (c *Client) Operation(ctx context.Context, v api.AutomationContext, id uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := c.request(ctx, "GET", scope(v)+"/operations/"+id.String(), nil, &out)
	return out, err
}
