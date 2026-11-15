package applicationdns

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type Cloudflare struct {
	Zone, Token string
	HTTP        *http.Client
	pace        pace
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

// ErrUnanswered is ErrProvider for a request that got no answer, such as one cut off by a timeout
// or the caller's deadline. The provider may still act on it later.
var ErrUnanswered error = unanswered{}

type unanswered struct{}

func (unanswered) Error() string        { return ErrProvider.Error() }
func (unanswered) Is(target error) bool { return target == ErrProvider }

// Every request the client sends shares one pace, so the zone sweep and DNS jobs together stay
// under Cloudflare's limit of 1,200 requests per five minutes. A 429 holds every request until its
// Retry-After has passed.
const (
	paceRate        = 3
	paceBurst       = 20
	retryAfterUnset = time.Minute
	retryAfterMost  = 5 * time.Minute
)

type pace struct {
	mu        sync.Mutex
	limiter   *rate.Limiter
	notBefore time.Time
}

// wait returns once a request may be sent, or false when the context ends first. Nothing has been
// sent then, so the provider has nothing to act on later.
func (p *pace) wait(ctx context.Context) bool {
	p.mu.Lock()
	if p.limiter == nil {
		p.limiter = rate.NewLimiter(paceRate, paceBurst)
	}
	limiter, until := p.limiter, time.Until(p.notBefore)
	p.mu.Unlock()
	if until > 0 {
		timer := time.NewTimer(until)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
		}
	}
	return limiter.Wait(ctx) == nil
}

// hold keeps every request back for as long as a 429's Retry-After asks, in seconds or as a date,
// within retryAfterMost.
func (p *pace) hold(retryAfter string) {
	wait := retryAfterUnset
	if seconds, e := strconv.Atoi(retryAfter); e == nil && seconds >= 0 {
		wait = time.Duration(seconds) * time.Second
	} else if at, e := http.ParseTime(retryAfter); e == nil {
		wait = time.Until(at)
	}
	wait = min(max(wait, 0), retryAfterMost)
	p.mu.Lock()
	defer p.mu.Unlock()
	if until := time.Now().Add(wait); until.After(p.notBefore) {
		p.notBefore = until
	}
}

type envelope struct {
	Success bool           `json:"success"`
	Result  jsontext.Value `json:"result"`
	Info    struct {
		Page       int `json:"page"`
		Total      int `json:"total_count"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// send makes one request and returns its envelope, refusing an answer larger than limit bytes.
func (c *Cloudflare) send(ctx context.Context, method, path string, in any, limit int64) (envelope, error) {
	var out envelope
	var body io.Reader
	if in != nil {
		raw, e := json.Marshal(in)
		if e != nil {
			return out, e
		}
		body = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4/zones/"+c.Zone+"/dns_records"+path, body)
	if e != nil {
		return out, ErrProvider
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if !c.pace.wait(ctx) {
		return out, ErrProvider
	}
	response, e := client.Do(req)
	if e != nil {
		return out, ErrUnanswered
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		c.pace.hold(response.Header.Get("Retry-After"))
		return out, ErrProvider
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if e != nil || int64(len(raw)) > limit || response.StatusCode < 200 || response.StatusCode >= 300 {
		return out, ErrProvider
	}
	if json.Unmarshal(raw, &out) != nil || !out.Success {
		return out, ErrProvider
	}
	return out, nil
}

func (c *Cloudflare) request(ctx context.Context, method, path string, in, out any) error {
	envelope, e := c.send(ctx, method, path, in, 65536)
	if e != nil {
		return e
	}
	if envelope.Info.Total > 100 {
		return ErrProvider
	}
	if out != nil && json.Unmarshal(envelope.Result, out) != nil {
		return ErrProvider
	}
	return nil
}

// Removed is a provider record Astilba is about to delete. Deleting is told about each one
// before its deletion is sent, and an error from it stops the deletion.
type Removed struct{ ID, Type, Content string }
type Deleting func(Removed) error

// list returns the records at exactly name, refusing a response that names anything else.
func (c *Cloudflare) list(ctx context.Context, name string) ([]record, error) {
	var records []record
	if e := c.request(ctx, "GET", "?name="+url.QueryEscape(name)+"&per_page=100", nil, &records); e != nil {
		return nil, e
	}
	for _, r := range records {
		if r.Name != name || !cloudflareID.MatchString(r.ID) {
			return nil, ErrProvider
		}
	}
	return records, nil
}

// Reconcile only changes exact-name A/AAAA records bearing this installation's
// marker. Listing first also recovers a successful mutation whose response was lost.
func (c *Cloudflare) Reconcile(ctx context.Context, name, marker, ipv4, ipv6 string, deleting Deleting) (map[string]string, error) {
	records, e := c.list(ctx, name)
	if e != nil {
		return nil, e
	}
	existing := map[string]record{}
	for _, r := range records {
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
				if e := c.delete(ctx, old, deleting); e != nil {
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

func (c *Cloudflare) delete(ctx context.Context, r record, deleting Deleting) error {
	if e := deleting(Removed{r.ID, r.Type, r.Content}); e != nil {
		return e
	}
	return c.request(ctx, "DELETE", "/"+r.ID, nil, nil)
}

// Remove deletes the exact-name A/AAAA records that carry this installation's marker
// and leaves every other record at that name alone. It never matches by name alone:
// a record someone else created, or another installation's, survives. Run again after
// a failure, it lists afresh, so a deletion whose response was lost is not repeated.
// It returns nil only once none of the installation's records are left at the name.
func (c *Cloudflare) Remove(ctx context.Context, name, marker string, deleting Deleting) error {
	_, e := c.remove(ctx, name, marker, func(record) bool { return true }, deleting)
	return e
}

// RemoveListed deletes the records ids names that are still at name, as A/AAAA records carrying
// this installation's marker. Any other record there survives, ours included, so a record
// published since the caller looked is never deleted on the strength of an older listing. It lists
// name afresh first, so a deletion whose response was lost is not repeated, and returns the IDs of
// our address records still at name.
func (c *Cloudflare) RemoveListed(ctx context.Context, name, marker string, ids []string, deleting Deleting) ([]string, error) {
	listed := map[string]bool{}
	for _, id := range ids {
		listed[id] = true
	}
	return c.remove(ctx, name, marker, func(r record) bool { return listed[r.ID] }, deleting)
}

// remove deletes our address records at name that chosen picks, and returns the IDs of the ones
// it left.
func (c *Cloudflare) remove(ctx context.Context, name, marker string, chosen func(record) bool, deleting Deleting) ([]string, error) {
	records, e := c.list(ctx, name)
	if e != nil {
		return nil, e
	}
	left := []string{}
	for _, r := range records {
		if r.Type != "A" && r.Type != "AAAA" || r.Comment != marker {
			continue
		}
		if !chosen(r) {
			left = append(left, r.ID)
			continue
		}
		if e := c.delete(ctx, r, deleting); e != nil {
			return nil, e
		}
	}
	return left, nil
}

// Marked is a provider record whose comment starts with the prefix the zone was listed for.
type Marked struct{ ID, Type, Name, Content, Comment string }

// The zone listing reads pages of markedPage records, at most markedPages of them, each answer at
// most markedBytes.
const (
	markedPage  = 100
	markedPages = 200
	markedBytes = 1 << 20
)

// ListMarked returns every record in the zone whose comment starts with prefix, reading the
// provider's pages in turn, and whether it read them all. A zone with more than markedPages pages
// is read only that far. Pages can shift while records change, so a record can be missed or seen
// twice; it is returned once, and one missed is found by the next listing. An answer naming a
// record whose comment doesn't start with prefix, exactly, refuses the whole listing: the
// provider didn't apply the filter, so nothing in it can be taken to be ours.
func (c *Cloudflare) ListMarked(ctx context.Context, prefix string) ([]Marked, bool, error) {
	seen := map[string]bool{}
	out := []Marked{}
	for page := 1; ; page++ {
		query := url.Values{"comment.startswith": {prefix}, "per_page": {strconv.Itoa(markedPage)}, "page": {strconv.Itoa(page)}}
		envelope, e := c.send(ctx, "GET", "?"+query.Encode(), nil, markedBytes)
		if e != nil {
			return nil, false, e
		}
		var records []record
		if json.Unmarshal(envelope.Result, &records) != nil || envelope.Info.Page != page || len(records) > markedPage {
			return nil, false, ErrProvider
		}
		for _, r := range records {
			if !cloudflareID.MatchString(r.ID) || !strings.HasPrefix(r.Comment, prefix) {
				return nil, false, ErrProvider
			}
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, Marked{r.ID, r.Type, r.Name, r.Content, r.Comment})
			}
		}
		if len(records) == 0 || page >= envelope.Info.TotalPages {
			return out, true, nil
		}
		if page == markedPages {
			return out, false, nil
		}
	}
}
