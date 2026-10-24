package agent

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const backupS3Image = "dxflrs/garage:v2.4.1@sha256:9c96caa2612d3411acc5b0e6701fb238dbfba33e533a6d7d3d811a4b12d0d020"

func nativeS3Backup(t *testing.T, e *Engine, policy backup.Policy, databasePassword string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	key := "GK" + strings.ReplaceAll(uuid.NewString(), "-", "")
	secret := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	config := `metadata_dir = "/data/meta"
data_dir = "/data/objects"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "127.0.0.1:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "` + secret + `"
[s3_api]
s3_region = "garage"
api_bind_addr = "0.0.0.0:3900"
`
	path := filepath.Join(dir, "garage.toml")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	port := network.MustParsePort("3900/tcp")
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-s3-" + uuid.NewString(), Config: &container.Config{Image: backupS3Image, Entrypoint: []string{"/garage"}, Cmd: []string{"server", "--single-node", "--default-bucket"}, Env: []string{"GARAGE_DEFAULT_ACCESS_KEY=" + key, "GARAGE_DEFAULT_SECRET_KEY=" + secret, "GARAGE_DEFAULT_BUCKET=recovery-fixture"}, ExposedPorts: network.PortSet{port: {}}, Labels: map[string]string{labelServer: policy.ServerID.String(), labelKind: "backup-s3-fixture"}}, HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: path, Target: "/etc/garage.toml", ReadOnly: true}}, PortBindings: network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}}, Resources: container.Resources{Memory: 512 << 20}}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, err := e.Docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	address := "http://127.0.0.1:" + inspect.Container.NetworkSettings.Ports[port][0].HostPort
	ready := false
	for range 100 {
		res, err := http.Get(address)
		if err == nil {
			res.Body.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("S3 fixture did not start")
	}
	origin, _ := url.Parse(address)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(origin))
	defer proxy.Close()
	cert := filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	original := e.ResticCLI
	defer func() { e.ResticCLI = original }()
	wrapper := filepath.Join(dir, "restic")
	if err = os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec /opt/astilba-platform-tools/restic --cacert '%s' \"$@\"\n", cert)), 0700); err != nil {
		t.Fatal(err)
	}
	e.ResticCLI = wrapper
	policy.ID = uuid.New()
	policy.DestinationID = uuid.New()
	policy.Retain = 1
	in := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "S3 fixture", Kind: "s3", Endpoint: proxy.URL, Bucket: "recovery-fixture", Region: "garage"}, Access: backup.Access{AccessKey: key, SecretKey: secret, Password: "s3-repository-password-canary", DatabasePassword: databasePassword}}
	if err = e.prepareRepository(ctx, &in, true); err != nil {
		t.Fatal(err)
	}
	s := scheduleState{Installation: in, Runs: []backup.Run{}}
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err != nil {
		t.Fatalf("S3 upload: %v", err)
	}
	snapshot := s.Runs[0].Snapshot
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), snapshot, uuid.New()); err != nil {
		t.Fatalf("S3 restore: %v", err)
	}
	if !s.Runs[len(s.Runs)-1].Verified {
		t.Fatal("S3 restore not verified")
	}
	s.Installation.Access.SecretKey = "invalid-storage-credential"
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err == nil {
		t.Fatal("invalid S3 credential accepted")
	}
	if s.Runs[len(s.Runs)-1].State != "failed" || s.Runs[len(s.Runs)-1].Snapshot != "" {
		t.Fatal("failed S3 upload reported a usable snapshot")
	}
}
