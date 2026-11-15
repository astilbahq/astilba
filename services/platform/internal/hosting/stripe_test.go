package hosting

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

type answerFunc func(*http.Request) (any, error)

func (f answerFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := f(r)
	if err != nil {
		return nil, err
	}
	raw, err := stablejson.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
}

const day = int64(24 * 60 * 60)

func line(subscription string, start, end int64, price string, proration bool) map[string]any {
	return map[string]any{
		"period":  map[string]any{"start": start, "end": end},
		"pricing": map[string]any{"price_details": map[string]any{"price": price}},
		"parent":  map[string]any{"type": "subscription_item_details", "subscription_item_details": map[string]any{"subscription": subscription, "subscription_item": "si_one", "proration": proration}},
	}
}

func invoice(id, customer, status, subscription string, lines ...map[string]any) map[string]any {
	return map[string]any{
		"id": id, "customer": customer, "status": status,
		"parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": subscription}},
		"lines":  map[string]any{"data": lines, "has_more": false},
	}
}

func paidThrough(t *testing.T, invoices ...map[string]any) (*time.Time, error) {
	t.Helper()
	s := Stripe{Key: "sk_test_unit", Price: "price_now", HTTP: &http.Client{Transport: answerFunc(func(r *http.Request) (any, error) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v1/invoices" || q.Get("customer") != "cus_one" || q.Get("subscription") != "sub_one" || q.Get("status") != "paid" || q.Get("limit") != "10" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return nil, errors.New("unexpected")
		}
		return map[string]any{"data": invoices, "has_more": false}, nil
	})}}
	return s.PaidThrough(context.Background(), "sub_one", "cus_one")
}

func TestPaidThroughTakesTheLatestPaidPeriod(t *testing.T) {
	const start = int64(1_800_000_000)
	got, err := paidThrough(t,
		// Newest first. The proration line adjusts a period already billed and does not count.
		invoice("in_c", "cus_one", "paid", "sub_one", line("sub_one", start+60*day, start+90*day, "price_now", true)),
		// Paid at the price configured before a price change: still a paid period.
		invoice("in_b", "cus_one", "paid", "sub_one", line("sub_one", start+30*day, start+60*day, "price_before", false)),
		invoice("in_a", "cus_one", "paid", "sub_one", line("sub_one", start, start+30*day, "price_now", false)),
	)
	if err != nil || got == nil || got.Unix() != start+60*day {
		t.Fatalf("paid through %v %v", got, err)
	}
}

func TestPaidThroughWithoutPaidInvoices(t *testing.T) {
	got, err := paidThrough(t)
	if err != nil || got != nil {
		t.Fatalf("paid through %v %v", got, err)
	}
	// A line of another subscription, or one that is not a subscription item's, is no
	// evidence for this one.
	other := line("sub_other", 1_800_000_000, 1_800_000_000+30*day, "price_now", false)
	loose := map[string]any{"period": map[string]any{"start": 1_800_000_000, "end": 1_800_000_000 + 30*day}, "parent": map[string]any{"type": "invoice_item_details"}}
	got, err = paidThrough(t, invoice("in_a", "cus_one", "paid", "sub_one", other, loose))
	if err != nil || got != nil {
		t.Fatalf("paid through %v %v", got, err)
	}
}

func TestPaidThroughRefusesWhatWasNotAskedFor(t *testing.T) {
	const start = int64(1_800_000_000)
	good := line("sub_one", start, start+30*day, "price_now", false)
	many := []map[string]any{}
	for range 11 {
		many = append(many, invoice("in_a", "cus_one", "paid", "sub_one", good))
	}
	for name, invoices := range map[string][]map[string]any{
		"unpaid":           {invoice("in_a", "cus_one", "open", "sub_one", good)},
		"other customer":   {invoice("in_a", "cus_two", "paid", "sub_one", good)},
		"other parent":     {invoice("in_a", "cus_one", "paid", "sub_two", good)},
		"no parent":        {{"id": "in_a", "customer": "cus_one", "status": "paid", "lines": map[string]any{"data": []any{good}}}},
		"bad id":           {invoice("ch_a", "cus_one", "paid", "sub_one", good)},
		"empty period":     {invoice("in_a", "cus_one", "paid", "sub_one", line("sub_one", start, start, "price_now", false))},
		"reversed period":  {invoice("in_a", "cus_one", "paid", "sub_one", line("sub_one", start, start-day, "price_now", false))},
		"overlong period":  {invoice("in_a", "cus_one", "paid", "sub_one", line("sub_one", start, start+33*day, "price_now", false))},
		"missing start":    {invoice("in_a", "cus_one", "paid", "sub_one", line("sub_one", 0, 30*day, "price_now", false))},
		"more than a page": many,
		"draft":            {invoice("in_a", "cus_one", "draft", "sub_one")},
	} {
		if got, err := paidThrough(t, invoices...); !errors.Is(err, ErrProvider) {
			t.Errorf("%s: accepted %v %v", name, got, err)
		}
	}
}

func TestPaidThroughRefusesBadIdentifiers(t *testing.T) {
	s := Stripe{Key: "sk_test_unit", HTTP: &http.Client{Transport: answerFunc(func(r *http.Request) (any, error) {
		t.Errorf("asked Stripe %s", r.URL)
		return nil, errors.New("unexpected")
	})}}
	for _, ids := range [][2]string{{"sub_a/b", "cus_one"}, {"sub_one", "cus_x&y=1"}, {"", "cus_one"}} {
		if _, err := s.PaidThrough(context.Background(), ids[0], ids[1]); !errors.Is(err, ErrProvider) {
			t.Errorf("%v accepted", ids)
		}
	}
}
