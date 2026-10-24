package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/config"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/httpapi"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/webassets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var revision = "development"

func main() {
	if err := run(); err != nil {
		slog.Error("command failed", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: astilba keygen|migrate|bootstrap|rewrap|invite|grant-management|prepare-recovery|serve")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if os.Args[1] == "keygen" {
		f := flag.NewFlagSet("keygen", flag.ContinueOnError)
		file := f.String("out", "", "new private keyring file")
		id := f.String("id", "primary", "wrapping key ID")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *file == "" {
			return errors.New("--out is required")
		}
		keys, e := cryptobox.New(*id)
		if e != nil {
			return e
		}
		return keys.WriteNew(*file)
	}
	c, e := config.Load()
	if e != nil {
		return e
	}
	if os.Args[1] == "migrate" {
		return dbmigrate.Up(ctx, c.DatabaseURL)
	}
	if e = dbmigrate.Check(ctx, c.DatabaseURL); e != nil {
		return e
	}
	var keys *cryptobox.Keyring
	if c.KeyringJSON != "" {
		keys, e = cryptobox.Parse([]byte(c.KeyringJSON))
	} else {
		keys, e = cryptobox.Load(c.Keyring)
	}
	if e != nil {
		return e
	}
	pool, e := pgxpool.New(ctx, c.DatabaseURL)
	if e != nil {
		return errors.New("cannot configure database")
	}
	defer pool.Close()
	st := store.New(pool, keys, c.Hosted)
	st.Hosting = c.Hosting
	st.LogRetention = c.LogRetention
	switch os.Args[1] {
	case "invite":
		f := flag.NewFlagSet("invite", flag.ContinueOnError)
		email := f.String("email", "", "beta invitee email")
		if e = f.Parse(os.Args[2:]); e != nil {
			return e
		}
		mail, e := mailer.New(c.SMTP)
		if e != nil {
			return e
		}
		return (&auth.Service{Store: st, Mail: mail, Now: time.Now}).InviteHosted(ctx, *email)
	case "prepare-recovery":
		f := flag.NewFlagSet("prepare-recovery", flag.ContinueOnError)
		server := f.String("server", "", "restored server UUID")
		generation := f.Int64("controller-generation", 0, "generation greater than the host's current controller")
		if e = f.Parse(os.Args[2:]); e != nil {
			return e
		}
		id, e := uuid.Parse(*server)
		if e != nil {
			return domain.Invalid("server")
		}
		return st.PrepareRecovery(ctx, id, *generation)
	case "grant-management":
		f := flag.NewFlagSet("grant-management", flag.ContinueOnError)
		ws := f.String("workspace", "", "workspace UUID")
		until := f.String("until", "", "RFC3339 expiration")
		limit := f.Int("servers", 0, "server allowance")
		if e = f.Parse(os.Args[2:]); e != nil {
			return e
		}
		id, e := uuid.Parse(*ws)
		if e != nil {
			return domain.Invalid("workspace")
		}
		end, e := time.Parse(time.RFC3339, *until)
		if e != nil {
			return domain.Invalid("until")
		}
		return st.GrantManagement(ctx, id, end, *limit)
	case "rewrap":
		return st.Rewrap(ctx)
	case "bootstrap":
		f := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
		email := f.String("email", "", "owner email")
		name := f.String("name", "", "owner name")
		workspace := f.String("workspace", "", "workspace name")
		file := f.String("password-file", "-", "private password file or - for stdin")
		if e = f.Parse(os.Args[2:]); e != nil {
			return e
		}
		address, e := domain.Email(*email)
		if e != nil {
			return e
		}
		owner, e := domain.Name(*name)
		if e != nil {
			return e
		}
		ws, e := domain.Name(*workspace)
		if e != nil {
			return e
		}
		var reader io.Reader = os.Stdin
		var opened *os.File
		if *file != "-" {
			opened, e = os.Open(*file)
			if e != nil {
				return errors.New("cannot read password file")
			}
			defer opened.Close()
			info, e := opened.Stat()
			if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return errors.New("password file must be regular and owner-only")
			}
			reader = opened
		}
		data, e := io.ReadAll(io.LimitReader(reader, 514))
		if e != nil {
			return errors.New("cannot read password")
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		passwords, e := auth.NewPasswords()
		if e != nil {
			return e
		}
		hash, e := passwords.Hash(ctx, password)
		if e != nil {
			return e
		}
		_, e = st.Bootstrap(ctx, address, owner, hash, ws)
		return e
	case "serve":
		if e = c.ValidateServer(); e != nil {
			return e
		}
		mail, e := mailer.New(c.SMTP)
		if e != nil {
			return e
		}
		passwords, e := auth.NewPasswords()
		if e != nil {
			return e
		}
		service := &auth.Service{Store: st, Passwords: passwords, Mail: mail, Now: time.Now}
		api, cleanup := httpapi.New(st, service, c.Origin, c.Development, slog.Default())
		api.TrustedProxies = c.TrustedProxies
		defer cleanup()
		notices := &billing.Service{Store: st, Now: time.Now}
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				sweep, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := st.PruneHistory(sweep); err != nil && ctx.Err() == nil {
					slog.Warn("diagnostic retention sweep failed")
				}

				cancel()
				alertCtx, alertCancel := context.WithTimeout(ctx, 30*time.Second)
				if err := api.Alerts.Sweep(alertCtx); err != nil && ctx.Err() == nil {
					slog.Warn("operational alert sweep failed")
				}
				alertCancel()
				if c.Hosted {
					if err := notices.NotifyExpiring(ctx, mail); err != nil && ctx.Err() == nil {
						slog.Warn("workspace expiry notification sweep failed")
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()

		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				deliveryCtx, deliveryCancel := context.WithTimeout(ctx, 30*time.Second)
				if err := api.Alerts.Deliver(deliveryCtx); err != nil && ctx.Err() == nil {
					slog.Warn("operational alert delivery failed")
				}
				deliveryCancel()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()

		handler, e := api.Handler(webassets.Handler())
		if e != nil {
			return e
		}
		if dir := os.Getenv("ASTILBA_AGENT_RELEASES"); dir != "" {
			keyBytes, err := os.ReadFile(os.Getenv("ASTILBA_AGENT_RELEASE_KEY"))
			if err != nil {
				return errors.New("release public key unavailable")
			}
			key, err := agentrelease.PublicKey(keyBytes)
			if err != nil {
				return err
			}
			api.Execution.Releases, err = agentrelease.Load(dir, key)
			if err != nil {
				return err
			}
			api.ArtifactDirectory = dir
		}
		if e = api.Execution.Start(ctx); e != nil {
			return e
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = api.Execution.Stop(c)
		}()
		server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
		slog.Info("server starting", "revision", revision)
		if e = server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server stopped")
		}
		return nil
	default:
		return errors.New("unknown command")
	}
}
