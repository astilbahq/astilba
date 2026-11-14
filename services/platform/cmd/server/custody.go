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
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// custodyCommand runs `astilba custody status|history|rewrap|init-keys`. Status and history change
// nothing; rewrap records its start and finish in the installation's key history, and init-keys
// each key it makes.
func custodyCommand(ctx context.Context, c config.Config, pool *pgxpool.Pool, st *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: astilba custody status|history|rewrap|init-keys")
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
	return errors.New("usage: astilba custody status|history|rewrap|init-keys")
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
