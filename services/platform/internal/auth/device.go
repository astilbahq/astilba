package auth

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A device token records that this browser completed sign-in to this account. It waives no
// credential check. It moves the holder from the budgets anyone can spend by naming the email
// to ones only its holders can, so a guessing attack on the account cannot refuse the person
// who signs in from their usual browser.
const (
	DeviceTokenLifetime = 90 * 24 * time.Hour
	deviceIdentity      = "login-device:v1"
)

type deviceToken struct {
	Account uuid.UUID `json:"account"`
	Email   string    `json:"email"`
	Version int64     `json:"version"`
	Nonce   string    `json:"nonce"`
	Expires time.Time `json:"expires"`
}

// IssueDeviceToken ties a token to the account's current auth version, so a password change,
// a recovery code or anything else that ends the account's sessions also ends these.
func (s *Service) IssueDeviceToken(account db.Account) (string, error) {
	nonce, err := cryptobox.Token()
	if err != nil {
		return "", err
	}
	raw, err := stablejson.Marshal(deviceToken{account.ID, account.Email, account.AuthVersion, nonce, s.Now().Add(DeviceTokenLifetime)})
	if err != nil {
		return "", err
	}
	blob, err := s.Store.Keys.Seal(raw, deviceIdentity)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// openDeviceToken returns nil for anything that is not a live token for this email. Such a
// request is simply treated as coming from a browser with no token.
func (s *Service) openDeviceToken(ctx context.Context, email, token string) (*deviceToken, error) {
	blob, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, nil
	}
	raw, err := s.Store.Keys.Open(blob, deviceIdentity)
	if err != nil {
		return nil, nil
	}
	var device deviceToken
	if json.Unmarshal(raw, &device) != nil || device.Email != email || !device.Expires.After(s.Now()) {
		return nil, nil
	}
	account, err := s.Store.Queries.AccountByID(ctx, device.Account)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if account.AuthVersion != device.Version {
		return nil, nil
	}
	return &device, nil
}

// presentedDevice takes the first live token among those sent. A browser can send several: a
// host sharing the domain can plant a cookie of the same name, and browsers list the older first.
func (s *Service) presentedDevice(ctx context.Context, email string, tokens []string) (*deviceToken, error) {
	for _, token := range tokens {
		device, err := s.openDeviceToken(ctx, email, token)
		if device != nil || err != nil {
			return device, err
		}
	}
	return nil, nil
}

func accountBudget(email string) string { return "password:account:" + email }
func accountSourceBudget(email, source string) string {
	return accountBudget(email) + ":source:" + source
}

// loginAttempt decides who pays for a sign-in. Without a device token the account's limits are
// shared by everyone claiming that email: five from each source, so one source cannot lock out
// another, and ten overall, which bounds guessing however many addresses take part. With a
// token each token has its own five, so a stolen one cannot spend another's. Success never
// clears the cap across tokens, because anyone with the password can mint more, but it belongs
// to the auth version, so a password reset starts a new one.
func loginAttempt(email string, from Source, device *deviceToken) (passwordShare, []budget) {
	if device != nil {
		return passwordShare{"account:" + device.Account.String(), true}, []budget{
			{fmt.Sprintf("password:device-account:%s:%d", device.Account, device.Version), 20, attemptWindow},
			{"password:device:" + device.Nonce, 5, attemptWindow},
		}
	}
	return passwordShare{from.principal(), false}, append(sourceBudgets("password", from, 50),
		budget{accountSourceBudget(email, from.Key), 5, attemptWindow},
		budget{accountBudget(email), 10, attemptWindow},
	)
}

// Login checks a password. A malformed email cannot match any account, so it spends nothing.
func (s *Service) Login(ctx context.Context, email, password string, from Source, devices []string) (db.Account, error) {
	var account db.Account
	email, err := domain.Email(email)
	if err != nil {
		return account, domain.Credentials
	}
	device, err := s.presentedDevice(ctx, email, devices)
	if err != nil {
		return account, err
	}
	share, budgets := loginAttempt(email, from, device)
	err = s.withPassword(ctx, share, budgets, func() error {
		var err error
		account, err = s.Store.Queries.AccountByEmail(ctx, email)
		hash := account.PasswordHash
		if errors.Is(err, pgx.ErrNoRows) {
			hash = s.Passwords.Dummy
		} else if err != nil {
			return err
		}
		return passwordMatches(password, hash)
	})
	if err != nil {
		return db.Account{}, err
	}
	if account.ID == uuid.Nil {
		return account, domain.Credentials
	}
	// A known browser spent none of these. Clearing them would hand guessers a new budget each
	// time the owner signs in, and that sign-in is exactly what they cannot prevent any more.
	if device != nil {
		return account, nil
	}
	return account, s.clearLoginFailures(ctx, s.Store.Queries, email, from.Key)
}

func (s *Service) clearLoginFailures(ctx context.Context, q *db.Queries, email, source string) error {
	if err := q.ClearAuthAttempts(ctx, Hash(accountSourceBudget(email, source))); err != nil {
		return err
	}
	return q.ClearAuthAttempts(ctx, Hash(accountBudget(email)))
}
