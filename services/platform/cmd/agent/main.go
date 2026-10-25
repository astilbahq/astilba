package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

var version = "development"

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: astilba-agent describe|preflight|enroll|transfer|run|retry|resolve-run|inspect-upgrades|inspect-controller|update-local|release-keygen|release-sign")
	}
	command := os.Args[1]
	if command == "describe" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"Version": version, "Protocol": 1, "Arch": runtime.GOARCH})
	}
	if command == "release-keygen" || command == "release-sign" {
		return releaseCommand(command, os.Args[2:])
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	dir := flags.String("state", "/var/lib/astilba-agent", "protected state directory")
	origin := flags.String("management", "", "exact HTTPS management origin")
	transferGeneration := flags.Int64("transfer-generation", 0, "confirmed target controller generation")
	server := flags.String("server", "", "server UUID from the dashboard")
	tokenFile := flags.String("token-file", "-", "private enrollment token file or stdin")
	publicFile := flags.String("release-key", "", "trusted Ed25519 public key PEM")
	host := flags.String("docker", "unix:///var/run/docker.sock", "local Docker Unix socket")
	dockerCLI := flags.String("docker-cli", "docker", "Docker CLI with the supported Compose plugin")
	acmeEmail := flags.String("acme-email", "", "certificate contact email, applied only by owner-reviewed host changes")
	resticCLI := flags.String("restic-cli", "restic", "pinned restic executable")
	backupRoots := flags.String("backup-roots", "", "comma-separated operator-approved filesystem roots")
	policyID := flags.String("policy", "", "backup policy UUID for local reconciliation")
	runID := flags.String("run", "", "scheduled run UUID after local inspection")
	operation := flags.String("operation", "", "operation UUID to resume after local inspection")
	manifest := flags.String("manifest", "", "signed release manifest")
	artifact := flags.String("artifact", "", "local agent artifact")
	executable := flags.String("executable", "/usr/local/bin/astilba-agent", "installed executable")
	if e := flags.Parse(os.Args[2:]); e != nil {
		return e
	}
	if !strings.HasPrefix(*host, "unix:///") {
		return errors.New("Docker must be an explicitly selected local Unix socket")
	}
	if *acmeEmail != "" {
		addr, err := mail.ParseAddress(*acmeEmail)
		if err != nil || addr.Address != *acmeEmail {
			return errors.New("invalid certificate contact email")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	docker, e := client.New(client.WithHost(*host))
	if e != nil {
		return e
	}
	defer docker.Close()
	preflight := func(ctx context.Context) api.AgentHeartbeat {
		h := agent.Preflight(ctx, docker, version, *dir, *dockerCLI, *resticCLI)
		configured := *acmeEmail != ""
		h.Preflight.CertificateContactConfigured = &configured
		return h
	}
	if command == "preflight" {
		return json.NewEncoder(os.Stdout).Encode(preflight(ctx))
	}
	j, e := agent.Open(*dir)
	if e != nil {
		return e
	}
	defer j.Close()
	if command == "inspect-controller" {
		cfg, err := j.Config()
		if err != nil {
			return err
		}
		inventory, err := j.Inventory()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Origin     string                  `json:"origin"`
			ServerID   uuid.UUID               `json:"serverId"`
			Generation int64                   `json:"controllerGeneration"`
			Enrolled   bool                    `json:"enrolled"`
			Inventory  api.ControllerInventory `json:"inventory"`
		}{cfg.Origin, cfg.ServerID, cfg.ControllerGeneration, cfg.Enrolled, inventory})
	}
	if command == "inspect-upgrades" {
		views, err := j.UpgradeViews()
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(views)
	}
	if command == "enroll" || command == "transfer" {
		if e = agent.ValidateOrigin(*origin); e != nil {
			return e
		}
		id, e := uuid.Parse(*server)
		if e != nil {
			return errors.New("server UUID required")
		}
		cfg, e := j.Config()
		if errors.Is(e, os.ErrNotExist) {
			cfg = agent.Config{Origin: *origin, ServerID: id, MachineID: uuid.New()}
		} else if e != nil {
			return e
		}
		var in io.Reader = os.Stdin
		if *tokenFile != "-" {
			f, e := privateFile(*tokenFile)
			if e != nil {
				return e
			}
			defer f.Close()
			in = f
		}
		token, e := io.ReadAll(io.LimitReader(in, 128))
		if e != nil {
			return e
		}
		if command == "transfer" {
			if cfg.ServerID != id || *publicFile == "" {
				return errors.New("transfer preserves server identity and requires the target release key")
			}
			raw, e := os.ReadFile(*publicFile)
			if e != nil {
				return e
			}
			key, e := agentrelease.PublicKey(raw)
			if e != nil {
				return e
			}
			if e = j.Transfer(*origin, key, *transferGeneration, string(token)); e != nil {
				return e
			}
			cfg, e = j.Config()
			if e != nil {
				return e
			}
		}
		if cfg.ServerID != id || cfg.Origin != *origin {
			return errors.New("state belongs to another management origin or server")
		}
		if *publicFile != "" {
			b, e := os.ReadFile(*publicFile)
			if e != nil {
				return e
			}
			key, e := agentrelease.PublicKey(b)
			if e != nil {
				return e
			}
			cfg.PublicKey = key
		}
		if len(cfg.PublicKey) != ed25519.PublicKeySize {
			return errors.New("a pinned --release-key is required")
		}
		if e = j.SetConfig(cfg); e != nil {
			return e
		}

		c := agent.NewClient(j, nil)
		c.Observe = func(ctx context.Context) api.AgentHeartbeat {
			return preflight(ctx)
		}
		if e = c.Enroll(ctx, string(token)); e != nil {
			return e
		}
		fmt.Println("Agent enrolled.")
		return nil
	}
	if command == "retry" {
		id, e := uuid.Parse(*operation)
		if e != nil {
			return e
		}
		return j.Mutate(id, func(r *agent.Record) {
			if r.Report.State == "needs_attention" {
				r.Report.State = "running"
				r.Report.Note = "Local operator requested reconciliation."
			}
		})
	}
	if command == "update-local" {
		upgrades, err := j.UpgradeViews()
		if err != nil {
			return err
		}
		for _, u := range upgrades {
			if u.Status.Active() {
				return errors.New("complete or recover the active database upgrade before replacing the agent")
			}
		}

		cfg, e := j.Config()
		if e != nil {
			return e
		}
		pending, e := j.Pending()
		if e != nil {
			return e
		}
		for _, r := range pending {
			if r.Command.Kind != "update" {
				return errors.New("resolve the active operation before updating")
			}
		}
		b, e := os.ReadFile(*manifest)
		if e != nil {
			return e
		}
		var release api.AgentRelease
		if json.Unmarshal(b, &release) != nil {
			return errors.New("invalid manifest")
		}
		f, e := os.Open(*artifact)
		if e != nil {
			return e
		}
		defer f.Close()
		return agent.Install(ctx, release, cfg.PublicKey, f, *executable)
	}
	if command == "resolve-run" {
		policy, err := uuid.Parse(*policyID)
		if err != nil {
			return err
		}
		run, err := uuid.Parse(*runID)
		if err != nil {
			return err
		}
		engine := &agent.Engine{Journal: j, Docker: docker, DockerCLI: *dockerCLI, ResticCLI: *resticCLI, BackupRoots: strings.Split(*backupRoots, ",")}
		return engine.ResolveScheduledRun(ctx, policy, run)
	}
	if command != "run" {
		return errors.New("unknown command")
	}
	engine := &agent.Engine{Journal: j, Docker: docker, DockerCLI: *dockerCLI, ACMEEmail: *acmeEmail, ResticCLI: *resticCLI, BackupRoots: strings.Split(*backupRoots, ",")}
	c := agent.NewClient(j, engine)
	collector := &agent.MetricCollector{Journal: j}
	c.Metrics = func(ctx context.Context) api.MetricReport {
		cfg, err := j.Config()
		if err != nil {
			return api.MetricReport{Partial: true}
		}
		return collector.Collect(ctx, docker, cfg.ServerID, *dir)
	}
	c.Observe = func(ctx context.Context) api.AgentHeartbeat {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return preflight(ctx)
	}
	var updated atomic.Bool
	engine.Update = func(ctx context.Context, r api.AgentRelease) error {
		e := agent.RemoteUpdate(ctx, c, r, *executable)
		if e == nil {
			updated.Store(true)
			stop()
		}
		return e
	}
	e = c.Run(ctx)
	if updated.Load() {
		return nil
	}
	if errors.Is(e, context.Canceled) {
		return nil
	}
	return e
}
func privateFile(path string) (*os.File, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("file must be private and regular")
	}
	return f, nil
}
func releaseCommand(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	keyPath := f.String("key", "", "private signing key PEM")
	out := f.String("out", "", "output directory")
	binary := f.String("artifact", "", "agent executable")
	v := f.String("version", "", "release version")
	arch := f.String("arch", "", "linux architecture")
	if e := f.Parse(args); e != nil {
		return e
	}
	if command == "release-keygen" {
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		der, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return e
		}
		p, e := x509.MarshalPKIXPublicKey(pub)
		if e != nil {
			return e
		}
		if e = writeNew(*keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); e != nil {
			return e
		}
		return writeNew(*keyPath+".pub", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: p}), 0644)
	}
	k, e := privateFile(*keyPath)
	if e != nil {
		return e
	}
	defer k.Close()
	raw, e := io.ReadAll(io.LimitReader(k, 4096))
	if e != nil {
		return e
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return errors.New("invalid signing key")
	}
	parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
	if e != nil {
		return e
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return errors.New("Ed25519 key required")
	}
	data, e := os.ReadFile(*binary)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(data)
	r := api.AgentRelease{Version: *v, Protocol: 1, Arch: api.AgentReleaseArch(*arch), Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
	if e = agentrelease.Validate(r, key.Public().(ed25519.PublicKey)); e != nil {
		return e
	}
	if e = os.MkdirAll(*out, 0755); e != nil {
		return e
	}
	if e = writeNew(filepath.Join(*out, r.Sha256), data, 0755); e != nil {
		return e
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	return writeNew(filepath.Join(*out, r.Version+"-"+string(r.Arch)+".json"), b, 0644)
}
func writeNew(path string, b []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	return f.Sync()
}
