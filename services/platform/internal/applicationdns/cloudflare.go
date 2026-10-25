package applicationdns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Cloudflare struct {
	Zone, Token string
	HTTP        *http.Client
}
type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Comment string `json:"comment"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

var ErrProvider = errors.New("DNS provider request failed; inspect the operator configuration and retry")
var ErrOwnership = errors.New("DNS contains a record not owned by this installation; inspect it without overwriting it")

func (c *Cloudflare) request(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4/zones/"+c.Zone+"/dns_records"+path, body)
	if e != nil {
		return ErrProvider
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, e := client.Do(req)
	if e != nil {
		return ErrProvider
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrProvider
	}
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Info    struct {
			Total int `json:"total_count"`
		} `json:"result_info"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !envelope.Success || envelope.Info.Total > 100 {
		return ErrProvider
	}
	if out != nil && json.Unmarshal(envelope.Result, out) != nil {
		return ErrProvider
	}
	return nil
}

// Reconcile only changes exact-name A/AAAA records bearing this installation's
// marker. Listing first also recovers a successful mutation whose response was lost.
func (c *Cloudflare) Reconcile(ctx context.Context, name, marker, ipv4, ipv6 string) (map[string]string, error) {
	var records []record
	if e := c.request(ctx, "GET", "?name="+url.QueryEscape(name)+"&per_page=100", nil, &records); e != nil {
		return nil, e
	}
	existing := map[string]record{}
	for _, r := range records {
		if r.Name != name || !cloudflareID.MatchString(r.ID) {
			return nil, ErrProvider
		}
		if r.Type == "CNAME" || r.Type == "NS" {
			return nil, ErrOwnership
		}
		if r.Type != "A" && r.Type != "AAAA" {
			continue
		}
		if r.Comment != marker || existing[r.Type].ID != "" {
			return nil, ErrOwnership
		}
		existing[r.Type] = r
	}
	out := map[string]string{}
	for _, kind := range []string{"A", "AAAA"} {
		address := ipv4
		if kind == "AAAA" {
			address = ipv6
		}
		old := existing[kind]
		if address == "" {
			if old.ID != "" {
				if e := c.request(ctx, "DELETE", "/"+old.ID, nil, nil); e != nil {
					return nil, e
				}
			}
			continue
		}
		wanted := record{Type: kind, Name: name, Content: address, Comment: marker, TTL: 300}
		if old.ID != "" && old.Content == address && !old.Proxied && old.TTL == 300 {
			out[kind] = old.ID
			continue
		}
		method, path := "POST", ""
		if old.ID != "" {
			method, path = "PUT", "/"+old.ID
		}
		var result record
		if e := c.request(ctx, method, path, wanted, &result); e != nil {
			return nil, e
		}
		if !cloudflareID.MatchString(result.ID) || result.Name != name || result.Type != kind || result.Comment != marker || result.Content != address || result.Proxied {
			return nil, ErrProvider
		}
		out[kind] = result.ID
	}
	return out, nil
}
