package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type hostRequest struct {
	Command     *api.ExecutionCommand `json:"command,omitempty"`
	Acknowledge *api.AgentReport      `json:"acknowledge,omitempty"`
	Membership  *uuid.UUID            `json:"membership,omitempty"`
	Inventory   bool                  `json:"inventory,omitempty"`
	Policy      *uuid.UUID            `json:"policy,omitempty"`
	Upgrade     *uuid.UUID            `json:"upgrade,omitempty"`
	Transfer    bool                  `json:"transfer,omitempty"`
	Barrier     bool                  `json:"barrier,omitempty"`
	Retry       *uuid.UUID            `json:"retry,omitempty"`
	Reject      *uuid.UUID            `json:"reject,omitempty"`
	CrashAt     string                `json:"crashAt,omitempty"`
}
type hostReply struct {
	Report    *api.AgentReport        `json:"report,omitempty"`
	Identity  *hostnetwork.Identity   `json:"identity,omitempty"`
	Inventory *api.CrossHostInventory `json:"inventory,omitempty"`
	Status    *backup.Status          `json:"status,omitempty"`
	Upgrade   *upgrade.Status         `json:"upgrade,omitempty"`
	Barrier   *bool                   `json:"barrier,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

// Each helper runs inside its deployment host's network namespace and talks to
// that namespace's real Docker daemon. Commands still cross the production
// journal, identity, plan, secret and ownership boundaries.
func TestNativeCrossHostHelper(t *testing.T) {
	if os.Getenv("ASTILBA_CROSS_HOST_HELPER") != "1" {
		t.Skip("isolated host process helper")
	}
	state := os.Getenv("ASTILBA_CROSS_HOST_STATE")
	server, err := uuid.Parse(os.Getenv("ASTILBA_CROSS_HOST_SERVER"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = j.Config(); errors.Is(err, os.ErrNotExist) {
		if err = j.SetConfig(Config{ServerID: server, Role: "deployment", MachineID: uuid.New()}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	engine := Engine{Journal: j, Docker: docker, DockerCLI: os.Getenv("ASTILBA_CROSS_HOST_DOCKER_CLI"), ResticCLI: "/opt/astilba-platform-tools/restic", BackupRoots: []string{filepath.Join(os.Getenv("ASTILBA_CROSS_HOST_ROOT"), "backups")}}
	// SQL integration covers authenticated delivery and management encryption;
	// native hosts exercise durability and admission with an opaque transport.
	engine.FetchCheckpoint = func(_ context.Context, d deployment.RecoveryDescriptor) ([]byte, error) {
		return nativeRecoveryEnvelope(d.ParentID), nil
	}
	decoder, encoder := json.NewDecoder(bufio.NewReader(os.Stdin)), json.NewEncoder(os.Stdout)
	for {
		var request hostRequest
		if err = decoder.Decode(&request); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		reply := hostReply{}
		engine.Point = func(point string) {
			if point == request.CrashAt && point != "" {
				os.Exit(77)
			}
		}
		if request.Retry != nil {
			err = j.Retry(*request.Retry)
		} else if request.Reject != nil {
			err = engine.RejectCrossHostEffect(context.Background(), *request.Reject)
		} else if request.Transfer {
			err = j.Transfer("https://management.example.test", make([]byte, 32), 1, strings.Repeat("t", 43))
		} else if request.Barrier {
			blocked, x := j.hasCrossHostBarrier()
			err, reply.Barrier = x, &blocked
		} else if request.Upgrade != nil {
			s, x := j.upgrade(*request.Upgrade)
			err = x
			reply.Upgrade = &s.Status
		} else if request.Policy != nil {
			s, x := j.schedule(*request.Policy)
			err = x
			reply.Status = &backup.Status{PolicyID: s.Installation.Policy.ID, Generation: s.Installation.Policy.Generation, Runs: s.Runs}
		} else if request.Membership != nil {
			identity, x := engine.networkHost().Identity(*request.Membership)
			err = x
			reply.Identity = &identity
		} else if request.Inventory {
			inventory, x := capacityInventory(context.Background(), docker)
			err = x
			reply.Inventory = &inventory
		} else if request.Acknowledge != nil {
			err = j.Acknowledge(request.Acknowledge.Id, request.Acknowledge.Sequence)
		} else if request.Command != nil {
			err = engine.Execute(context.Background(), *request.Command)
			if err == nil {
				record, x := j.Record(request.Command.Id)
				err = x
				reply.Report = &record.Report
			}
		}
		if err != nil {
			reply.Error = err.Error()
		}
		if err = encoder.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
}

type nativePlacementHost struct {
	server                        uuid.UUID
	namespace, state, socket, cli string
	helper                        *exec.Cmd
	input                         io.WriteCloser
	output                        *json.Decoder
	generation, networkGeneration int64
	controllerGeneration          int64
	docker                        *client.Client
	root, executable              string
	ctx                           context.Context
	checkpoints                   map[uuid.UUID]deployment.RecoveryDescriptor
	checkpointReady               map[uuid.UUID]bool
	checkpointEnvironments        map[uuid.UUID]uuid.UUID
}

func nativeRecoveryEnvelope(parent uuid.UUID) []byte {
	return []byte("native-fixture-opaque-metadata:" + parent.String())
}

func (h *nativePlacementHost) start(t *testing.T) {
	t.Helper()
	command := exec.CommandContext(h.ctx, "nsenter", "--net=/run/netns/"+h.namespace, h.executable, "-test.run=^TestNativeCrossHostHelper$")
	command.Env = append(os.Environ(), "ASTILBA_CROSS_HOST_HELPER=1", "ASTILBA_CROSS_HOST_STATE="+h.state, "ASTILBA_CROSS_HOST_SERVER="+h.server.String(), "PLAT_APP_DOCKER_HOST="+h.socket, "ASTILBA_CROSS_HOST_DOCKER_CLI="+h.cli, "ASTILBA_CROSS_HOST_ROOT="+h.root)
	var err error
	h.input, err = command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.output = json.NewDecoder(bufio.NewReader(output))
	log, err := os.OpenFile(filepath.Join(h.root, "helper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = log
	if err = command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	log.Close()
	h.helper = command
}
func (h *nativePlacementHost) stop() {
	if h.helper != nil {
		_ = h.input.Close()
		_ = h.helper.Process.Kill()
		_ = h.helper.Wait()
		h.helper = nil
	}
}
func (h *nativePlacementHost) call(t *testing.T, request hostRequest) hostReply {
	t.Helper()
	if c := request.Command; c != nil && c.ParentId != nil {
		if d, ok := h.checkpoints[*c.ParentId]; ok {
			raw, _ := json.Marshal(d)
			message := json.RawMessage(raw)
			c.RecoveryCheckpoint = &message
			if c.EnvironmentId == uuid.Nil {
				c.EnvironmentId = h.checkpointEnvironments[*c.ParentId]
			}
			body, err := crossHostCommand(*c)
			if err == nil && body.Phase == "reserve" && body.Unit.Plan.Local.Primary && !h.checkpointReady[*c.ParentId] {
				checkpoint := *c
				body.Phase = "checkpoint"
				payload, _ := json.Marshal(body)
				msg := json.RawMessage(payload)
				checkpoint.Id, checkpoint.Generation, checkpoint.Crosshost, checkpoint.PlanHash = uuid.New(), c.Generation-1, &msg, body.Digest()
				h.command(t, checkpoint)
				h.checkpointReady[*c.ParentId] = true
			}
		}
	}
	if err := json.NewEncoder(h.input).Encode(request); err != nil {
		t.Fatal(err)
	}
	var reply hostReply
	if err := h.output.Decode(&reply); err != nil {
		t.Fatalf("host process response: %v", err)
	}
	if reply.Error != "" {
		t.Fatal(reply.Error)
	}
	return reply
}
func (h *nativePlacementHost) command(t *testing.T, c api.ExecutionCommand) api.AgentReport {
	t.Helper()
	reply := h.call(t, hostRequest{Command: &c})
	if reply.Report == nil {
		t.Fatal("missing host receipt")
	}
	if reply.Report.State != "succeeded" {
		for _, args := range [][]string{{"wg", "show"}, {"ip", "route"}, {"nft", "list", "ruleset"}} {
			out, _ := exec.Command("nsenter", append([]string{"--net=/run/netns/" + h.namespace}, args...)...).CombinedOutput()
			t.Logf("host fixture %s: %s", args[0], out)
		}
		t.Fatalf("host phase %s: %s", c.Kind, reply.Report.Note)
	}
	h.call(t, hostRequest{Acknowledge: reply.Report})
	return *reply.Report
}
func (h *nativePlacementHost) visual(t *testing.T, action string, p deployment.Plan, secrets map[string]string) api.AgentReport {
	t.Helper()
	h.generation++
	body := deployment.Payload{Action: action, Plan: p, Secrets: secrets}
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	return h.command(t, api.ExecutionCommand{Id: uuid.New(), ServerId: h.server, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Generation: h.generation, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(5 * time.Minute), Visual: &message})
}
func (h *nativePlacementHost) crossHost(t *testing.T, parent uuid.UUID, hash, phase string, unit deployment.DistributedUnit, secrets map[string]string) api.AgentReport {
	t.Helper()
	return h.command(t, h.crossHostCommand(parent, hash, phase, unit, secrets))
}
func (h *nativePlacementHost) crossHostCommand(parent uuid.UUID, hash, phase string, unit deployment.DistributedUnit, secrets map[string]string) api.ExecutionCommand {
	body := deployment.CrossHostCommand{ParentID: parent, GroupHash: hash, Phase: phase, Unit: unit, Secrets: secrets}
	return h.coordinatedCommand(body)
}
func (h *nativePlacementHost) coordinatedCommand(body deployment.CrossHostCommand) api.ExecutionCommand {
	if h.checkpoints == nil {
		h.checkpoints = map[uuid.UUID]deployment.RecoveryDescriptor{}
		h.checkpointReady = map[uuid.UUID]bool{}
		h.checkpointEnvironments = map[uuid.UUID]uuid.UUID{}
	}
	if body.Phase != "resolve" && body.Phase != "observe" {
		if _, ok := h.checkpoints[body.ParentID]; !ok {
			p := body.Unit.Plan
			participants := []uuid.UUID{p.ServerID}
			for _, peer := range p.Local.Network.Peers {
				participants = append(participants, peer.ServerID)
			}
			sort.Slice(participants, func(i, j int) bool { return participants[i].String() < participants[j].String() })
			id := uuid.NewSHA1(body.ParentID, []byte("native-recovery-checkpoint"))
			envelope := nativeRecoveryEnvelope(body.ParentID)
			h.checkpoints[body.ParentID] = deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.NewSHA1(p.EnvironmentID, []byte("native-workspace")), ParentID: body.ParentID, RootHash: body.GroupHash, GroupID: p.Local.GroupID, GroupHash: body.GroupHash, BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: participants}
			h.checkpointEnvironments[body.ParentID] = p.EnvironmentID
			if p.Local.Primary && body.Phase == "reserve" {
				h.generation++ // The primary metadata ACK precedes reserve.
			}
		}
	}
	h.generation++
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: &body.ParentID, ServerId: h.server, EnvironmentId: body.Unit.Plan.EnvironmentID, ResourceId: body.Unit.Plan.UnitID, Generation: h.generation, Kind: "crosshost", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(5 * time.Minute), Crosshost: &message}
	if d, ok := h.checkpoints[body.ParentID]; ok {
		raw, _ := json.Marshal(d)
		checkpoint := json.RawMessage(raw)
		c.RecoveryCheckpoint = &checkpoint
	}
	if h.controllerGeneration > 0 {
		c.ControllerGeneration = &h.controllerGeneration
	}
	return c
}
func (h *nativePlacementHost) backupCommand(body backup.Command, parent *uuid.UUID) api.ExecutionCommand {
	h.generation++
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	p := body.Installation.Policy
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: parent, ServerId: h.server, EnvironmentId: p.EnvironmentID, ResourceId: p.ID, Generation: h.generation, Kind: "backup", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(5 * time.Minute), Backup: &message}
	if parent != nil {
		if d, ok := h.checkpoints[*parent]; ok {
			raw, _ := json.Marshal(d)
			checkpoint := json.RawMessage(raw)
			c.RecoveryCheckpoint = &checkpoint
		}
	}
	return c
}
func (h *nativePlacementHost) network(t *testing.T, parent *uuid.UUID, m hostnetwork.Manifest, restrict bool) {
	t.Helper()
	h.networkGeneration++
	action := "configure"
	if restrict {
		action = "restrict"
	}
	body := hostnetwork.Command{Action: action, NetworkID: m.NetworkID, MembershipID: m.MembershipID, Manifest: &m}
	if restrict && parent == nil {
		prior, err := (hostnetwork.Host{Dir: h.state}).Snapshot(m.NetworkID)
		if err != nil {
			t.Fatal(err)
		}
		body.Baseline = &prior.Manifest
	}
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: parent, ServerId: h.server, Generation: h.networkGeneration, Kind: "network", Action: "inspect", ResourceId: m.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(5 * time.Minute), Network: &message}
	if h.controllerGeneration > 0 {
		c.ControllerGeneration = &h.controllerGeneration
	}
	h.command(t, c)
}

func nativePlacementHosts(t *testing.T) (context.Context, []*nativePlacementHost) {
	t.Helper()
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires explicitly disposable Ubuntu native host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("owned disposable Linux host required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	root, err := os.MkdirTemp("/opt", "astilba-cross-host-")
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = os.WriteFile(filepath.Join(root, "owner"), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	owned := func() bool {
		marker, err := os.ReadFile(filepath.Join(root, "owner"))
		if err != nil || string(marker) != owner {
			t.Error("native fixture ownership changed; cleanup refused", err)
			return false
		}
		return true
	}
	t.Cleanup(func() {
		if owned() {
			if err := os.RemoveAll(root); err != nil {
				t.Error(err)
			}
		}
	})
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("native host fixture %s: %s (%v)", args[0], out, err)
		}
		return out
	}
	bridge := "af" + strings.ReplaceAll(owner, "-", "")[:10]
	run("ip", "link", "add", bridge, "type", "bridge")
	t.Cleanup(func() {
		if owned() {
			_ = exec.Command("ip", "link", "delete", bridge).Run()
		}
	})
	run("ip", "address", "add", "10.219.0.254/24", "dev", bridge)
	run("ip", "link", "set", bridge, "up")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hosts := []*nativePlacementHost{}
	for index := 0; index < 2; index++ {
		hostRoot := filepath.Join(root, fmt.Sprintf("host%d", index))
		if err = os.Mkdir(hostRoot, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.Mkdir(filepath.Join(hostRoot, "backups"), 0700); err != nil {
			t.Fatal(err)
		}
		namespace := "astilba-cross-" + strings.ReplaceAll(owner, "-", "")[:8] + fmt.Sprint(index)
		run("ip", "netns", "add", namespace)
		t.Cleanup(func() {
			if owned() {
				_ = exec.Command("ip", "netns", "delete", namespace).Run()
			}
		})
		hostEnd, peerEnd := "ah"+fmt.Sprint(index)+strings.ReplaceAll(owner, "-", "")[:7], "uplink"
		run("ip", "link", "add", hostEnd, "type", "veth", "peer", "name", peerEnd)
		run("ip", "link", "set", peerEnd, "netns", namespace)
		run("ip", "link", "set", hostEnd, "master", bridge)
		run("ip", "link", "set", hostEnd, "up")
		run("ip", "-n", namespace, "address", "add", fmt.Sprintf("10.219.0.%d/24", index+1), "dev", peerEnd)
		run("ip", "-n", namespace, "link", "set", peerEnd, "up")
		run("ip", "-n", namespace, "link", "set", "lo", "up")
		run("ip", "-n", namespace, "route", "add", "default", "via", "10.219.0.254")
		socket := "unix://" + filepath.Join(hostRoot, "docker.sock")
		daemon := exec.CommandContext(ctx, "nsenter", "--net=/run/netns/"+namespace, "/opt/astilba-plat4/bin/dockerd", "--host="+socket, "--data-root="+filepath.Join(hostRoot, "docker"), "--exec-root="+filepath.Join(hostRoot, "exec"), "--pidfile="+filepath.Join(hostRoot, "docker.pid"), "--group=root", "--exec-opt", "native.cgroupdriver=systemd")
		daemon.Env = append(os.Environ(), "PATH=/opt/astilba-plat4/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
		log, err := os.OpenFile(filepath.Join(hostRoot, "daemon.log"), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		daemon.Stdout = log
		daemon.Stderr = log
		if err = daemon.Start(); err != nil {
			t.Fatal(err)
		}
		log.Close()
		t.Cleanup(func() {
			_ = daemon.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { _ = daemon.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				_ = daemon.Process.Kill()
				<-done
			}
		})
		docker, err := client.New(client.WithHost(socket))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = docker.Close() })
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err = docker.Info(ctx, client.InfoOptions{}); err == nil {
				break
			}
			if time.Now().After(deadline) {
				log, _ := os.ReadFile(filepath.Join(hostRoot, "daemon.log"))
				t.Fatalf("owned fixture daemon did not start: %v\n%s", err, log)
			}
			time.Sleep(100 * time.Millisecond)
		}
		state := filepath.Join(hostRoot, "state")
		if err = os.Mkdir(state, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(state, "network-enabled"), []byte("astilba-managed-network-v1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(hostRoot, "config")
		if err = os.Mkdir(config, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600); err != nil {
			t.Fatal(err)
		}
		cli := filepath.Join(hostRoot, "docker-cli")
		if err = os.WriteFile(cli, []byte("#!/bin/sh\nexport DOCKER_CONFIG='"+config+"'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		host := &nativePlacementHost{ctx: ctx, server: uuid.New(), namespace: namespace, state: state, socket: socket, cli: cli, docker: docker, root: hostRoot, executable: executable}
		host.start(t)
		t.Cleanup(host.stop)
		hosts = append(hosts, host)
	}
	// Model direct server transport separately from the fixture VM's access
	// bridge, whose Docker policy can filter bridged traffic between namespaces.
	ends := []string{"aw0" + strings.ReplaceAll(owner, "-", "")[:8], "aw1" + strings.ReplaceAll(owner, "-", "")[:8]}
	run("ip", "link", "add", ends[0], "type", "veth", "peer", "name", ends[1])
	for index, host := range hosts {
		run("ip", "link", "set", ends[index], "netns", host.namespace)
		run("ip", "-n", host.namespace, "link", "set", ends[index], "name", "transport")
		run("ip", "-n", host.namespace, "address", "add", fmt.Sprintf("10.218.0.%d/30", index+1), "dev", "transport")
		run("ip", "-n", host.namespace, "link", "set", "transport", "up")
	}
	run("nsenter", "--net=/run/netns/"+hosts[1].namespace, "ping", "-c", "1", "-W", "2", "10.218.0.1")
	return ctx, hosts
}

func nativePlacementRequest(t *testing.T, address, domain string) (int, string) {
	t.Helper()
	request, err := http.NewRequest("GET", "http://"+address+"/cgi-bin/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = domain
	client := http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func TestNativeCrossHostDeployment(t *testing.T) {
	ctx, hosts := nativePlacementHosts(t)
	primary, remote := hosts[0], hosts[1]
	source, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, image := range []string{execution.TestImage, domain.Postgres17Image, domain.PostgresImage, deployment.ProxyImage} {
		if _, err = source.ImageInspect(ctx, image); err != nil {
			if err = (&Engine{Docker: source}).pullImage(ctx, image); err != nil {
				t.Fatal(err)
			}
		}
		for _, host := range hosts {
			archive, err := source.ImageSave(ctx, []string{image})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := host.docker.ImageLoad(ctx, archive)
			if err != nil {
				archive.Close()
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(loaded)
			loaded.Close()
			archive.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err = host.docker.ImageInspect(ctx, image); err != nil {
				// Docker's OCI export retains the content digest but drops its
				// repository name. Restore that name without changing the content.
				named, parseErr := reference.ParseNormalizedNamed(image)
				digested, ok := named.(reference.Digested)
				if parseErr != nil || !ok {
					t.Fatal("fixture requires pinned registry images")
				}
				alias, aliasErr := exec.CommandContext(ctx, "/opt/astilba-plat4/bin/ctr", "--address", filepath.Join(host.root, "exec/containerd/containerd.sock"), "--namespace", "moby", "images", "tag", "moby-dangling@"+digested.Digest().String(), named.Name()+"@"+digested.Digest().String()).CombinedOutput()
				if _, err = host.docker.ImageInspect(ctx, image); aliasErr != nil || err != nil {
					t.Fatalf("fixture pinned image %s: %s %s (%v, %v)", image, body, alias, aliasErr, err)
				}
			}
		}
	}
	app, pg, volume, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	files, fileVolume := uuid.New(), uuid.New()
	webImage, pgImage := execution.TestImage, domain.Postgres17Image
	script := `mkdir -p /tmp/www/cgi-bin
cat > /tmp/www/cgi-bin/health <<'CGI'
#!/bin/sh
if ! nc -z -w 2 "$DB_HOST" "$DB_PORT"; then
  printf 'Status: 503\r\nContent-Type: text/plain\r\n\r\nDatabase unreachable'
  exit
fi
printf 'Content-Type: text/plain\r\n\r\n'
hostname
CGI
chmod +x /tmp/www/cgi-bin/health
exec httpd -f -p 8080 -h /tmp/www`
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{
		{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &webImage}, DependsOn: []uuid.UUID{pg}, Command: []string{"sh", "-c", script}, Health: &domain.HealthCheck{Port: 8080, Path: "/cgi-bin/health"}},
		{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgImage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(pgImage)}}},
		{ID: files, Name: "Files", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &webImage}, Command: []string{"sh", "-c", "test -f /files/value || echo retained-file-data > /files/value; exec sleep 10000"}, Mounts: []domain.VolumeMount{{VolumeID: fileVolume, Target: "/files"}}},
	}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}, {ID: fileVolume, Name: "Files"}}, Bindings: []domain.Binding{
		{ID: uuid.New(), SourceID: pg, TargetID: app, Output: "HOST", Variable: "DB_HOST"},
		{ID: uuid.New(), SourceID: pg, TargetID: app, Output: "PORT", Variable: "DB_PORT"},
	}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "cross-host.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	secrets := map[string]string{secret.String(): "disposable-cross-host-canary"}
	initial := deployment.Plan{ID: uuid.New(), ServerID: primary.server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, HostChanges: true, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
	if err := initial.Validate(false); err != nil {
		t.Fatal(err)
	}
	r := primary.visual(t, "resolve", initial, nil)
	var observed deployment.Observation
	if err = json.Unmarshal(*r.Observation, &observed); err != nil {
		t.Fatal(err)
	}
	initial.Images, initial.ExpectedState = observed.Images, observed.StateHash
	initial = initial.WithHash()
	primary.visual(t, "deploy", initial, secrets)
	if status, body := nativePlacementRequest(t, "10.219.0.1", "cross-host.example.test"); status != 200 {
		t.Fatalf("initial stack: %d %s", status, body)
	}
	network := uuid.New()
	manifests := []hostnetwork.Manifest{}
	for index, host := range hosts {
		membership := uuid.New()
		identity := host.call(t, hostRequest{Membership: &membership}).Identity
		if identity == nil {
			t.Fatal("missing host-local network identity")
		}
		manifests = append(manifests, hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: membership, ServerID: host.server, Generation: 1, Prefix: "10.220.0.0/24", Address: fmt.Sprintf("10.220.0.%d", index+1), PublicKey: identity.PublicKey, ListenPort: 51820, Peers: []hostnetwork.Peer{}, Exports: []hostnetwork.Endpoint{}, Egress: []hostnetwork.Egress{}})
	}
	for index := range hosts {
		other := 1 - index
		manifests[index].Peers = []hostnetwork.Peer{{ServerID: hosts[other].server, MembershipID: manifests[other].MembershipID, Address: manifests[other].Address, PublicKey: manifests[other].PublicKey, Endpoint: fmt.Sprintf("10.218.0.%d:51820", other+1)}}
		hosts[index].network(t, nil, manifests[index], false)
	}
	group, parent := uuid.New(), uuid.New()
	primaryInstance, remoteInstance, remoteUnit := uuid.New(), uuid.New(), uuid.New()
	dbEndpoint, webEndpoint := uuid.New(), uuid.New()
	manifests[0].Generation = 2
	manifests[1].Generation = 2
	manifests[0].Exports = []hostnetwork.Endpoint{{ID: dbEndpoint, UnitID: initial.UnitID, ResourceID: pg, InstanceID: pg, Port: 20001, ContainerPort: 5432, Sources: []hostnetwork.Source{{ServerID: remote.server, UnitID: remoteUnit}}}}
	manifests[0].Egress = []hostnetwork.Egress{{Ingress: true, UnitID: initial.UnitID, ServerID: remote.server, EndpointID: webEndpoint, Port: 20002}}
	manifests[1].Exports = []hostnetwork.Endpoint{{ID: webEndpoint, UnitID: remoteUnit, ResourceID: app, InstanceID: remoteInstance, Port: 20002, ContainerPort: 8080, Sources: []hostnetwork.Source{{ServerID: primary.server, UnitID: initial.UnitID}}}}
	manifests[1].Egress = []hostnetwork.Egress{{UnitID: remoteUnit, ServerID: primary.server, EndpointID: dbEndpoint, Port: 20001}}
	spec.Applications = append([]domain.Application(nil), spec.Applications...)
	spec.Applications[0].Replicas = 2
	spec.Placement = &domain.Placement{NetworkID: network, IngressServerID: primary.server, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 200, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: primary.server, Count: 1}, {ServerID: remote.server, Count: 1}}}}}
	backends := []deployment.PrivateEndpoint{
		{ServerID: primary.server, UnitID: initial.UnitID, ResourceID: app, InstanceID: primaryInstance, Address: deployment.ServiceName(primaryInstance), Port: 8080, ContainerPort: 8080},
		{ServerID: remote.server, UnitID: remoteUnit, ResourceID: app, InstanceID: remoteInstance, Address: manifests[1].Address, Port: 20002, ContainerPort: 8080},
	}
	units := []deployment.DistributedUnit{}
	for index, host := range hosts {
		local := &deployment.LocalPlacement{GroupID: group, Primary: index == 0, Network: manifests[index], Instances: map[string][]uuid.UUID{}, PriorInstances: map[string][]uuid.UUID{}, Requests: map[string]deployment.ResourceRequest{app.String(): {CPUMillis: 200, MemoryMiB: 128}}, Outputs: map[string]deployment.PrivateEndpoint{}, Backends: map[string][]deployment.PrivateEndpoint{}}
		unitID := remoteUnit
		previous := deployment.Plan{}
		if index == 0 {
			unitID, previous = initial.UnitID, initial
			local.Instances = map[string][]uuid.UUID{app.String(): {primaryInstance}, pg.String(): {pg}, files.String(): {files}}
			local.PriorInstances = map[string][]uuid.UUID{app.String(): {app}, pg.String(): {pg}, files.String(): {files}}
			local.Backends[app.String()] = backends
		} else {
			local.Instances[app.String()] = []uuid.UUID{remoteInstance}
			local.Outputs[pg.String()+":5432"] = deployment.PrivateEndpoint{ServerID: primary.server, UnitID: initial.UnitID, ResourceID: pg, InstanceID: pg, Address: manifests[0].Address, Port: 20001, ContainerPort: 5432}
		}
		p := deployment.Plan{ID: uuid.New(), ServerID: host.server, EnvironmentID: initial.EnvironmentID, UnitID: unitID, RevisionID: uuid.NewSHA1(group, []byte("revision")), Recipe: deployment.CrossHostRecipe, Specification: spec, Local: local, Images: map[string]string{}, ExpectedAppliedID: &initial.RevisionID, ExpiresAt: time.Now().Add(time.Hour), ReleaseOptions: &deployment.ReleaseOptions{DrainSeconds: 1, CommandTimeoutSeconds: 60}}
		if index == 1 {
			p.ExpectedAppliedID = nil
		}
		inventory := host.call(t, hostRequest{Inventory: true}).Inventory
		if inventory == nil {
			t.Fatal("missing capacity inventory")
		}
		u := deployment.DistributedUnit{Plan: p.WithHash(), Previous: previous, CPUMillis: 200, MemoryMiB: 128, InventoryHash: inventory.UnboundedInventoryHash}
		r := host.crossHost(t, parent, strings.Repeat("0", 64), "resolve", u, nil)
		var receipt deployment.CrossHostReceipt
		if err = json.Unmarshal(*r.Crosshost, &receipt); err != nil {
			t.Fatal(err)
		}
		u.Plan.Images = maps.Clone(receipt.Observation.Images)
		u.Plan.ExpectedState = receipt.Observation.StateHash
		u.Plan = u.Plan.WithHash()
		units = append(units, u)
	}
	groupHash := deployment.Hash(units)
	sourceGroup := deployment.Plan{ID: group, ServerID: primary.server, EnvironmentID: initial.EnvironmentID, UnitID: initial.UnitID, RevisionID: units[0].Plan.RevisionID, Recipe: deployment.CrossHostRecipe, Specification: spec, Images: map[string]string{}, Distributed: &deployment.DistributedPlan{NetworkID: network, NetworkGeneration: 2, Units: units}, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
	if err = sourceGroup.Validate(true); err != nil {
		t.Fatal("invalid fixture source group", err)
	}
	upgradePlan := upgrade.Plan{Group: &sourceGroup, ID: uuid.New(), Recipe: upgrade.Recipe, PolicyID: uuid.New(), ResourceID: pg, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), RetainDays: 7, ExpiresAt: time.Now().Add(time.Hour), Source: sourceGroup.PrimaryPlan()}
	if err = upgradePlan.Validate(); err != nil {
		t.Fatal(err)
	}
	if err = upgradePlan.TargetGroup().Validate(true); err != nil {
		t.Fatal("upgrade did not preserve the reviewed per-host inventory", err)
	}
	if sourceGroup.WithHash().Hash != sourceGroup.Hash || upgradePlan.Target().Local.GroupID != upgradePlan.TargetPlanID {
		t.Fatal("upgrade mutated its source group or lost the target group identity")
	}
	for index, host := range hosts {
		host.crossHost(t, parent, groupHash, "reserve", units[index], secretsForPlan(units[index].Plan, secrets))
		host.stop()
		host.start(t)
		host.network(t, &parent, manifests[index], false)
	}
	t.Run("expired_continuation_checkpoint", func(t *testing.T) {
		d := primary.checkpoints[parent]
		d.Sequence++
		d.PreviousHash = d.Digest
		d.CheckpointID = uuid.New()
		primary.checkpoints[parent] = d
		c := primary.coordinatedCommand(deployment.CrossHostCommand{ParentID: parent, GroupHash: groupHash, Phase: "checkpoint", Unit: units[0]})
		c.Deadline = time.Now().Add(-time.Hour)
		primary.command(t, c)
		if held := primary.call(t, hostRequest{Barrier: true}).Barrier; held == nil || !*held {
			t.Fatal("expired metadata delivery changed the resource reservation")
		}
	})
	for index, host := range hosts {
		host.crossHost(t, parent, groupHash, "prepare", units[index], secretsForPlan(units[index].Plan, secrets))
	}
	primary.crossHost(t, parent, groupHash, "probe", units[0], secrets)
	publication := primary.crossHostCommand(parent, groupHash, "publish", units[0], secrets)
	if err = json.NewEncoder(primary.input).Encode(hostRequest{Command: &publication, CrashAt: "route-published"}); err != nil {
		t.Fatal(err)
	}
	var lost hostReply
	if err = primary.output.Decode(&lost); err == nil {
		t.Fatal("publication crash did not interrupt the process")
	}
	primary.stop()
	primary.start(t)
	primary.command(t, publication)
	seen := map[string]bool{}
	for index := 0; index < 12; index++ {
		status, body := nativePlacementRequest(t, "10.219.0.1", "cross-host.example.test")
		if status != 200 {
			t.Fatalf("cross-host database binding: %d %s", status, body)
		}
		seen[strings.TrimSpace(body)] = true
	}
	if len(seen) != 2 {
		t.Fatalf("ingress did not reach both hosts: %v", seen)
	}
	completed := make([]deployment.Observation, len(hosts))
	currentGroup := sourceGroup
	var fileInstallation backup.Installation
	var fileSnapshot string
	for index, host := range hosts {
		host.crossHost(t, parent, groupHash, "retire", units[index], secretsForPlan(units[index].Plan, secrets))
		r := host.crossHost(t, parent, groupHash, "complete", units[index], secretsForPlan(units[index].Plan, secrets))
		var receipt deployment.CrossHostReceipt
		if err = json.Unmarshal(*r.Crosshost, &receipt); err != nil {
			t.Fatal(err)
		}
		completed[index] = receipt.Observation
	}
	if v, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(app), client.ContainerInspectOptions{}); err != nil || v.Container.State.Running {
		t.Fatal("the previous application pool was not stopped and retained", err)
	}
	if v, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(pg), client.ContainerInspectOptions{}); err != nil || !v.Container.State.Running {
		t.Fatal("primary PostgreSQL was not retained", err)
	}
	t.Run("candidate_retains_unchanged_primary_services", func(t *testing.T) {
		before := map[uuid.UUID]string{}
		for _, id := range []uuid.UUID{pg, files} {
			v, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			before[id] = v.Container.ID
		}
		raw, _ := json.Marshal(units[0])
		var candidate deployment.DistributedUnit
		if err := json.Unmarshal(raw, &candidate); err != nil {
			t.Fatal(err)
		}
		candidate.Previous = units[0].Plan
		candidate.Plan.ID, candidate.Plan.Local.GroupID = uuid.New(), uuid.New()
		options := *candidate.Plan.ReleaseOptions
		options.Command = []string{"sh", "-c", "exit 0"}
		candidate.Plan.ReleaseOptions = &options
		candidate.Plan.Release = &deployment.Release{ApplicationID: app, PreviousPlanID: sourceGroup.ID, Options: options}
		nextInstance := uuid.New()
		candidate.Plan.Local.Instances[app.String()] = []uuid.UUID{nextInstance}
		candidate.Plan.Local.PriorInstances = maps.Clone(units[0].Plan.Local.Instances)
		endpoints := candidate.Plan.Local.Backends[app.String()]
		endpoints[0].InstanceID, endpoints[0].Address = nextInstance, deployment.ServiceName(nextInstance)
		candidate.Plan.Local.Backends[app.String()] = endpoints
		candidate.Plan = candidate.Plan.WithHash()
		candidate.InventoryHash = primary.call(t, hostRequest{Inventory: true}).Inventory.UnboundedInventoryHash
		parent := uuid.New()
		report := primary.crossHost(t, parent, strings.Repeat("1", 64), "resolve", candidate, nil)
		var receipt deployment.CrossHostReceipt
		if err := json.Unmarshal(*report.Crosshost, &receipt); err != nil {
			t.Fatal(err)
		}
		candidate.Plan.ExpectedState = receipt.Observation.StateHash
		candidate.Plan = candidate.Plan.WithHash()
		for _, phase := range []string{"reserve", "prepare"} {
			primary.crossHost(t, parent, strings.Repeat("1", 64), phase, candidate, secretsForPlan(candidate.Plan, secrets))
		}
		for id, containerID := range before {
			v, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil || v.Container.ID != containerID || !v.Container.State.Running {
				t.Fatal("candidate preparation interrupted an unchanged primary service", err)
			}
		}
		hook := primary.crossHostCommand(parent, strings.Repeat("1", 64), "hook", candidate, secretsForPlan(candidate.Plan, secrets))
		if err := json.NewEncoder(primary.input).Encode(hostRequest{Command: &hook, CrashAt: "release-command-intent"}); err != nil {
			t.Fatal(err)
		}
		if err := primary.output.Decode(&lost); err == nil {
			t.Fatal("release-command intent crash did not interrupt the helper")
		}
		primary.stop()
		primary.start(t)
		reply := primary.call(t, hostRequest{Command: &hook})
		if reply.Report == nil || reply.Report.State != "needs_attention" {
			t.Fatal("uncertain release-command start was replayed")
		}
		primary.call(t, hostRequest{Reject: &hook.Id})
		reply = primary.call(t, hostRequest{Command: &hook})
		if reply.Report == nil || reply.Report.State != "failed" {
			t.Fatal("local release-command rejection was not recorded")
		}
		primary.call(t, hostRequest{Acknowledge: reply.Report})
		primary.crossHost(t, parent, strings.Repeat("1", 64), "abort", candidate, secretsForPlan(candidate.Plan, secrets))
	})
	for _, interrupted := range []bool{true, false} {
		t.Run(fmt.Sprintf("maintenance_pause_restart_and_resume_interrupted_%t", interrupted), func(t *testing.T) {
			parent, policy, run := uuid.New(), uuid.New(), uuid.New()
			p := backup.Policy{ID: policy, Name: "Coordinated files", WorkspaceID: uuid.New(), EnvironmentID: initial.EnvironmentID, ServerID: primary.server, DestinationID: uuid.New(), ResourceID: fileVolume, Generation: 1, Method: backup.QuiescedFiles, Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Coordinated: true, Plan: units[0].Plan}
			installation := backup.Installation{Policy: p, Destination: backup.Destination{ID: p.DestinationID, Name: "Fixture", Kind: "filesystem", Root: filepath.Join(primary.root, "backups")}, Access: backup.Access{Password: "disposable-repository-canary"}}
			fileInstallation = installation
			effect := backup.Command{Action: "backup", Installation: installation, RunID: run}
			primary.command(t, primary.backupCommand(backup.Command{Action: "install", Installation: installation}, nil))
			intents := make([]deployment.Maintenance, len(hosts))
			for index := range hosts {
				intents[index] = deployment.Maintenance{Kind: "backup", ResourceID: policy, RunID: run, EffectHash: effect.Digest(), ExpectedState: completed[index].StateHash}
			}
			command := func(index int, phase string) api.ExecutionCommand {
				body := deployment.CrossHostCommand{ParentID: parent, GroupHash: groupHash, Phase: phase, Unit: units[index], Secrets: secretsForPlan(units[index].Plan, secrets), Maintenance: &intents[index]}
				if phase == "effect" {
					body.EffectAction = "backup"
				}
				return hosts[index].coordinatedCommand(body)
			}
			for index, host := range hosts {
				host.command(t, command(index, "reserve"))
			}
			pause := command(0, "pause")
			if err = json.NewEncoder(primary.input).Encode(hostRequest{Command: &pause, CrashAt: "maintenance-writer-stopped"}); err != nil {
				t.Fatal(err)
			}
			if err = primary.output.Decode(&lost); err == nil {
				t.Fatal("writer-stop crash did not interrupt the process")
			}
			primary.stop()
			primary.start(t)
			primary.command(t, pause)
			remote.command(t, command(1, "pause"))
			for index, host := range hosts {
				id := units[index].Plan.InstanceIDs(app)[0]
				v, err := host.docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
				if err != nil || v.Container.State.Running {
					t.Fatal("writer pause was not durable", err)
				}
			}
			if v, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(pg), client.ContainerInspectOptions{}); err != nil || !v.Container.State.Running {
				t.Fatal("writer coordination stopped PostgreSQL before the primary data effect", err)
			}
			primary.command(t, command(0, "effect"))
			leaf := primary.backupCommand(effect, &parent)
			if interrupted {
				if err := json.NewEncoder(primary.input).Encode(hostRequest{Command: &leaf, CrashAt: "backup-run-recorded"}); err != nil {
					t.Fatal(err)
				}
				if err := primary.output.Decode(&lost); err == nil {
					t.Fatal("backup crash did not interrupt the helper")
				}
				primary.stop()
				primary.start(t)
				reply := primary.call(t, hostRequest{Command: &leaf})
				if reply.Report == nil || reply.Report.State != "needs_attention" {
					t.Fatal("unknown data effect was replayed after restart")
				}
				// Restricting permissions remains deliverable through both the
				// unacknowledged leaf and its uncertain schedule, without releasing it.
				restriction := units[0].Plan.Local.Network
				restriction.Generation++
				primary.network(t, nil, restriction, true)
				primary.call(t, hostRequest{Reject: &leaf.Id})
				reply = primary.call(t, hostRequest{Command: &leaf})
				if reply.Report == nil || reply.Report.State != "failed" {
					t.Fatal("inspected data outcome was not recorded as failed")
				}
				primary.call(t, hostRequest{Acknowledge: reply.Report})
				if barrier := primary.call(t, hostRequest{Barrier: true}).Barrier; barrier == nil || !*barrier {
					t.Fatal("local effect rejection released the parent reservation")
				}
			} else {
				primary.command(t, leaf)
			}
			status := primary.call(t, hostRequest{Policy: &policy}).Status
			if status == nil || len(status.Runs) != 1 || status.Runs[0].ID != run || !interrupted && (status.Runs[0].State != "succeeded" || !backup.SnapshotID(status.Runs[0].Snapshot)) || interrupted && (status.Runs[0].State != "failed" || status.Runs[0].Snapshot != "") {
				t.Fatalf("coordinated file snapshot was not confirmed: %+v", status)
			}
			if !interrupted {
				fileSnapshot = status.Runs[0].Snapshot
			}
			for index, host := range hosts {
				host.command(t, command(index, "resume-source"))
				r := host.command(t, command(index, "complete"))
				var receipt deployment.CrossHostReceipt
				if err = json.Unmarshal(*r.Crosshost, &receipt); err != nil {
					t.Fatal(err)
				}
				completed[index] = receipt.Observation
			}
			nativeWaitPlacementRecovery(t)
		})
	}
	t.Run("coordinated_postgres_major_upgrade", func(t *testing.T) {
		parent := uuid.New()
		policy := backup.Policy{ID: upgradePlan.PolicyID, Name: "Upgrade recovery", WorkspaceID: uuid.New(), EnvironmentID: initial.EnvironmentID, ServerID: primary.server, DestinationID: uuid.New(), ResourceID: pg, Generation: 1, Method: "postgres-logical", Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Plan: upgradePlan.Source}
		installation := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Recovery", Kind: "filesystem", Root: filepath.Join(primary.root, "backups")}, Access: backup.Access{Password: "disposable-upgrade-repository-canary"}}
		primary.command(t, primary.backupCommand(backup.Command{Action: "install", Installation: installation}, nil))
		pgEngine := &Engine{Docker: primary.docker, DockerCLI: primary.cli}
		pgApp := sourceGroup.Specification.Applications[1]
		if _, err := pgEngine.pgQuery(ctx, deployment.UnitName(pg), pgApp, "CREATE TABLE cross_host_marker(value text); INSERT INTO cross_host_marker VALUES('retained database data')"); err != nil {
			t.Fatal(err)
		}
		effect := upgrade.Command{Action: "", Plan: upgradePlan, Installation: installation, Secrets: secrets}
		target := upgradePlan.TargetGroup()
		intents := make([]deployment.Maintenance, len(hosts))
		for index := range hosts {
			projection := target.Distributed.Units[index].Plan
			intents[index] = deployment.Maintenance{Kind: "upgrade", ResourceID: upgradePlan.ID, EffectHash: effect.Digest(), ExpectedState: completed[index].StateHash, Target: &projection}
		}
		command := func(index int, phase, action string, useTarget bool) api.ExecutionCommand {
			return hosts[index].coordinatedCommand(deployment.CrossHostCommand{ParentID: parent, GroupHash: deployment.Hash(upgradePlan), Phase: phase, Unit: units[index], Secrets: secretsForPlan(units[index].Plan, secrets), Maintenance: &intents[index], EffectAction: action, UseTarget: useTarget})
		}
		upgradeCommand := func(action string) api.ExecutionCommand {
			primary.generation++
			body := effect
			body.Action = action
			raw, _ := json.Marshal(body)
			message := json.RawMessage(raw)
			return api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: primary.server, EnvironmentId: initial.EnvironmentID, ResourceId: upgradePlan.ID, Kind: "upgrade", Action: "inspect", Generation: primary.generation, Deadline: time.Now().Add(5 * time.Minute), PlanHash: body.Digest(), Upgrade: &message}
		}
		for index, host := range hosts {
			host.command(t, command(index, "reserve", "", false))
		}
		for index, host := range hosts {
			host.command(t, command(index, "pause", "", false))
		}
		primary.command(t, command(0, "effect", "prepare", false))
		primary.command(t, upgradeCommand("prepare"))
		for _, host := range hosts {
			host.stop()
			host.start(t)
		}
		status := primary.call(t, hostRequest{Upgrade: &upgradePlan.ID}).Upgrade
		if status == nil || status.Phase != "ready" || status.WritesPossible || !backup.SnapshotID(status.Snapshot) {
			t.Fatalf("upgrade review checkpoint was not durable: %+v", status)
		}
		for index, host := range hosts {
			for _, id := range units[index].Plan.InstanceIDs(app) {
				v, err := host.docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
				if err != nil || v.Container.State.Running {
					t.Fatal("a writer restarted before owner-reviewed database cutover", err)
				}
			}
		}
		primary.command(t, command(0, "effect", "cutover", false))
		primary.command(t, upgradeCommand("cutover"))
		for index, host := range hosts {
			host.command(t, command(index, "resume-target", "", true))
			r := host.command(t, command(index, "complete", "", true))
			var receipt deployment.CrossHostReceipt
			if err := json.Unmarshal(*r.Crosshost, &receipt); err != nil {
				t.Fatal(err)
			}
			completed[index] = receipt.Observation
		}
		rows, err := pgEngine.pgQuery(ctx, deployment.UnitName(pg), pgApp, "SELECT count(*) FROM cross_host_marker WHERE value='retained database data'")
		if err != nil || strings.TrimSpace(string(rows)) != "1" {
			t.Fatal("major upgrade did not preserve application data", err)
		}
		if _, err = primary.docker.VolumeInspect(ctx, deployment.VolumeName(volume), client.VolumeInspectOptions{}); err != nil {
			t.Fatal("major upgrade removed the retained source volume", err)
		}
		if status, body := nativePlacementRequest(t, "10.219.0.1", "cross-host.example.test"); status != 200 {
			t.Fatalf("cross-host application did not recover on PostgreSQL18: %d %s", status, body)
		}
		currentGroup = upgradePlan.TargetGroup()
	})
	for _, method := range []string{backup.QuiescedFiles, "postgres-logical"} {
		t.Run("coordinated_"+method+"_restore_cutover", func(t *testing.T) {
			installation := fileInstallation
			installation.Policy.Generation++
			consumer, oldVolume, snapshot := files, fileVolume, fileSnapshot
			if method == "postgres-logical" {
				consumer = pg
				oldVolume = currentGroup.Specification.Applications[1].Mounts[0].VolumeID
				policy := backup.Policy{ID: uuid.New(), Name: "Restore recovery", WorkspaceID: uuid.New(), EnvironmentID: initial.EnvironmentID, ServerID: primary.server, DestinationID: uuid.New(), ResourceID: pg, Generation: 1, Method: method, Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 7, Enabled: true}
				installation = backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Restore recovery", Kind: "filesystem", Root: filepath.Join(primary.root, "backups")}, Access: backup.Access{Password: "disposable-restore-repository-canary"}}
			}
			installation.Policy.Plan = currentGroup.PrimaryPlan()
			primary.command(t, primary.backupCommand(backup.Command{Action: "install", Installation: installation}, nil))
			policyID := installation.Policy.ID
			if method == "postgres-logical" {
				backupID := uuid.New()
				primary.command(t, primary.backupCommand(backup.Command{Action: "backup", Installation: installation, RunID: backupID}, nil))
				status := primary.call(t, hostRequest{Policy: &policyID}).Status
				if status == nil || len(status.Runs) != 1 || status.Runs[0].State != "succeeded" || !backup.SnapshotID(status.Runs[0].Snapshot) {
					t.Fatal("PostgreSQL18 recovery snapshot did not complete")
				}
				snapshot = status.Runs[0].Snapshot
			}
			run, restoredVolume := uuid.New(), uuid.New()
			primary.command(t, primary.backupCommand(backup.Command{Action: "restore", Installation: installation, RunID: run, Snapshot: snapshot, TargetID: restoredVolume}, nil))
			status := primary.call(t, hostRequest{Policy: &policyID}).Status
			var restored backup.Run
			for _, candidate := range status.Runs {
				if candidate.ID == run {
					restored = candidate
				}
			}
			if _, err := backup.CutoverVolume(installation.Policy, restored); err != nil {
				t.Fatalf("isolated restore did not verify: %v; state=%s note=%s", err, restored.State, restored.Note)
			}
			data := &Engine{Docker: primary.docker, DockerCLI: primary.cli}
			if method == backup.QuiescedFiles {
				if _, err := data.dataOutput(ctx, deployment.UnitName(files), "sh", "-c", "echo newer-source-data > /files/value"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := data.pgQuery(ctx, deployment.UnitName(pg), currentGroup.Specification.Applications[1], "UPDATE cross_host_marker SET value='newer source data'"); err != nil {
				t.Fatal(err)
			}
			boundary := deployment.RestoreCutover{SourcePlanID: currentGroup.ID, SourceHash: currentGroup.Hash, PolicyID: policyID, RunID: run, ManifestHash: restored.ManifestHash, OldVolumeID: oldVolume, TargetVolumeID: restoredVolume}
			target, err := deployment.RestoredGroup(currentGroup, boundary, uuid.New(), uuid.New(), time.Now().Add(time.Hour))
			if err != nil || target.Validate(true) != nil {
				t.Fatal("invalid restored target", err)
			}
			parent := uuid.New()
			coordinationHash := deployment.Hash([]any{parent, currentGroup.Hash, target.Hash})
			intents := make([]deployment.Maintenance, len(hosts))
			for index := range hosts {
				plan := target.Distributed.Units[index].Plan
				intents[index] = deployment.Maintenance{Kind: "restore", ResourceID: policyID, RunID: run, Restore: &boundary, EffectHash: deployment.Hash(boundary), ExpectedState: completed[index].StateHash, Target: &plan}
			}
			command := func(index int, phase string) api.ExecutionCommand {
				return hosts[index].coordinatedCommand(deployment.CrossHostCommand{ParentID: parent, GroupHash: coordinationHash, Phase: phase, Unit: currentGroup.Distributed.Units[index], Maintenance: &intents[index], Secrets: secretsForPlan(currentGroup.Distributed.Units[index].Plan, secrets), UseTarget: phase == "complete"})
			}
			for index, host := range hosts {
				host.command(t, command(index, "reserve"))
			}
			for index, host := range hosts {
				host.command(t, command(index, "pause"))
			}
			cutover := command(0, "restore")
			if err := json.NewEncoder(primary.input).Encode(hostRequest{Command: &cutover, CrashAt: "restore-cutover-created"}); err != nil {
				t.Fatal(err)
			}
			if err := primary.output.Decode(&lost); err == nil {
				t.Fatal("restore cutover crash did not interrupt the helper")
			}
			primary.stop()
			if _, err := primary.docker.ContainerUpdate(ctx, deployment.UnitName(consumer), client.ContainerUpdateOptions{Resources: &container.Resources{Memory: 64 << 20, MemorySwap: 128 << 20}}); err != nil {
				t.Fatal(err)
			}
			created, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(consumer), client.ContainerInspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			foreignLabels := maps.Clone(created.Container.Config.Labels)
			foreignLabels[labelResource] = uuid.NewString()
			foreignLabels["com.docker.compose.container-number"] = "2"
			foreignLabels["com.astilba.fixture"] = parent.String()
			foreign, err := primary.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-foreign-" + uuid.NewString(), Config: &container.Config{Image: target.PrimaryPlan().Images[consumer.String()], Cmd: []string{"sleep", "600"}, Labels: foreignLabels}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
			if err != nil {
				t.Fatal(err)
			}
			primary.start(t)
			blockedRestore := primary.call(t, hostRequest{Command: &cutover}).Report
			if blockedRestore == nil || blockedRestore.State != "needs_attention" {
				t.Fatal("creating-stage replay adopted a foreign Compose replica")
			}
			retainedForeign, err := primary.docker.ContainerInspect(ctx, foreign.ID, client.ContainerInspectOptions{})
			if err != nil || retainedForeign.Container.Config.Labels["com.astilba.fixture"] != parent.String() {
				t.Fatal("creating-stage replay removed a foreign Compose replica", err)
			}
			if _, err = primary.docker.ContainerRemove(ctx, retainedForeign.Container.ID, client.ContainerRemoveOptions{}); err != nil {
				t.Fatal(err)
			}
			primary.call(t, hostRequest{Retry: &cutover.Id})
			primary.command(t, cutover)
			recreated, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(consumer), client.ContainerInspectOptions{})
			if err != nil || recreated.Container.HostConfig.Memory != 0 {
				t.Fatal("restore recovery adopted memory drift during the creation gap", err)
			}
			for index, host := range hosts {
				host.command(t, command(index, "resume-target"))
				r := host.command(t, command(index, "complete"))
				var receipt deployment.CrossHostReceipt
				if err := json.Unmarshal(*r.Crosshost, &receipt); err != nil {
					t.Fatal(err)
				}
				completed[index] = receipt.Observation
			}
			if method == backup.QuiescedFiles {
				value, err := data.dataOutput(ctx, deployment.UnitName(files), "cat", "/files/value")
				if err != nil || strings.TrimSpace(string(value)) != "retained-file-data" {
					t.Fatalf("cutover did not serve restored files: %s (%v)", value, err)
				}
			} else {
				value, err := data.pgQuery(ctx, deployment.UnitName(pg), target.Specification.Applications[1], "SELECT value FROM cross_host_marker")
				if err != nil || strings.TrimSpace(string(value)) != "retained database data" {
					t.Fatalf("cutover did not serve the restored database marker: %s (%v)", value, err)
				}
			}
			if _, err := primary.docker.VolumeInspect(ctx, deployment.VolumeName(oldVolume), client.VolumeInspectOptions{}); err != nil {
				t.Fatal("restore cutover removed source data", err)
			}
			nativeWaitPlacementRecovery(t)
			currentGroup = target
		})
	}
	t.Run("current_controller_and_completion_drift", func(t *testing.T) {
		for _, host := range hosts {
			host.call(t, hostRequest{Transfer: true})
			host.controllerGeneration = 1
		}
		parent := uuid.New()
		target := currentGroup
		intents := make([]deployment.Maintenance, len(hosts))
		for index := range hosts {
			intents[index] = deployment.Maintenance{Kind: "backup", ResourceID: uuid.New(), RunID: uuid.New(), EffectHash: strings.Repeat("e", 64), ExpectedState: completed[index].StateHash}
		}
		command := func(index int, phase string) api.ExecutionCommand {
			return hosts[index].coordinatedCommand(deployment.CrossHostCommand{ParentID: parent, GroupHash: target.Hash, Phase: phase, Unit: target.Distributed.Units[index], Maintenance: &intents[index], Secrets: secretsForPlan(target.Distributed.Units[index].Plan, secrets)})
		}
		stale := command(0, "reserve")
		stale.ControllerGeneration = nil
		if err := json.NewEncoder(primary.input).Encode(hostRequest{Command: &stale}); err != nil {
			t.Fatal(err)
		}
		var rejected hostReply
		if err := primary.output.Decode(&rejected); err != nil || rejected.Error == "" {
			t.Fatal("old controller authority was accepted", err)
		}
		for index, host := range hosts {
			host.command(t, command(index, "reserve"))
		}
		for index, host := range hosts {
			host.command(t, command(index, "pause"))
		}
		for index, host := range hosts {
			host.command(t, command(index, "resume-source"))
		}
		instance := target.Distributed.Units[0].Plan.InstanceIDs(app)[0]
		original, err := primary.docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := primary.docker.ContainerUpdate(ctx, deployment.UnitName(instance), client.ContainerUpdateOptions{Resources: &container.Resources{Memory: 129 << 20, MemorySwap: 129 << 20}}); err != nil {
			t.Fatal(err)
		}
		c := command(0, "complete")
		c.Deadline = time.Now().Add(2 * time.Second)
		r := primary.call(t, hostRequest{Command: &c}).Report
		if r == nil || r.State != "needs_attention" {
			t.Fatalf("post-resume drift released completion: %+v", r)
		}
		blocked := primary.call(t, hostRequest{Barrier: true}).Barrier
		if blocked == nil || !*blocked {
			t.Fatal("post-resume drift released the host reservation")
		}
		if _, err := primary.docker.ContainerUpdate(ctx, deployment.UnitName(instance), client.ContainerUpdateOptions{Resources: &container.Resources{Memory: original.Container.HostConfig.Memory, MemorySwap: original.Container.HostConfig.MemorySwap}}); err != nil {
			t.Fatal(err)
		}
		if remaining := time.Until(c.Deadline); remaining > 0 {
			time.Sleep(remaining + time.Millisecond)
		}
		primary.call(t, hostRequest{Retry: &c.Id})
		primary.command(t, c)
		if blocked := primary.call(t, hostRequest{Barrier: true}).Barrier; blocked == nil || *blocked {
			t.Fatal("same-command recovery did not confirm completion and release its barrier")
		}
		hosts[1].command(t, command(1, "complete"))
		// A rejected reservation on another participant must not turn this
		// host's unchanged source into an unapplied completion receipt.
		parent = uuid.New()
		remote.command(t, command(1, "resume-source"))
		report := remote.command(t, command(1, "complete"))
		var receipt deployment.CrossHostReceipt
		if json.Unmarshal(*report.Crosshost, &receipt) != nil || !receipt.Observation.Applied {
			t.Fatal("unreserved maintenance lost its retained applied source")
		}
		if blocked := remote.call(t, hostRequest{Barrier: true}).Barrier; blocked == nil || *blocked {
			t.Fatal("unreserved source completion installed a maintenance barrier")
		}
	})
	t.Run("recovered_cleanup_preserves_newer_revocation", func(t *testing.T) {
		parent := uuid.New()
		commands := make([]deployment.CrossHostCommand, len(hosts))
		for index, host := range hosts {
			unit := currentGroup.Distributed.Units[index]
			r := host.crossHost(t, uuid.New(), currentGroup.Hash, "observe", unit, secretsForPlan(unit.Plan, secrets))
			var receipt deployment.CrossHostReceipt
			if json.Unmarshal(*r.Crosshost, &receipt) != nil {
				t.Fatal("invalid source observation")
			}
			intent := deployment.Maintenance{Kind: "backup", ResourceID: uuid.New(), RunID: uuid.New(), EffectHash: strings.Repeat("f", 64), ExpectedState: receipt.Observation.StateHash}
			commands[index] = deployment.CrossHostCommand{ParentID: parent, GroupHash: currentGroup.Hash, Phase: "reserve", Unit: unit, Maintenance: &intent, Secrets: secretsForPlan(unit.Plan, secrets)}
			host.command(t, host.coordinatedCommand(commands[index]))
		}
		// Freeze cleanup before delivering the newer independent revocation.
		wanted := commands[0].Unit.Plan.Local.Network
		wanted.Generation += 2
		wanted.Egress = nil
		body := hostnetwork.Command{Action: "restrict", NetworkID: wanted.NetworkID, MembershipID: wanted.MembershipID, Manifest: &wanted}
		raw, _ := json.Marshal(body)
		message := json.RawMessage(raw)
		primary.networkGeneration++
		cleanup := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: primary.server, EnvironmentId: commands[0].Unit.Plan.EnvironmentID, Generation: primary.networkGeneration, Kind: "network", Action: "inspect", ResourceId: wanted.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(-time.Hour), Network: &message}
		remoteInstance := commands[1].Unit.Plan.InstanceIDs(app)[0]
		reachable := func() bool {
			return exec.CommandContext(ctx, remote.cli, "--host", remote.socket, "exec", deployment.UnitName(remoteInstance), "sh", "-c", `nc -z -w 2 "$DB_HOST" "$DB_PORT"`).Run() == nil
		}
		if !reachable() {
			t.Fatal("native source connection was not available before revocation")
		}
		prior, err := (hostnetwork.Host{Dir: primary.state}).Snapshot(wanted.NetworkID)
		if err != nil {
			t.Fatal(err)
		}
		revocation := prior.Manifest
		revocation.Generation++
		revocation.Exports = nil
		primary.network(t, nil, revocation, true)
		if reachable() {
			t.Fatal("newer revocation did not block the actual private connection")
		}
		for index, host := range hosts {
			host.stop()
			journal, err := Open(host.state)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := journal.RecoveryEvidence()
			if err != nil {
				journal.Close()
				t.Fatal(err)
			}
			unit := commands[index].Unit.Plan
			h := deployment.RecoveryHandover{Authority: deployment.RecoveryAuthority{ParentID: parent, RootHash: host.checkpoints[parent].RootHash, EvidenceHash: deployment.Hash(evidence), PriorController: evidence.Controller, Controller: evidence.Controller + 1}, Descriptor: host.checkpoints[parent], EnvironmentID: unit.EnvironmentID, UnitID: unit.UnitID, UnitHash: unit.Hash, Primary: unit.Local.Primary, Network: unit.Local.Network}
			if err = journal.TransferRecovery("https://recovered.example.test", make([]byte, 32), strings.Repeat("t", 43), h); err != nil {
				journal.Close()
				t.Fatal(err)
			}
			journal.Close()
			host.controllerGeneration = h.Authority.Controller
			host.start(t)
		}
		for index, host := range hosts {
			openBody := commands[index]
			openBody.Phase = "recovery-open"
			open := host.coordinatedCommand(openBody)
			open.Id = deployment.RecoveryOpenID(parent, openBody.Unit.Plan.UnitID, host.controllerGeneration)
			host.command(t, open)
		}
		cleanup.ControllerGeneration = &primary.controllerGeneration
		primary.command(t, cleanup)
		installed, err := (hostnetwork.Host{Dir: primary.state}).Snapshot(wanted.NetworkID)
		if err != nil || len(installed.Manifest.Exports)+len(installed.Manifest.Egress) != 0 || installed.Manifest.ControllerGeneration != primary.controllerGeneration {
			t.Fatal("old cleanup restored a revoked grant or missed its own removal", err)
		}
		if reachable() {
			t.Fatal("frozen cleanup reopened the revoked private connection")
		}
	})

}

func secretsForPlan(p deployment.Plan, secrets map[string]string) map[string]string {
	out := map[string]string{}
	for _, id := range p.SecretReferences() {
		out[id.String()] = secrets[id.String()]
	}
	return out
}

// Public health checks run every five seconds independently of local readiness.
// Writer maintenance promises bounded convergence rather than instant routing.
func nativeWaitPlacementRecovery(t *testing.T) {
	t.Helper()
	started := time.Now()
	for {
		status, body := nativePlacementRequest(t, "10.219.0.1", "cross-host.example.test")
		if status == 200 {
			t.Logf("public routing recovered after %s", time.Since(started).Round(time.Millisecond))
			return
		}
		if time.Since(started) > 30*time.Second {
			t.Fatalf("application did not recover after writer restart: %d %s", status, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
