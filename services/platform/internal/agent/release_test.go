package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestReleaseDiagnosticsCannotOverwriteSecretPatterns(t *testing.T) {
	_, p := testVisualJournal(t)
	app, secret := uuid.New(), uuid.New()
	p.Release = &deployment.Release{ApplicationID: app}
	plain := "decoded-password-canary"
	c := visualCommand(p, "deploy", 1)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/logs") {
			fmt.Fprint(w, "migration-failed: "+plain)
			return
		}
		if strings.Contains(r.URL.Path, p.ContainerName(app)) {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "candidate", "Config": map[string]any{"Tty": true, "Labels": map[string]string{labelServer: p.ServerID.String(), labelEnvironment: p.EnvironmentID.String(), "com.astilba.unit": p.UnitID.String(), labelResource: app.String(), labelKind: "visual-v1", "com.astilba.plan": p.Hash}, "Env": []string{secret.String() + "=replacement", "DATABASE_URL=postgres://app:" + plain + "@database/app"}}, "State": map[string]any{"Status": "exited", "ExitCode": 23}})
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"absent"}`)
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	note := (&Engine{Docker: docker}).releaseDiagnostics(context.Background(), c, deployment.Payload{Plan: p, Secrets: map[string]string{secret.String(): plain}})
	if strings.Contains(note, plain) || !strings.Contains(note, "migration-failed: [redacted]") || !strings.Contains(note, "exit=23") {
		t.Fatalf("diagnostic redaction lost secret provenance: %s", note)
	}
}
