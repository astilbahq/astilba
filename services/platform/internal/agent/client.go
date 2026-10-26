package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
)

type Client struct {
	Journal        *Journal
	Engine         *Engine
	HTTP           *http.Client
	Observe        func(context.Context) api.AgentHeartbeat
	Metrics        func(context.Context) api.MetricReport
	buildControls  atomic.Bool
	buildCache     atomic.Bool
	railpack       atomic.Bool
	safeReleases   atomic.Bool
	serviceOutputs atomic.Bool
	previews       atomic.Bool
	schedules      atomic.Bool
}

func NewClient(j *Journal, e *Engine) *Client {
	return &Client{Journal: j, Engine: e, HTTP: &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second, MaxConnsPerHost: 4}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func ValidateOrigin(origin string) error {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("management must be an exact HTTPS origin")
	}
	return nil
}
func (c *Client) call(ctx context.Context, method, path string, body, out any, token string) error {
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	// Older protocol-1 controllers reject unknown capability fields. Advertise
	// extensions only after this controller has explicitly accepted that schema.
	switch value := body.(type) {
	case api.AgentEnrollment:
		value.Capabilities = c.advertisedCapabilities(value.Capabilities)
		body = value
	case api.AgentHeartbeat:
		value.Capabilities = c.advertisedCapabilities(value.Capabilities)
		body = value
	}
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, method, cfg.Origin+path, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.ServerID.String()+"."+token)
	}
	req.Header.Set("Content-Type", "application/json")
	response, e := c.HTTP.Do(req)
	if e != nil {
		return errors.New("management connection unavailable")
	}
	defer response.Body.Close()
	// Also forget an extension after controller replacement or downgrade.
	c.buildControls.Store(response.Header.Get("Astilba-Agent-Features") == "build-controls")
	c.buildCache.Store(response.Header.Get("Astilba-Agent-Build-Cache") == "v1")
	c.railpack.Store(response.Header.Get("Astilba-Agent-Railpack") == "v1")
	c.safeReleases.Store(response.Header.Get("Astilba-Agent-Safe-Releases") == "v1")
	c.serviceOutputs.Store(response.Header.Get("Astilba-Agent-Service-Outputs") == "v1")
	c.previews.Store(response.Header.Get("Astilba-Agent-Previews") == "v1")
	c.schedules.Store(response.Header.Get("Astilba-Agent-Schedules") == "v1")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("management rejected request; check enrollment, credentials and operation status")
	}
	limit := int64(16385)
	if path == "/agent/v1/poll" {
		limit = 262145
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(response.Body, limit))
		return e
	}
	d := json.NewDecoder(io.LimitReader(response.Body, limit))
	d.DisallowUnknownFields()
	if e = d.Decode(out); e != nil {
		return errors.New("invalid management response")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("management response exceeds limits")
	}
	return nil
}

func (c *Client) advertisedCapabilities(v api.AgentCapabilities) api.AgentCapabilities {
	if !c.schedules.Load() {
		v.Schedules = nil
	}
	if !c.previews.Load() {
		v.Previews = nil
	}
	if !c.serviceOutputs.Load() {
		v.ServiceOutputs = nil
	}
	if !c.safeReleases.Load() {
		v.SafeReleases = nil
	}
	if !c.buildControls.Load() {
		v.BuildControls = nil
	}
	if !c.buildCache.Load() {
		v.BuildCache = nil
	}
	if !c.railpack.Load() {
		v.RailpackVersion = nil
	}
	return v
}
func (c *Client) Enroll(ctx context.Context, token string) error {
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	token = strings.TrimSpace(token)
	if token == "" {
		token = cfg.EnrollmentToken
	}
	if len(token) != 43 {
		return errors.New("a valid enrollment token is required")
	}
	if cfg.Credential == "" {
		cfg.Credential, e = cryptobox.Token()
		if e != nil {
			return e
		}
	}
	cfg.EnrollmentToken = token
	if e = c.Journal.SetConfig(cfg); e != nil {
		return e
	}

	h := c.Observe(ctx)
	var status api.AgentStatus
	enrollment := api.AgentEnrollment{ServerId: cfg.ServerID, MachineId: cfg.MachineID, Credential: cfg.Credential, Token: strings.TrimSpace(token), Capabilities: h.Capabilities, Preflight: h.Preflight}
	if cfg.ControllerGeneration > 0 {
		inventory, err := c.Journal.Inventory()
		if err != nil {
			return err
		}
		enrollment.ControllerGeneration = &cfg.ControllerGeneration
		enrollment.Inventory = &inventory
	}
	if e = c.call(ctx, "POST", "/agent/v1/enroll", enrollment, &status, ""); e != nil {
		return e
	}
	if enrollment.Inventory != nil {
		if e = c.Journal.acknowledgePreviewProofs(enrollment.Inventory.PreviewCleanups); e != nil {
			return e
		}
	}
	cfg.PreviewOnly = status.PreviewOnly != nil && *status.PreviewOnly
	cfg.Role = status.Role
	cfg.Pending = ""
	cfg.Enrolled = true
	cfg.EnrollmentToken = ""
	return c.Journal.SetConfig(cfg)
}
func (c *Client) rotate(ctx context.Context) error {
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	if cfg.Pending == "" {
		cfg.Pending, e = cryptobox.Token()
		if e != nil {
			return e
		}
		if e = c.Journal.SetConfig(cfg); e != nil {
			return e
		}
	}
	// A lost exchange response is recovered by trying the persisted candidate first.
	var status api.AgentStatus
	if e = c.call(ctx, "POST", "/agent/v1/heartbeat", c.Observe(ctx), &status, cfg.Pending); e != nil {
		if e = c.call(ctx, "POST", "/agent/v1/rotate", api.AgentRotation{Credential: cfg.Pending}, nil, cfg.Credential); e != nil {
			return e
		}
	}
	cfg.Credential = cfg.Pending
	cfg.Pending = ""
	return c.Journal.SetConfig(cfg)
}
func (c *Client) Run(ctx context.Context) error {
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	if !cfg.Enrolled && cfg.EnrollmentToken == "" {
		return errors.New("enroll this agent before running")
	}
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	var wg sync.WaitGroup
	if c.Metrics != nil {
		wg.Go(func() {
			c.loop(ctx, time.Minute, func() error {
				cfg, err := c.Journal.Config()
				if err != nil || !cfg.Enrolled {
					return err
				}
				sampleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				report := c.Metrics(sampleCtx)
				cancel()
				sendCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				return c.call(sendCtx, "POST", "/agent/v1/metrics", report, nil, cfg.Credential)
			})
		})
	}
	wg.Go(func() {
		c.loop(ctx, time.Second, func() error { return c.Engine.TickSchedules(ctx) })
	})
	wg.Go(func() {
		sent := map[string]int64{}
		c.loop(ctx, 5*time.Second, func() error {
			reports, err := c.Journal.ScheduleReports()
			if err != nil {
				return err
			}
			cfg, err := c.Journal.Config()
			if err != nil {
				return err
			}
			if !cfg.Enrolled {
				return nil
			}
			for _, report := range reports {
				key := report.Status.PolicyId.String()
				if sent[key] >= report.Sequence {
					continue
				}
				if err = c.call(ctx, "POST", "/agent/v1/schedule-reports", report, nil, cfg.Credential); err != nil {
					return err
				}
				sent[key] = report.Sequence
			}
			return nil
		})
	})
	wg.Go(func() {
		c.loop(ctx, 15*time.Second, func() error {
			cfg, e := c.Journal.Config()
			if e != nil {
				return e
			}
			if !cfg.Enrolled {
				return nil
			}
			if cfg.Pending != "" {
				return c.rotate(ctx)
			}
			var status api.AgentStatus
			if e = c.call(ctx, "POST", "/agent/v1/heartbeat", c.Observe(ctx), &status, cfg.Credential); e != nil {
				return e
			}
			if status.RotationRequested {
				return c.rotate(ctx)
			}
			return nil
		})
	})
	wg.Go(func() {
		c.loop(ctx, 2*time.Second, func() error {
			pending, e := c.Journal.Pending()
			if e != nil {
				return e
			}
			cfg, e := c.Journal.Config()
			if e != nil {
				return e
			}
			if !cfg.Enrolled {
				return nil
			}
			for _, r := range pending {
				if e = c.call(ctx, "POST", "/agent/v1/report", r.Report, nil, cfg.Credential); e != nil {
					return e
				}
				if execution.Terminal(r.Report.State) {
					if e = c.Journal.Acknowledge(r.Command.Id, r.Report.Sequence); e != nil {
						return e
					}
				}
			}
			return nil
		})
	})
	wg.Go(func() {
		var retry <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-retry:
			}
			retry = nil
			pending, e := c.Journal.Pending()
			if e != nil {
				retry = time.After(time.Second)
				continue
			}
			for _, r := range pending {
				if r.Report.State == "running" {
					if err := c.Engine.Execute(ctx, r.Command); err != nil && ctx.Err() == nil {
						retry = time.After(time.Second)
					}
				}
			}
		}
	})
	delay := time.Second
	for ctx.Err() == nil {
		cfg, e := c.Journal.Config()
		if e != nil {
			break
		}
		if !cfg.Enrolled {
			e = c.Enroll(ctx, "")
			if e != nil {
				if !wait(ctx, delay) {
					break
				}
				delay = min(delay*2, 30*time.Second)
				continue
			}
			cfg, e = c.Journal.Config()
			if e != nil {
				break
			}
		}
		var poll api.AgentPoll
		e = c.call(ctx, "GET", "/agent/v1/poll", nil, &poll, cfg.Credential)
		if e == nil && poll.Command != nil {
			_, e = c.Journal.Accept(*poll.Command)
			if e == nil {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
		if e != nil {
			if !wait(ctx, delay+time.Duration(rand.IntN(500))*time.Millisecond) {
				break
			}
			delay = min(delay*2, 30*time.Second)
		} else {
			delay = time.Second
			if !wait(ctx, 250*time.Millisecond) {
				break
			}
		}
	}
	wg.Wait()
	return ctx.Err()
}
func (c *Client) loop(ctx context.Context, interval time.Duration, fn func() error) {
	for ctx.Err() == nil {
		_ = fn()
		if !wait(ctx, interval) {
			return
		}
	}
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
