package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

const wrongPassword = "wrong wrong wrong wrong"

// availability gives a test clients at chosen addresses, over the real HTTP route.
type availability struct {
	t       *testing.T
	f       *fixture
	mu      sync.Mutex
	proxies map[string]*httptest.Server
}

func newAvailability(t *testing.T, f *fixture) *availability {
	f.apiServer.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	return &availability{t: t, f: f, proxies: map[string]*httptest.Server{}}
}

func (a *availability) proxy(address string) *httptest.Server {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, found := a.proxies[address]; !found {
		a.proxies[address] = proxyClient(a.t, a.f, address)
	}
	return a.proxies[address]
}

// post sends a request from address with the browser's cookies, and returns the status, or 0
// if it did not reach the service. It runs on flood goroutines too, so it cannot end the test.
func (a *availability) post(b *browser, address, path string, body any) int {
	a.t.Helper()
	raw, err := stablejson.Marshal(body)
	if err != nil {
		a.t.Error(err)
		return 0
	}
	request, err := newRequest(b, "POST", path, raw)
	if err != nil {
		a.t.Error(err)
		return 0
	}
	target, _ := url.Parse(a.proxy(address).URL)
	request.URL.Scheme, request.URL.Host = target.Scheme, target.Host
	response, err := b.client.Do(request)
	if err != nil {
		a.t.Error(err)
		return 0
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func (a *availability) login(b *browser, address, email, password string) int {
	a.t.Helper()
	return a.post(b, address, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
}

func (a *availability) wantLogin(b *browser, address, email, password string, want int, why string) {
	a.t.Helper()
	if got := a.login(b, address, email, password); got != want {
		a.t.Fatalf("%s: login from %s answered %d, want %d", why, address, got, want)
	}
}

func signOut(b *browser) {
	b.t.Helper()
	b.csrf = b.session().CsrfToken
	b.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	b.csrf = b.session().CsrfToken
}

// signedIn returns a browser that holds the owner's device cookie, then signs it out again.
func (a *availability) signedIn(address string) *browser {
	a.t.Helper()
	b := a.f.browser(a.t)
	a.wantLogin(b, address, "owner@example.test", a.f.password, 200, "first sign-in")
	signOut(b)
	return b
}

func seedOtherAccount(t *testing.T, f *fixture, email string) {
	t.Helper()
	ctx := context.Background()
	hash, err := f.auth.Passwords.Hash(f.password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: email, Name: "Other", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
}

// spendAccount spends the owner's whole account budget from two addresses, as a guessing
// attacker who knows the email would.
func (a *availability) spendAccount(firstAddress int) {
	a.t.Helper()
	guesser := a.f.browser(a.t)
	for i := range 10 {
		a.wantLogin(guesser, fmt.Sprintf("203.0.113.%d", firstAddress+i/5), "owner@example.test", wrongPassword, 400, "guess within budget")
	}
}

func TestWrongGuessesFromOneSourceDoNotRefuseAnother(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	guesser, owner := f.browser(t), f.browser(t)
	for range 5 {
		a.wantLogin(guesser, "203.0.113.1", "owner@example.test", wrongPassword, 400, "guess within budget")
	}
	a.wantLogin(guesser, "203.0.113.1", "owner@example.test", wrongPassword, 429, "guessing source over its budget")
	a.wantLogin(guesser, "203.0.113.1", "owner@example.test", f.password, 429, "guessing source keeps paying for its guesses")
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", f.password, 200, "another source with the correct password")
}

func TestAccountBudgetBoundsGuessingAcrossSources(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	a.spendAccount(1)
	stranger := f.browser(t)
	a.wantLogin(stranger, "198.51.100.1", "owner@example.test", wrongPassword, 429, "another guessing address")
	a.wantLogin(stranger, "198.51.100.2", "owner@example.test", f.password, 429, "no device cookie while the account budget is spent")
	if got := attemptCount(t, f, "login:account:owner@example.test"); got != 10 {
		t.Fatalf("guesses reached the account budget %d times, want 10", got)
	}
}

func TestDeviceCookieSignsInThroughSpentAccountBudget(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	owner := a.signedIn("198.51.100.1")
	other := a.signedIn("198.51.100.2")
	a.spendAccount(1)
	a.wantLogin(owner, "198.51.100.3", "owner@example.test", f.password, 200, "known browser at a new address")
	signOut(owner)

	// The cookie moves its holder to its own budget. It still has to find the password.
	for range 5 {
		a.wantLogin(owner, "198.51.100.3", "owner@example.test", wrongPassword, 400, "guess with a device cookie")
	}
	a.wantLogin(owner, "198.51.100.3", "owner@example.test", wrongPassword, 429, "guesses with a device cookie are limited")
	a.wantLogin(owner, "198.51.100.3", "owner@example.test", f.password, 429, "a spent cookie stays spent")
	a.wantLogin(other, "198.51.100.3", "owner@example.test", f.password, 200, "another browser has its own budget")
}

func deviceCookie(t *testing.T, f *fixture, b *browser) *http.Cookie {
	t.Helper()
	login, _ := url.Parse(f.server.URL + "/api/v1/auth/login")
	for _, cookie := range b.client.Jar.Cookies(login) {
		if cookie.Name == "astilba_device" {
			return cookie
		}
	}
	return nil
}

func TestDeviceCookieIsLimitedToItsPurpose(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	device := deviceCookie(t, f, b)
	if device == nil {
		t.Fatal("completed sign-in issued no device cookie")
	}
	session, _ := url.Parse(f.server.URL + "/api/v1/session")
	for _, cookie := range b.client.Jar.Cookies(session) {
		if cookie.Name == device.Name {
			t.Fatal("device cookie is sent beyond the login path")
		}
	}

	// A device cookie for one account gives nothing on another.
	seedOtherAccount(t, f, "other@example.test")
	seedAttempts(t, f, "login:account:other@example.test", 10)
	stolen := f.browser(t)
	login, _ := url.Parse(f.server.URL + "/api/v1/auth/login")
	stolen.client.Jar.SetCookies(login, []*http.Cookie{{Name: device.Name, Value: device.Value, Path: "/api/v1/auth/login"}})
	a.wantLogin(stolen, "198.51.100.5", "other@example.test", f.password, 429, "another account's token")

	// A password change ends the account's tokens, like its sessions.
	holder := a.signedIn("198.51.100.1")
	b.csrf = b.session().CsrfToken
	const replacement = "replacement password for this test"
	b.call("POST", "/api/v1/auth/password", map[string]any{"password": replacement, "current": map[string]string{"password": f.password}}, 200, nil)
	a.spendAccount(1)
	a.wantLogin(holder, "198.51.100.1", "owner@example.test", replacement, 429, "token from before the password change")
}

func TestPlantedCookieDoesNotShadowDeviceToken(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	token := deviceCookie(t, f, a.signedIn("198.51.100.1")).Value
	a.spendAccount(1)
	b := f.browser(t)
	raw, err := stablejson.Marshal(map[string]string{"email": "owner@example.test", "password": f.password})
	if err != nil {
		t.Fatal(err)
	}
	request, err := newRequest(b, "POST", "/api/v1/auth/login", raw)
	if err != nil {
		t.Fatal(err)
	}
	// Browsers list an older cookie of the same name and path first.
	request.AddCookie(&http.Cookie{Name: "astilba_device", Value: "planted-by-another-host"})
	request.AddCookie(&http.Cookie{Name: "astilba_device", Value: token})
	response, err := b.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("genuine token behind a planted cookie answered %d", response.StatusCode)
	}
}

func TestKnownBrowserLeavesGuessersBudgetSpent(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	owner := a.signedIn("198.51.100.1")
	a.spendAccount(1)
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", f.password, 200, "known browser")
	a.wantLogin(f.browser(t), "203.0.113.9", "owner@example.test", wrongPassword, 429, "a new address after the owner signed in")
}

func TestDeviceTokensHaveACapAcrossTheAccount(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	owner := a.signedIn("198.51.100.1")
	account, err := f.st.Queries.AccountByID(context.Background(), f.owner)
	if err != nil {
		t.Fatal(err)
	}
	seedAttempts(t, f, fmt.Sprintf("login:device-account:%s:%d", f.owner, account.AuthVersion), 20)
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", f.password, 429, "tokens over their shared cap")

	// A reset starts a new cap, so whoever spent this one cannot keep the owner out.
	if status := a.post(owner, "198.51.100.1", "/api/v1/auth/reset/request", map[string]string{"email": "owner@example.test"}); status != 202 {
		t.Fatal("reset request answered", status)
	}
	token := tokenFromLinkMail(t, f, "/reset")
	const replacement = "replacement password after the cap"
	if status := a.post(owner, "198.51.100.1", "/api/v1/auth/reset/complete", map[string]string{"token": token, "password": replacement}); status != 204 {
		t.Fatal("reset answered", status)
	}
	owner.csrf = owner.session().CsrfToken
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", replacement, 200, "the browser that completed the reset")
}

func TestExpiredDeviceTokenCountsForNothing(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	owner := a.signedIn("198.51.100.1")
	a.spendAccount(1)
	later := time.Now().Add(auth.DeviceTokenLifetime + time.Hour)
	f.auth.Now = func() time.Time { return later }
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", f.password, 429, "token past its lifetime")
}

func TestReauthenticationFailsWhenThePasswordChangesDuringIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	principal := browserPrincipal(t, f.st, account, false)
	held, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(ctx)
	if _, err := held.Exec(ctx, "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", f.owner); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.auth.Reauthenticate(ctx, principal, auth.Credential{Password: f.password})
		result <- err
	}()
	waiting := false
	for deadline := time.Now().Add(10 * time.Second); !waiting && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err := f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%-- name: LockAccount%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if !waiting {
		t.Fatal("reauthentication did not reach the account lock")
	}
	hash, err := f.auth.Passwords.Hash("a replacement password for this test")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.New(held).UpdatePassword(ctx, db.UpdatePasswordParams{ID: f.owner, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The change ended the session, so it is refused as signed out.
	if err := <-result; !errors.Is(err, domain.Unauthenticated) {
		t.Fatal("the old password reauthenticated after it was replaced", err)
	}
}

func TestResetRequestIsNotRefusedByOthersSpendingTheEmailsBudget(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	attacker, owner := f.browser(t), f.browser(t)
	var newest string
	for range 3 {
		if status := a.post(attacker, "203.0.113.1", "/api/v1/auth/reset/request", map[string]string{"email": "owner@example.test"}); status != 202 {
			t.Fatal("reset request answered", status)
		}
		newest = tokenFromLinkMail(t, f, "/reset")
	}
	if status := a.post(owner, "198.51.100.1", "/api/v1/auth/reset/request", map[string]string{"email": "owner@example.test"}); status != 202 {
		t.Fatal("the owner's reset request answered", status)
	}
	waitForResetJobsToFinish(t, f)
	select {
	case <-f.mail:
		t.Fatal("a spent email budget still sent mail")
	default:
	}
	if status := a.post(owner, "198.51.100.1", "/api/v1/auth/reset/complete", map[string]string{"token": newest, "password": "replacement password for this test"}); status != 204 {
		t.Fatal("the newest link answered", status)
	}
}

type lockedLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *lockedLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(data)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// Nothing is installation-wide, so this is what bounds trying one password across every account:
// each account's own budget however many addresses take part, each address's own budget however
// many accounts it tries, and hash capacity (TestHashesNeverExceedSlotsAcrossPrincipals).
func TestSprayingIsBoundedWithoutAnInstallationBudget(t *testing.T) {
	t.Run("each account across many addresses", func(t *testing.T) {
		f := setup(t)
		a := newAvailability(t, f)
		sprayer := f.browser(t)
		const accounts, guessesPerAccount = 3, 10
		for account := range accounts {
			email := fmt.Sprintf("sprayed-%d@example.test", account)
			checked := 0
			for address := range 6 {
				for range 4 {
					switch status := a.login(sprayer, fmt.Sprintf("203.0.113.%d", address+1), email, wrongPassword); status {
					case 400:
						checked++
					case 429:
					default:
						t.Fatalf("spray answered %d", status)
					}
				}
			}
			if checked != guessesPerAccount {
				t.Fatalf("%s took %d guesses from 6 addresses, want %d", email, checked, guessesPerAccount)
			}
		}
	})
	t.Run("each address across many accounts", func(t *testing.T) {
		f := setup(t)
		a := newAvailability(t, f)
		seedAttempts(t, f, "login:ip:203.0.113.1", 48)
		sprayer := f.browser(t)
		want := []int{400, 400, 429, 429}
		for i, status := range want {
			if got := a.login(sprayer, "203.0.113.1", fmt.Sprintf("sprayed-%d@example.test", i), wrongPassword); got != status {
				t.Fatalf("attempt %d answered %d, want %d", i, got, status)
			}
		}
	})
}

func TestDeviceCookieWaitsForSecondFactor(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	var enrollment api.MFAEnrollment
	b.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": code, "current": map[string]string{"password": f.password}}, 200, nil)

	partial := f.browser(t)
	a.wantLogin(partial, "198.51.100.1", "owner@example.test", f.password, 200, "password step")
	if deviceCookie(t, f, partial) != nil {
		t.Fatal("a device cookie was issued before the second factor")
	}
}

func TestCompletedResetLiftsLockoutWhileGuessingContinues(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	a.spendAccount(1)
	owner := f.browser(t)
	const replacement = "replacement password after lockout"
	if status := a.post(owner, "198.51.100.1", "/api/v1/auth/reset/request", map[string]string{"email": "owner@example.test"}); status != 202 {
		t.Fatal("reset request refused during the attack", status)
	}
	token := tokenFromLinkMail(t, f, "/reset")
	if status := a.post(owner, "198.51.100.1", "/api/v1/auth/reset/complete", map[string]string{"token": token, "password": replacement}); status != 204 {
		t.Fatal("reset refused during the attack", status)
	}
	if got := attemptCount(t, f, "login:account:owner@example.test"); got != 0 {
		t.Fatalf("completed reset left %d failures on the account", got)
	}
	// Attackers spend the budget again at once, and the reset's browser still gets in.
	owner.csrf = owner.session().CsrfToken
	a.spendAccount(3)
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", replacement, 200, "the browser that completed the reset")
}

func TestManyAddressesCannotRefuseEveryAccount(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	flooder := f.browser(t)
	// 1000 attempts, sent far faster than they can be hashed. Before the fix they spent an
	// installation-wide budget of 1000 whether or not they were hashed.
	attempts := make(chan [2]int, 1000)
	for attempt := range 1000 {
		attempts <- [2]int{attempt % 20, attempt / 20}
	}
	close(attempts)
	var flood sync.WaitGroup
	for range 64 {
		flood.Go(func() {
			for attempt := range attempts {
				status := a.login(flooder, fmt.Sprintf("2001:db8:%x::1", attempt[0]+1), fmt.Sprintf("nobody-%d-%d@example.test", attempt[0], attempt[1]), wrongPassword)
				if status != 400 && status != 429 && status != 503 {
					t.Errorf("attempt did not reach the service: %d", status)
				}
			}
		})
	}
	flood.Wait()
	owner := f.browser(t)
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", f.password, 200, "unrelated source after the flood")
}

func TestKnownBrowserSignsInDuringFlood(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	owner := a.signedIn("198.51.100.1")
	flooder := f.browser(t)
	halt := make(chan struct{})
	var refused atomic.Int32
	var flood sync.WaitGroup
	for address := range 40 {
		flood.Go(func() {
			for attempt := 0; ; attempt++ {
				select {
				case <-halt:
					return
				default:
				}
				if a.login(flooder, fmt.Sprintf("203.0.113.%d", address+1), fmt.Sprintf("nobody-%d-%d@example.test", address, attempt), wrongPassword) == 503 {
					refused.Add(1)
				}
			}
		})
	}
	stop := sync.OnceFunc(func() {
		close(halt)
		flood.Wait()
	})
	defer stop()
	for deadline := time.Now().Add(30 * time.Second); refused.Load() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("flood never filled the queue")
		}
	}
	a.wantLogin(owner, "198.51.100.1", "owner@example.test", f.password, 200, "known browser during a flood that filled the queue")
}

func TestCapacityRefusalChargesNothing(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	const address = "198.51.100.9"
	const requests = 12
	flooder := f.browser(t)
	statuses := make(chan int, requests)
	start := make(chan struct{})
	var flood sync.WaitGroup
	for range requests {
		flood.Go(func() {
			<-start
			statuses <- a.login(flooder, address, "owner@example.test", wrongPassword)
		})
	}
	close(start)
	flood.Wait()
	close(statuses)
	counts := map[int]int32{}
	for status := range statuses {
		counts[status]++
	}
	if counts[503] == 0 {
		t.Fatalf("one principal's requests were never over its limit: %v", counts)
	}
	if got := attemptCount(t, f, "login:ip:"+address); got != counts[400] {
		t.Fatalf("source charged %d times for %d checked attempts: %v", got, counts[400], counts)
	}
	if got := attemptCount(t, f, "login:account:owner@example.test"); got != counts[400] {
		t.Fatalf("account charged %d times for %d checked attempts: %v", got, counts[400], counts)
	}
}

func TestPasswordChangeLoopCannotRefuseOtherSignIns(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	account := f.browser(t)
	account.login("owner@example.test", f.password)
	// Two loops share the browser, as an attacker would, each following its rotated session.
	var csrf atomic.Pointer[string]
	csrf.Store(&account.csrf)
	change := func(i int) {
		raw, _ := stablejson.Marshal(map[string]any{"password": f.password, "current": map[string]string{"password": f.password}})
		request, err := newRequest(account, "POST", "/api/v1/auth/password", raw)
		if err != nil {
			t.Error(err)
			return
		}
		request.Header.Set("X-CSRF-Token", *csrf.Load())
		response, err := account.client.Do(request)
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusOK {
			var session api.Session
			if err := json.UnmarshalRead(response.Body, &session); err == nil {
				csrf.Store(&session.CsrfToken)
			}
		}
	}
	seedOtherAccount(t, f, "victim@example.test")
	victim := f.browser(t)
	halt := make(chan struct{})
	var loops sync.WaitGroup
	for range 2 {
		loops.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-halt:
					return
				default:
					change(i)
				}
			}
		})
	}
	stop := sync.OnceFunc(func() {
		close(halt)
		loops.Wait()
	})
	defer stop()
	for i := range 10 {
		a.wantLogin(victim, fmt.Sprintf("198.51.100.%d", i+1), "victim@example.test", f.password, 200, "another account during the password changes")
		signOut(victim)
	}
	stop()
	// Each change proves the current password before hashing the new one, so the loops spend
	// the account's confirmation attempts, and at most as many changes.
	if got := attemptCount(t, f, "reauth:"+f.owner.String()); got != 5 {
		t.Fatalf("password changes charged %d confirmations, want the whole budget of 5", got)
	}
	if got := attemptCount(t, f, "password-change:"+f.owner.String()); got < 1 || got > 5 {
		t.Fatalf("password changes charged %d times, want between 1 and 5", got)
	}
	if got := attemptCount(t, f, "login:account:victim@example.test"); got != 0 {
		t.Fatalf("other account's budget holds %d attempts", got)
	}
}

func TestPasswordChangeHasBudgetAndSkipsEndedSessions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	ended := browserPrincipal(t, f.st, account, false)
	if _, err := f.st.Pool.Exec(ctx, "DELETE FROM sessions"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.ChangePassword(ctx, ended, auth.Credential{Password: f.password}, "a replacement password for this test"); err == nil {
		t.Fatal("ended session changed the password")
	}
	if got := attemptCount(t, f, "password-change:"+f.owner.String()); got != 0 {
		t.Fatalf("ended session spent %d password changes", got)
	}
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	current := f.password
	for i := range 5 {
		var session api.Session
		replacement := fmt.Sprintf("a replacement password number %d", i)
		b.call("POST", "/api/v1/auth/password", map[string]any{"password": replacement, "current": map[string]string{"password": current}}, 200, &session)
		b.csrf, current = session.CsrfToken, replacement
	}
	b.call("POST", "/api/v1/auth/password", map[string]any{"password": "one replacement password too many", "current": map[string]string{"password": current}}, 429, nil)
}

func TestIPv6NetworkSharesWiderBudget(t *testing.T) {
	f := setup(t)
	a := newAvailability(t, f)
	seedAttempts(t, f, "login:network:2001:db8:1::/48", 200)
	b := f.browser(t)
	a.wantLogin(b, "2001:db8:1:2::1", "owner@example.test", f.password, 429, "another /64 of a spent /48")
	a.wantLogin(b, "2001:db8:2:2::1", "owner@example.test", f.password, 200, "another /48")
}

func TestBogusSignupTokensCannotRefuseValidInvitation(t *testing.T) {
	f := setupBootstrap(t, false)
	f.st.Hosted = true
	f.st.Hosting = hosting.Config{CohortLimit: 1, ServerLimit: 2}
	a := newAvailability(t, f)
	if err := f.auth.InviteHosted(context.Background(), "beta@example.test", f.apiServer.Origin); err != nil {
		t.Fatal(err)
	}
	token := tokenFromLinkMail(t, f, "/signup")
	body := func(token string) map[string]string {
		return map[string]string{"token": token, "workspace": "Beta", "name": "Owner", "password": f.password}
	}
	flooder := f.browser(t)
	for address := range 10 {
		for range 10 {
			if status := a.post(flooder, fmt.Sprintf("203.0.113.%d", address+1), "/api/v1/hosting/signup", body("unknown-signup-token")); status != 400 {
				t.Fatal("bogus token answered", status)
			}
		}
	}
	// Each address is limited by its own budget. Before the fix they also spent a shared one.
	if status := a.post(flooder, "203.0.113.1", "/api/v1/hosting/signup", body("unknown-signup-token")); status != 429 {
		t.Fatal("source over its signup budget answered", status)
	}
	invited := f.browser(t)
	if status := a.post(invited, "198.51.100.1", "/api/v1/hosting/signup", body(token)); status != 200 {
		t.Fatal("valid invitation answered", status)
	}
}
