package portability

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"encoding/json/jsontext"

	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker/brokertest"
	"github.com/google/uuid"
)

func TestImportedRowChecks(t *testing.T) {
	for _, tc := range []struct {
		name, table, column, value string
		ok                         bool
	}{
		{"ordinary generation", "environments", "execution_generation", `41`, true},
		{"generation at the bound", "environments", "execution_generation", `9007199254740992`, true},
		{"generation near overflow", "environments", "execution_generation", `9223372036854775807`, false},
		{"negative generation", "workspace_networks", "generation", `-1`, false},
		{"sequence near overflow", "execution_operations", "report_sequence", `9223372036854775807`, false},
		{"optional counter absent", "preview_requests", "cleanup_controller", `null`, true},
		{"counter of the wrong type", "servers", "update_generation", `"1"`, false},
		{"other columns are not counters", "preview_requests", "head_repository", `9223372036854775807`, true},
		{"plain decimal", "visual_plans", "plan", `{"a":[1.5,-4,"1e100000"]}`, true},
		{"number with an exponent", "visual_plans", "plan", `{"a":[1e100000]}`, false},
		{"number with a negative exponent", "backup_policies", "status", `{"a":1E-100000}`, false},
		{"very long number", "backup_policies", "status", `{"a":` + strings.Repeat("1", 65) + `}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkImportedRow(tc.table, Row{tc.column: jsontext.Value(tc.value)}); (err == nil) != tc.ok {
				t.Fatalf("value %s: %v", tc.value, err)
			}
		})
	}
}

func TestImportedValueLimits(t *testing.T) {
	value := func(column string, size int) Row {
		return Row{column: jsontext.Value(`"` + strings.Repeat("x", size) + `"`)}
	}
	for name, limit := range importedValueLimits {
		table, column, _ := strings.Cut(name, ".")
		if err := checkImportedRow(table, value(column, limit-2)); err != nil {
			t.Fatal(name, "refused a value at its limit", err)
		}
		if err := checkImportedRow(table, value(column, limit)); !errors.Is(err, ErrArchive) {
			t.Fatal(name, "accepted a value over its limit", err)
		}
	}
	if err := checkImportedRow("visual_plans", value("plan", 2<<20)); err != nil {
		t.Fatal("refused a large value in a column with no limit of its own", err)
	}
	if err := checkImportedRow("visual_plans", value("plan", maxImportedValue)); !errors.Is(err, ErrArchive) {
		t.Fatal("accepted a value over the general limit", err)
	}
}

// Keys.Seal used to accept this, leaving a row that stopped wrapping-key rotation for the
// whole installation.
func TestImportRefusesACredentialThatCouldNotBeOpened(t *testing.T) {
	broker, _ := brokertest.New(t)
	ws := uuid.New()
	keys, err := broker.Begin(context.Background(), secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(uuid.New()), Kinds: secretbroker.CredentialKinds, NewWorkspace: true})
	if err != nil {
		t.Fatal(err)
	}
	row := func(size int) Row {
		value := `\\x` + hex.EncodeToString([]byte(strings.Repeat("x", size)))
		r := Row{"id": jsontext.Value(`"` + uuid.NewString() + `"`), "encrypted_value": jsontext.Value(`"` + value + `"`), "erased_at": jsontext.Value("null")}
		return r
	}
	if err = transformCredential(keys, "secret_versions", row(1024), false); err != nil {
		t.Fatal("refused an ordinary credential", err)
	}
	if err = transformCredential(keys, "secret_versions", row(2<<20), false); !errors.Is(err, ErrArchive) {
		t.Fatal("accepted an oversize credential", err)
	}
}
