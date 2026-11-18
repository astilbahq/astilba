package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dnsCommand runs `astilba dns withdrawals|confirm-withdrawals`, for records the zone sweep holds
// back because it would withdraw too many at once. See docs/hosting.md#records-the-database-has-lost.
func dnsCommand(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	const usage = "usage: astilba dns withdrawals|confirm-withdrawals"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "withdrawals":
		f := flag.NewFlagSet("dns withdrawals", flag.ContinueOnError)
		sample := f.Int("sample", 20, "records of each hold to show, at most 1000")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *sample < 0 || *sample > 1000 {
			return errors.New("--sample must be between 0 and 1000")
		}
		holds, err := execution.ZoneHolds(ctx, pool, int32(*sample))
		if err != nil {
			return err
		}
		printZoneHolds(os.Stdout, holds)
		return nil
	case "confirm-withdrawals":
		f := flag.NewFlagSet("dns confirm-withdrawals", flag.ContinueOnError)
		hold := f.String("hold", "", "the hold's ID, as `astilba dns withdrawals` shows it")
		count := f.Int("count", -1, "how many records the hold holds, as `astilba dns withdrawals` shows it")
		operator := f.String("operator", "", "who confirms, kept with the hold")
		note := f.String("note", "", "why, kept with the hold")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		id, err := uuid.Parse(*hold)
		if err != nil {
			return errors.New("--hold must be a hold's ID")
		}
		if *count < 0 || *count > 1<<30 {
			return errors.New("--count must be the number of records the hold holds")
		}
		if err = execution.ConfirmZoneHold(ctx, pool, id, int32(*count), *operator, *note); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Confirmed. The next zone sweep, within %s, withdraws the hold's records it still finds should go.\n", execution.ZoneSweepInterval)
		return nil
	}
	return errors.New(usage)
}

func printZoneHolds(w io.Writer, holds []execution.ZoneHold) {
	if len(holds) == 0 {
		fmt.Fprintln(w, "No DNS withdrawals are held.")
		return
	}
	for _, h := range holds {
		state := "awaiting confirmation"
		if h.ConfirmedAt != nil {
			state = fmt.Sprintf("confirmed %s by %q: %q", h.ConfirmedAt.UTC().Format(time.RFC3339), h.ConfirmedBy.String, h.ConfirmationNote.String)
		}
		fmt.Fprintf(w, "hold %s  zone %s  %s\n", h.ID, h.Zone, state)
		fmt.Fprintf(w, "  %d records to withdraw, unbacked or newer than their server's approval, of %d of this installation's address records listed; opened %s, last seen %s\n",
			h.Records, h.Marked, h.OpenedAt.UTC().Format(time.RFC3339), h.SeenAt.UTC().Format(time.RFC3339))
		for _, ws := range h.Workspaces {
			owner := "servers this database doesn't know"
			if ws.WorkspaceID != nil {
				owner = "workspace " + ws.WorkspaceID.String()
			}
			fmt.Fprintf(w, "  %6d  %s\n", ws.Records, owner)
		}
		for _, r := range h.Sample {
			fmt.Fprintf(w, "  %-5s %-40s %s  server %s  record %s\n", r.RecordType, r.Content, r.Name, r.ServerID, r.RecordID)
		}
		if int64(len(h.Sample)) < int64(h.Records) {
			fmt.Fprintf(w, "  … and %d more\n", int64(h.Records)-int64(len(h.Sample)))
		}
		if h.ConfirmedAt == nil {
			fmt.Fprintf(w, "  Confirm with: astilba dns confirm-withdrawals --hold %s --count %d --operator NAME --note REASON\n", h.ID, h.Records)
		}
	}
}
