package applicationdns

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

type redirect struct{ to *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.cloudflare.com" {
		return nil, errors.New("unexpected provider origin")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.to.Scheme, r.to.Host
	clone.URL = &u
	return http.DefaultTransport.RoundTrip(clone)
}

// zone is a provider fixture. failDelete answers that many deletions with an error, after
// carrying out the deletion when lose is set.
type zone struct {
	mu         sync.Mutex
	records    map[string]record
	failDelete int
	lose       bool
	deletes    []string
	listed     []record // answered to every listing when set
	// limited answers that many requests with a 429 and retryAfter.
	limited    int
	retryAfter string
	requests   []time.Time
}

func (z *zone) serve(w http.ResponseWriter, r *http.Request) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.requests = append(z.requests, time.Now())
	if z.limited > 0 {
		z.limited--
		w.Header().Set("Retry-After", z.retryAfter)
		w.WriteHeader(429)
		return
	}
	var result any = map[string]any{}
	info := map[string]any{}
	switch r.Method {
	case "GET":
		query := r.URL.Query()
		list := []record{}
		for _, v := range z.records {
			if prefix := query.Get("comment.startswith"); prefix != "" && strings.HasPrefix(strings.ToLower(v.Comment), strings.ToLower(prefix)) || prefix == "" && v.Name == query.Get("name") {
				list = append(list, v)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		if query.Has("page") {
			page, _ := strconv.Atoi(query.Get("page"))
			size, _ := strconv.Atoi(query.Get("per_page"))
			info = map[string]any{"page": page, "per_page": size, "total_count": len(list), "total_pages": (len(list) + size - 1) / size}
			list = list[min((page-1)*size, len(list)):min(page*size, len(list))]
		}
		result = list
		if z.listed != nil {
			result = z.listed
		}
	case "DELETE":
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		z.deletes = append(z.deletes, id)
		if z.failDelete > 0 {
			z.failDelete--
			if z.lose {
				delete(z.records, id)
			}
			w.WriteHeader(502)
			return
		}
		if _, ok := z.records[id]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(z.records, id)
	default:
		w.WriteHeader(405)
		return
	}
	_ = stablejson.Encode(w, map[string]any{"success": true, "result": result, "result_info": info})
}

func fixture(t *testing.T, records ...record) (*zone, *Cloudflare) {
	z := &zone{records: map[string]record{}}
	for _, r := range records {
		z.records[r.ID] = r
	}
	server := httptest.NewServer(http.HandlerFunc(z.serve))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	return z, &Cloudflare{Zone: strings.Repeat("a", 32), Token: "fixture", HTTP: &http.Client{Transport: redirect{u}, Timeout: time.Second}}
}

func id(c byte) string { return strings.Repeat(string(c), 32) }

const (
	name   = "*.s1.apps.example.test"
	marker = "astilba:installation:server"
)

func TestRemoveDeletesOnlyMarkedAddressRecords(t *testing.T) {
	z, c := fixture(t,
		record{ID: id('1'), Type: "A", Name: name, Content: "198.51.100.1", Comment: marker},
		record{ID: id('2'), Type: "AAAA", Name: name, Content: "2001:db8::1", Comment: marker},
		record{ID: id('3'), Type: "A", Name: name, Content: "198.51.100.2", Comment: "someone else"},
		record{ID: id('4'), Type: "A", Name: name, Content: "198.51.100.3", Comment: "astilba:another-installation:server"},
		record{ID: id('5'), Type: "TXT", Name: name, Content: "verification", Comment: marker},
		record{ID: id('6'), Type: "A", Name: "*.s2.apps.example.test", Content: "198.51.100.4", Comment: marker},
	)
	var told []Removed
	if e := c.Remove(context.Background(), name, marker, func(r Removed) error { told = append(told, r); return nil }); e != nil {
		t.Fatal(e)
	}
	if len(told) != 2 || len(z.records) != 4 {
		t.Fatalf("removed %v, left %v", told, z.records)
	}
	for _, kept := range []string{id('3'), id('4'), id('5'), id('6')} {
		if _, ok := z.records[kept]; !ok {
			t.Fatal("a record Astilba doesn't own was deleted", kept)
		}
	}
}

func TestRemoveRetriesAfterAFailedOrLostDeletion(t *testing.T) {
	for _, lose := range []bool{false, true} {
		z, c := fixture(t, record{ID: id('1'), Type: "A", Name: name, Content: "198.51.100.1", Comment: marker})
		z.failDelete, z.lose = 1, lose
		var told int
		deleting := func(Removed) error { told++; return nil }
		if e := c.Remove(context.Background(), name, marker, deleting); !errors.Is(e, ErrProvider) {
			t.Fatal("a failed deletion was reported as done", e)
		}
		if e := c.Remove(context.Background(), name, marker, deleting); e != nil {
			t.Fatal(e)
		}
		// A lost answer is not deleted again: the retry lists the record as gone.
		if len(z.records) != 0 || told != 2-boolCount(lose) || len(z.deletes) != 2-boolCount(lose) {
			t.Fatalf("lose=%v: records %v, told %d, deletes %v", lose, z.records, told, z.deletes)
		}
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestRemoveRefusesAnswersForOtherNamesAndStopsWhenNotAudited(t *testing.T) {
	z, c := fixture(t)
	z.listed = []record{{ID: id('1'), Type: "A", Name: "*.s2.apps.example.test", Content: "198.51.100.1", Comment: marker}}
	if e := c.Remove(context.Background(), name, marker, func(Removed) error { return nil }); !errors.Is(e, ErrProvider) || len(z.deletes) != 0 {
		t.Fatal("acted on a listing for another name", e, z.deletes)
	}
	z.listed = nil
	z.records[id('2')] = record{ID: id('2'), Type: "A", Name: name, Content: "198.51.100.1", Comment: marker}
	refused := errors.New("audit unavailable")
	if e := c.Remove(context.Background(), name, marker, func(Removed) error { return refused }); !errors.Is(e, refused) || len(z.deletes) != 0 {
		t.Fatal("deleted a record the audit did not record", e, z.deletes)
	}
}

func TestListMarkedReadsEveryPageAndOnlyOurPrefix(t *testing.T) {
	prefix := "astilba:installation:"
	var records []record
	for i := range 250 {
		records = append(records, record{ID: strings.Repeat("0", 28) + strconv.FormatInt(int64(0x1000+i), 16), Type: "A", Name: "*.s" + strconv.Itoa(i) + ".apps.example.test", Content: "198.51.100.1", Comment: prefix + "server"})
	}
	records = append(records, record{ID: id('f'), Type: "A", Name: name, Content: "198.51.100.2", Comment: "astilba:another-installation:server"})
	// The provider matches the prefix ignoring case, as its documentation says, so a comment that
	// matches only so is returned rather than refusing the listing.
	records = append(records, record{ID: id('e'), Type: "A", Name: name, Content: "198.51.100.3", Comment: "ASTILBA:INSTALLATION:server"})
	_, c := fixture(t, records...)
	got, e := c.ListMarked(context.Background(), prefix)
	if e != nil || len(got) != 251 {
		t.Fatalf("listed %d records: %v", len(got), e)
	}
	for _, r := range got {
		if !strings.HasPrefix(strings.ToLower(r.Comment), prefix) {
			t.Fatal("listed another installation's record", r)
		}
	}
}

func TestListMarkedRefusesAListingNamingAnotherInstallation(t *testing.T) {
	z, c := fixture(t)
	z.listed = []record{
		{ID: id('1'), Type: "A", Name: name, Content: "198.51.100.1", Comment: "astilba:installation:server"},
		{ID: id('2'), Type: "A", Name: name, Content: "198.51.100.2", Comment: "astilba:installation-other:server"},
	}
	// The second comment starts with the first's installation ID only as text, without its colon.
	if _, e := c.ListMarked(context.Background(), "astilba:installation:"); !errors.Is(e, ErrProvider) {
		t.Fatal("trusted a listing the filter was not applied to", e)
	}
}

func TestRemoveListedLeavesRecordsItWasNotGiven(t *testing.T) {
	z, c := fixture(t,
		record{ID: id('1'), Type: "A", Name: name, Content: "198.51.100.1", Comment: marker},
		record{ID: id('2'), Type: "AAAA", Name: name, Content: "2001:db8::1", Comment: marker},
		record{ID: id('3'), Type: "A", Name: name, Content: "198.51.100.2", Comment: "someone else"},
	)
	left, e := c.RemoveListed(context.Background(), name, marker, []string{id('1'), id('3'), id('9')}, func(Removed) error { return nil })
	if e != nil || len(left) != 1 || left[0] != id('2') {
		t.Fatal("left", left, e)
	}
	if _, ok := z.records[id('1')]; ok || len(z.records) != 2 || len(z.deletes) != 1 {
		t.Fatalf("records %v, deletes %v", z.records, z.deletes)
	}
}

func TestTooManyRequestsHoldsEveryLaterRequest(t *testing.T) {
	z, c := fixture(t, record{ID: id('1'), Type: "A", Name: name, Content: "198.51.100.1", Comment: marker})
	z.limited, z.retryAfter = 1, "1"
	if e := c.Remove(context.Background(), name, marker, func(Removed) error { return nil }); !errors.Is(e, ErrProvider) || errors.Is(e, ErrUnanswered) {
		t.Fatal("a 429 was not reported as a request the provider refused", e)
	}
	if _, e := c.ListMarked(context.Background(), "astilba:installation:"); e != nil {
		t.Fatal(e)
	}
	if waited := z.requests[1].Sub(z.requests[0]); waited < 900*time.Millisecond {
		t.Fatal("the next request did not wait for Retry-After", waited)
	}
	// A request whose context ends while held is never sent.
	z.limited, z.retryAfter = 1, "60"
	_, _ = c.ListMarked(context.Background(), "astilba:installation:")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sent := len(z.requests)
	if _, e := c.ListMarked(ctx, "astilba:installation:"); !errors.Is(e, ErrProvider) || errors.Is(e, ErrUnanswered) || len(z.requests) != sent {
		t.Fatal("a held request was sent or reported as unanswered", e)
	}
}

func TestListMarkedRefusesAZoneTooLargeToReadAfterOnePage(t *testing.T) {
	prefix := "astilba:installation:"
	records := make([]record, 0, markedPages*markedPage+1)
	for i := range markedPages*markedPage + 1 {
		records = append(records, record{ID: strings.Repeat("0", 24) + strconv.FormatInt(int64(0x10000000+i), 16), Type: "A", Name: name, Content: "198.51.100.1", Comment: prefix + "server"})
	}
	z, c := fixture(t, records...)
	if _, e := c.ListMarked(context.Background(), prefix); !errors.Is(e, ErrZoneTooLarge) || len(z.requests) != 1 {
		t.Fatal("read a zone too large to compare", e, len(z.requests))
	}
}
