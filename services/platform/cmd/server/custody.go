package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/config"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// keyBackend is the configured key service, paired with the keyring that values sealed before
// per-workspace keys still open under.
func keyBackend(c config.Config, keys *cryptobox.Keyring) (custody.Legacy, error) {
	if c.KeyBackend != "transit" {
		return custody.NewFile(keys)
	}
	t, err := custody.NewTransit(custody.TransitConfig{
		Address: c.Transit.Address, Mount: c.Transit.Mount, Namespace: c.Transit.Namespace, CAFile: c.Transit.CAFile,
		Auth: custody.TransitAuth{Method: c.Transit.Auth, Mount: c.Transit.AuthMount, RoleIDFile: c.Transit.RoleIDFile, SecretIDFile: c.Transit.SecretIDFile, JWTFile: c.Transit.JWTFile, Role: c.Transit.JWTRole},
		// Plain HTTP only to a loopback server, in local development.
		Loopback: c.Development && !c.Hosted,
	})
	if err != nil {
		return nil, err
	}
	return custody.WithKeyring(t, keys)
}

// initTransitCommand sets up a local OpenBao dev server for the local stack's --custody=openbao
// profile. A real server is set up by its operator from docs/key-custody-transit.md: management
// never holds a token that can change the key service's own policies.
func initTransitCommand(ctx context.Context, out io.Writer, args []string) error {
	f := flag.NewFlagSet("custody init-transit", flag.ContinueOnError)
	dev := f.Bool("dev", false, "set up a local OpenBao dev server")
	address := f.String("address", "", "the dev server's loopback address, such as http://127.0.0.1:8200")
	tokenFile := f.String("token-file", "", "private file holding the dev server's root token")
	dir := f.String("out", "", "directory for the AppRole credential files")
	if err := f.Parse(args); err != nil {
		return err
	}
	if !*dev {
		return errors.New("init-transit sets up only a local OpenBao dev server, with --dev; set up a real server with docs/key-custody-transit.md")
	}
	if *address == "" || *tokenFile == "" || *dir == "" || f.NArg() > 0 {
		return errors.New("usage: astilba custody init-transit --dev --address <loopback URL> --token-file <file> --out <directory>")
	}
	info, err := os.Stat(*tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("--token-file must be a private regular file (mode 0600)")
	}
	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		return err
	}
	creds, err := custody.ProvisionTransitDev(ctx, *address, strings.TrimSpace(string(raw)), *dir)
	clear(raw)
	if err != nil {
		return err
	}
	for _, name := range custody.TransitPolicyNames {
		fmt.Fprintf(out, "%s: role ID in %s, secret ID in %s\n", name, creds[name].RoleIDFile, creds[name].SecretIDFile)
	}
	return nil
}

// custodyCommand runs `astilba custody status|history|rewrap|init-keys`. Status and history change
// nothing; rewrap records its start and finish in the installation's key history, and init-keys
// each key it makes.
func custodyCommand(ctx context.Context, c config.Config, pool *pgxpool.Pool, st *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: astilba custody status|history|rewrap|init-keys|init-transit")
	}
	switch args[0] {
	case "status":
		s, err := st.Broker.Status(ctx, pool, c.InstallationID)
		if err != nil {
			return err
		}
		printStatus(os.Stdout, s, c.InstallationID)
		return nil
	case "history":
		f := flag.NewFlagSet("custody history", flag.ContinueOnError)
		limit := f.Int("limit", 50, "most recent events to show, at most 1000")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *limit < 1 || *limit > 1000 {
			return errors.New("--limit must be between 1 and 1000")
		}
		events, err := db.New(pool).InstallationKeyEvents(ctx, int32(*limit))
		if err != nil {
			return err
		}
		for _, e := range events {
			fmt.Fprintf(os.Stdout, "%s  %-20s run %s", e.At.UTC().Format(time.RFC3339), e.Kind, e.RunID)
			for _, field := range [][2]string{{"operator", e.Operator.String}, {"backend", e.BackendID.String}, {"reason", e.Reason.String}} {
				if field[1] != "" {
					fmt.Fprintf(os.Stdout, "  %s=%q", field[0], field[1])
				}
			}
			if string(e.Details) != "{}" {
				fmt.Fprintf(os.Stdout, "  %s", e.Details)
			}
			fmt.Fprintln(os.Stdout)
		}
		return nil
	case "rewrap":
		f := flag.NewFlagSet("custody rewrap", flag.ContinueOnError)
		operator := f.String("operator", "", "who runs the rewrap, kept in the installation's key history")
		reason := f.String("reason", "", "why, kept with it")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if err := c.RequirePinnedInstallation(); err != nil {
			return err
		}
		if err := st.Broker.Install(ctx, pool, c.InstallationID, true); err != nil {
			return err
		}
		return st.Rewrap(ctx, *operator, *reason)
	case "init-keys":
		// `serve` does the same in the background; this runs it to the end, and is safe to run
		// beside it.
		if len(args) > 1 {
			return errors.New("usage: astilba custody init-keys")
		}
		if err := c.RequirePinnedInstallation(); err != nil {
			return err
		}
		if err := st.Broker.Install(ctx, pool, c.InstallationID, false); err != nil {
			return err
		}
		for {
			more, err := st.Broker.InitKeys(ctx, func() bool { return true })
			if err != nil || !more {
				if err == nil {
					fmt.Fprintln(os.Stdout, "every workspace, the installation and the token keys have keys")
				}
				return err
			}
			// Another instance holds some holder's lock: wait for it, then look again.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return errors.New("usage: astilba custody status|history|rewrap|init-keys|init-transit")
}

func printStatus(w io.Writer, s secretbroker.Status, configured uuid.UUID) {
	switch {
	case !s.Matches:
		fmt.Fprintf(w, "installation   %s (ASTILBA_INSTALLATION_ID is %s)\n", s.Installation, configured)
	case s.Pinned:
		fmt.Fprintf(w, "installation   %s (pinned)\n", s.Installation)
	default:
		fmt.Fprintf(w, "installation   %s (not pinned)\n", s.Installation)
	}
	fmt.Fprintf(w, "key backend    %s\n", s.Backend)
	for _, b := range s.Backends {
		fmt.Fprintf(w, "registered     %s %s\n", b.BackendID, b.Role)
	}
	states := make([]string, 0, len(custody.Classes))
	for _, class := range custody.Classes {
		states = append(states, string(class)+" "+s.Canaries[class])
	}
	fmt.Fprintf(w, "canaries       %s\n", strings.Join(states, ", "))
	fmt.Fprintf(w, "custody stage  %d\n", s.Stage)
	fmt.Fprintf(w, "keys           %d of %d workspaces, installation key %s, token key %s\n", s.Keys.WithKeys, s.Keys.Workspaces, keyState(s.Keys.Installation), keyState(s.Keys.Token))
	pending := s.Pending()
	if len(pending) == 0 {
		fmt.Fprintln(w, "pending        none")
		return
	}
	for i, p := range pending {
		label := ""
		if i == 0 {
			label = "pending"
		}
		fmt.Fprintf(w, "%-14s - %s\n", label, p)
	}
}

func keyState(present bool) string {
	if present {
		return "present"
	}
	return "missing"
}
