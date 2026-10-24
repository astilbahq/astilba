package hosting

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const StripeVersion = "2026-09-30.endive"

var ErrProvider = errors.New("billing provider unavailable")

// Stripe uses only server-owned identifiers. HTTP is injectable for contract
// tests; production always uses the fixed HTTPS origin and refuses redirects.
type Stripe struct {
	Key, Price string
	HTTP       *http.Client
}

func (s Stripe) request(ctx context.Context, method, path, key string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.stripe.com/v1/"+path, body)
	if err != nil {
		return ErrProvider
	}
	req.SetBasicAuth(s.Key, "")
	req.Header.Set("Stripe-Version", StripeVersion)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if s.HTTP != nil {
		copy := *s.HTTP
		client = &copy
		client.Timeout = 12 * time.Second
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	res, err := client.Do(req)
	if err != nil {
		return ErrProvider
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 262145))
	if err != nil || len(raw) > 262144 || res.StatusCode < 200 || res.StatusCode >= 300 || json.Unmarshal(raw, out) != nil {
		return ErrProvider
	}
	return nil
}
func validID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) > 255 || len(id) <= len(prefix) {
		return false
	}
	for _, r := range id[len(prefix):] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func (s Stripe) Customer(ctx context.Context, intent string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := s.request(ctx, "POST", "customers", intent+":customer", url.Values{"metadata[intent]": {intent}}, &out)
	if err != nil || !validID(out.ID, "cus_") {
		return "", ErrProvider
	}
	return out.ID, nil
}

type Checkout struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Customer     string `json:"customer"`
	Subscription string `json:"subscription"`
	Status       string `json:"status"`
	Expires      int64  `json:"expires_at"`
	Reference    string `json:"client_reference_id"`
}

func safeURL(raw, host string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host == host && u.User == nil && u.Fragment == ""
}
func (s Stripe) Checkout(ctx context.Context, intent, customer, returnURL string, expires time.Time) (Checkout, error) {
	var out Checkout
	form := url.Values{"mode": {"subscription"}, "customer": {customer}, "line_items[0][price]": {s.Price}, "line_items[0][quantity]": {"1"}, "success_url": {returnURL + "&billing=returned"}, "cancel_url": {returnURL + "&billing=cancelled"}, "client_reference_id": {intent}, "expires_at": {strconv.FormatInt(expires.Unix(), 10)}}
	err := s.request(ctx, "POST", "checkout/sessions", intent+":checkout", form, &out)
	if err != nil || !validID(out.ID, "cs_") || !safeURL(out.URL, "checkout.stripe.com") || out.Customer != customer || out.Expires != expires.Unix() {
		return out, ErrProvider
	}
	return out, nil
}
func (s Stripe) GetCheckout(ctx context.Context, id string) (Checkout, error) {
	var out Checkout
	if !validID(id, "cs_") {
		return out, ErrProvider
	}
	err := s.request(ctx, "GET", "checkout/sessions/"+id, "", nil, &out)
	if err != nil || out.ID != id {
		return out, ErrProvider
	}
	return out, nil
}
func (s Stripe) Portal(ctx context.Context, customer, returnURL string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if !validID(customer, "cus_") {
		return "", ErrProvider
	}
	err := s.request(ctx, "POST", "billing_portal/sessions", "", url.Values{"customer": {customer}, "return_url": {returnURL}}, &out)
	if err != nil || !safeURL(out.URL, "billing.stripe.com") {
		return "", ErrProvider
	}
	return out.URL, nil
}

type Subscription struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Status   string `json:"status"`
	EndedAt  *int64 `json:"ended_at"`
	Items    struct {
		Data []struct {
			Quantity int   `json:"quantity"`
			End      int64 `json:"current_period_end"`
			Price    struct {
				ID        string `json:"id"`
				Recurring *struct {
					Interval string `json:"interval"`
					Count    int    `json:"interval_count"`
				} `json:"recurring"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

func (s Stripe) Subscription(ctx context.Context, id, customer string) (Subscription, error) {
	var out Subscription
	if !validID(id, "sub_") {
		return out, ErrProvider
	}
	err := s.request(ctx, "GET", "subscriptions/"+id, "", nil, &out)
	if err != nil || out.ID != id || out.Customer != customer || len(out.Items.Data) != 1 {
		return out, ErrProvider
	}
	item := out.Items.Data[0]
	if item.Quantity != 1 || item.Price.ID != s.Price || item.Price.Recurring == nil || item.Price.Recurring.Interval != "month" || item.Price.Recurring.Count != 1 || item.End <= 0 {
		return out, ErrProvider
	}
	return out, nil
}
func (s Subscription) Entitlement() Entitlement {
	end := time.Unix(s.Items.Data[0].End, 0)
	if s.EndedAt != nil && time.Unix(*s.EndedAt, 0).Before(end) {
		end = time.Unix(*s.EndedAt, 0)
	}
	// Delinquent subscriptions retain only their already paid period. Expiration
	// is never moved to the webhook delivery time.
	return Entitlement{Status: s.Status, Until: &end}
}

type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

func VerifyEvent(raw []byte, header, secret string, now time.Time) (Event, error) {
	var out Event
	if secret == "" || len(raw) > 262144 || len(header) > 4096 {
		return out, ErrProvider
	}
	var stamp string
	signatures := []string{}
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if k == "t" {
			if stamp != "" {
				return out, ErrProvider
			}
			stamp = v
		}
		if k == "v1" {
			signatures = append(signatures, v)
		}
	}
	n, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || now.Unix()-n > 300 || n-now.Unix() > 300 {
		return out, ErrProvider
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s.", stamp)
	mac.Write(raw)
	verified := false
	for _, sig := range signatures {
		b, e := hex.DecodeString(sig)
		if e == nil && hmac.Equal(b, mac.Sum(nil)) {
			verified = true
		}
	}
	if !verified || json.NewDecoder(bytes.NewReader(raw)).Decode(&out) != nil || !validID(out.ID, "evt_") {
		return out, ErrProvider
	}
	return out, nil
}

// FindCheckout reconciles a lost create response before replacing an expired
// intent. A dedicated workspace customer cannot hide sessions from this list.
func (s Stripe) FindCheckout(ctx context.Context, customer, reference string) (*Checkout, error) {
	if !validID(customer, "cus_") {
		return nil, ErrProvider
	}
	after := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"customer": {customer}, "limit": {"100"}}
		if after != "" {
			q.Set("starting_after", after)
		}
		var out struct {
			Data []Checkout `json:"data"`
			More bool       `json:"has_more"`
		}
		if e := s.request(ctx, "GET", "checkout/sessions?"+q.Encode(), "", nil, &out); e != nil {
			return nil, e
		}
		for _, v := range out.Data {
			if v.Reference == reference {
				return &v, nil
			}
		}
		if !out.More {
			return nil, nil
		}
		if len(out.Data) == 0 {
			return nil, ErrProvider
		}
		after = out.Data[len(out.Data)-1].ID
	}
	return nil, ErrProvider
}
