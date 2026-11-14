package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// resetMFACommand turns off an account's two-factor authentication and replaces its password,
// for an account holder who has lost their authenticator or found someone else's on the
// account. See docs/incident-response.md.
func resetMFACommand(ctx context.Context, st *store.Store, mail mailer.Config, args []string) error {
	f := flag.NewFlagSet("reset-mfa", flag.ContinueOnError)
	email := f.String("email", "", "the account's email address")
	reason := f.String("reason", "", "why, as the account holder will read it in their notice (one line, at most 500 bytes)")
	if err := f.Parse(args); err != nil {
		return err
	}
	sender, err := mailer.New(mail)
	if err != nil {
		return err
	}
	passwords, err := auth.NewPasswords()
	if err != nil {
		return err
	}
	// The notice is a job that the serving process sends, so this client only inserts it.
	queue, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		return err
	}
	service := &auth.Service{Store: st, Passwords: passwords, Mail: sender, Now: time.Now, Queue: queue}
	account, err := service.ResetMFA(ctx, *email, *reason)
	var invalid *domain.Error
	switch {
	case errors.Is(err, domain.NotFound):
		return errors.New("no account has that email address")
	case errors.Is(err, domain.Conflict):
		return errors.New("that account does not use two-factor authentication, so there is nothing to reset; its holder can reset their password")
	case errors.As(err, &invalid) && invalid.Code == "invalid_input":
		return fmt.Errorf("check --%s: --email takes the account's address, and --reason one line of at most 500 bytes", invalid.Fields[0])
	case err != nil:
		return err
	}
	fmt.Printf("Reset two-factor authentication for account %s. The serving process emails its holder, who must reset their password to sign in.\n", account)
	return nil
}
