package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"

	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

type recoveryExportPaths []string

func (p *recoveryExportPaths) String() string { return "private host export directories" }
func (p *recoveryExportPaths) Set(value string) error {
	if value == "" {
		return errors.New("host export directory cannot be empty")
	}
	*p = append(*p, value)
	return nil
}

func recoverCrossHostCommand(ctx context.Context, st *store.Store, args []string) error {
	f := flag.NewFlagSet("recover-cross-host", flag.ContinueOnError)
	workspace := f.String("workspace", "", "workspace UUID to recover")
	owner := f.String("owner", "", "existing workspace owner UUID for missing imported records")
	expect := f.String("expected-state", "", "exact workspace state hash from the inspection output; omit to inspect")
	outDir := f.String("out", "", "new private handover directory required for a write")
	var exports recoveryExportPaths
	f.Var(&exports, "host-export", "private stopped-agent export directory; repeat for each required host")
	if err := f.Parse(args); err != nil {
		return err
	}
	ws, err := uuid.Parse(*workspace)
	if err != nil {
		return errors.New("workspace UUID is required")
	}
	account, err := uuid.Parse(*owner)
	if err != nil {
		return errors.New("existing owner UUID is required")
	}
	if *expect != "" {
		if *outDir == "" {
			return errors.New("a new --out directory is required for recovery handovers")
		}
		if err = os.Mkdir(*outDir, 0700); err != nil {
			return err
		}
	}
	service, err := execution.New(st)
	if err != nil {
		return err
	}
	result, err := service.RecoverCrossHost(ctx, execution.RecoveryImport{WorkspaceID: ws, OwnerID: account, ExpectedState: *expect, HostExports: exports})
	if err != nil {
		return err
	}
	if result.Applied {
		for server, handover := range result.Handovers {
			raw, err := json.Marshal(handover)
			if err != nil {
				return err
			}
			file, err := os.OpenFile(filepath.Join(*outDir, server.String()+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = file.Write(raw)
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		parent, err := os.Open(*outDir)
		if err != nil {
			return err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
