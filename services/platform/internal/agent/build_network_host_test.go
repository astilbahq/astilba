package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"encoding/json/jsontext"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// nativeHostAddress is the host's primary IPv4 address.
func nativeHostAddress(t *testing.T) string {
	t.Helper()
	probe, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	return probe.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap().String()
}

// nativeFixtureServer serves handler from the fixture LAN, where builds can reach a
// private Git provider or registry. They never reach one on the runner itself.
func nativeFixtureServer(t *testing.T, lan nativeLAN, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = lan.listen(t, lan.Address+":0")
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// nativeFixtureTLSServer serves handler over TLS from the fixture LAN, with a certificate for
// its address.
func nativeFixtureTLSServer(t *testing.T, lan nativeLAN, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "astilba native fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP(lan.Address)}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = lan.listen(t, lan.Address+":0")
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

// Builds reach the public internet and the destinations their runner allows, and nothing
// else on the host's networks: not cloud metadata, the LAN, the host, its containers or
// management, even when allowed. The policy is back before a build replayed after a reboot.
func TestNativeBuildNetwork(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// A neighbour on the LAN, which also answers at a link-local address as a cloud's
	// metadata service does, through the host's routes.
	lan := startNativeLAN(t, "169.254.42.42")
	canary := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "canary") })
	serve := func(listener net.Listener) *httptest.Server {
		server := httptest.NewUnstartedServer(canary)
		server.Listener.Close()
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		return server
	}
	host := nativeHostAddress(t)
	// The test allows a port on the host to prove that no entry opens it, and only private
	// addresses can be allowed.
	if a := netip.MustParseAddr(host); !slices.ContainsFunc(pipeline.AllowableRanges, func(p netip.Prefix) bool { return p.Contains(a) }) {
		t.Fatalf("this test needs a private primary address, not %s", host)
	}
	listenHost := func() net.Listener {
		listener, err := net.Listen("tcp", host+":0")
		if err != nil {
			t.Fatal(err)
		}
		return listener
	}
	metadata := serve(lan.listen(t, "169.254.42.42:0"))
	refusedLAN, allowedLAN, management := serve(lan.listen(t, lan.Address+":0")), serve(lan.listen(t, lan.Address+":0")), serve(lan.listen(t, lan.Address+":0"))
	refusedHost, allowedHost := serve(listenHost()), serve(listenHost())
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	fixtureNetwork := "astilba-egress-fixture-" + suffix
	if _, err = docker.NetworkCreate(ctx, fixtureNetwork, client.NetworkCreateOptions{Driver: "bridge"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.NetworkRemove(context.Background(), fixtureNetwork, client.NetworkRemoveOptions{})
	})
	neighbour, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-egress-neighbour-" + suffix, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sh", "-c", "mkdir /w; echo canary > /w/index.html; exec httpd -f -p 8080 -h /w"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(fixtureNetwork)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), neighbour.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(ctx, neighbour.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspected, err := docker.ContainerInspect(ctx, neighbour.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	neighbourAddress := inspected.Container.NetworkSettings.Networks[fixtureNetwork].IPAddress.String()

	state := t.TempDir()
	_ = os.Chmod(state, 0700)
	journal, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	serverID := uuid.New()
	if err = journal.SetConfig(Config{Origin: management.URL, ServerID: serverID, MachineID: uuid.New(), Role: "combined"}); err != nil {
		t.Fatal(err)
	}
	dockerCLI, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Journal: journal, Docker: docker, DockerCLI: dockerCLI}

	destinations := map[string]string{
		"metadata":     metadata.URL + "/",
		"lan":          refusedLAN.URL + "/",
		"lan-allowed":  allowedLAN.URL + "/",
		"host":         refusedHost.URL + "/",
		"host-allowed": allowedHost.URL + "/",
		"management":   management.URL + "/",
		"neighbour":    "http://" + neighbourAddress + ":8080/",
		"public":       "https://registry-1.docker.io/v2/",
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range destinations {
			if !yield(name) {
				return
			}
		}
	})
	// A response of any status, such as the public registry's 401, reached its destination.
	probe := "probe() { if wget -q -T 5 -O /dev/null \"$2\" 2>/tmp/e || grep -q 'server returned error' /tmp/e; then echo \"PROBE $1=reached\"; else echo \"PROBE $1=refused\"; fi; }"
	for _, name := range names {
		probe += "; probe " + name + " " + destinations[name]
	}
	probe += "; ip -o -4 address show | awk '{print \"IFACE\", $2, $4}'"
	root := t.TempDir()
	work, repo := filepath.Join(root, "work"), filepath.Join(root, "team", "app.git")
	run("git", "init", "--initial-branch=main", work)
	files := map[string]string{"Dockerfile": "FROM " + execution.TestImage + "\nRUN " + probe + "\n", "Dockerfile.add": "FROM " + execution.TestImage + "\nADD " + metadata.URL + "/ /metadata\n"}
	for name, value := range files {
		if err = os.WriteFile(filepath.Join(work, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "-C", work, "add", ".")
	run("git", "-C", work, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "fixture")
	commit := run("git", "-C", work, "rev-parse", "HEAD")
	_ = os.Mkdir(filepath.Dir(repo), 0700)
	run("git", "clone", "--bare", work, repo)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A self-hosted Git provider on the LAN, which builds reach because they clone from it.
	provider := nativeFixtureServer(t, lan, &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})

	port := func(server *httptest.Server) int { return server.Listener.Addr().(*net.TCPAddr).Port }
	connection := uuid.New()
	command := pipeline.Command{Action: "build", BuildNetwork: pipeline.BuildNetworkV1, Connection: pipeline.Connection{ID: connection, Provider: "gitea", BaseURL: provider.URL, Repository: "team/app", RepositoryID: 12}, Commit: commit, Branch: "main", RevisionID: uuid.New(), UnitID: uuid.New(), AppID: uuid.New(), InputHash: strings.Repeat("a", 64), SubmittedAt: time.Now().UTC().Truncate(time.Second)}
	command.Build = &pipeline.BuildConfig{ConnectionID: connection, Branch: "main", RunnerID: serverID, Context: ".", Dockerfile: "Dockerfile", Architecture: runtime.GOARCH, Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
	// The runner and management are allowed too, and must stay refused: no allowlist reaches
	// either.
	command.BuildEgress = pipeline.CanonicalEgress([]pipeline.EgressRule{{Prefix: lan.Address + "/32", Port: port(allowedLAN)}, {Prefix: host + "/32", Port: port(allowedHost)}, {Prefix: lan.Address + "/32", Port: port(management)}})
	if err = command.Validate(); err != nil {
		t.Fatal(err)
	}
	var interrupt bool
	engine.Point = func(point string) {
		if point == "build" && interrupt {
			interrupt = false
			panic("interrupted build")
		}
	}
	generation := int64(0)
	execute := func(p pipeline.Command) (api.ExecutionCommand, Record) {
		t.Helper()
		generation++
		raw, _ := stablejson.Marshal(p)
		message := jsontext.Value(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: serverID, EnvironmentId: uuid.New(), ResourceId: p.UnitID, Generation: generation, Kind: "pipeline", Action: "inspect", PlanHash: p.Digest(), Pipeline: &message, Deadline: time.Now().Add(5 * time.Minute)}
		interrupted := func() (stopped bool) {
			defer func() {
				if value := recover(); value != nil {
					if value != "interrupted build" {
						panic(value)
					}
					stopped = true
				}
			}()
			if err := engine.Execute(ctx, c); err != nil {
				t.Fatal(err)
			}
			return false
		}()
		if interrupted {
			// A reboot clears the host's firewall and stops the build container, and the
			// agent starts again with the build still running in its journal.
			run("nft", "delete", "table", "inet", "astilba_builds")
			if _, err := docker.ContainerKill(ctx, pipelineName(c), client.ContainerKillOptions{}); err != nil && !errdefs.IsNotFound(err) {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			if journal, err = Open(state); err != nil {
				t.Fatal(err)
			}
			engine = &Engine{Journal: journal, Docker: docker, DockerCLI: dockerCLI, Point: engine.Point}
			if err := engine.Execute(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		record, err := journal.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		if err = journal.Acknowledge(c.Id, record.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		return c, record
	}
	want := map[string]string{"metadata": "refused", "lan": "refused", "lan-allowed": "reached", "host": "refused", "host-allowed": "refused", "management": "refused", "neighbour": "refused", "public": "reached"}
	check := func(record Record, c api.ExecutionCommand) {
		t.Helper()
		var result pipeline.Result
		if record.Report.Pipeline != nil {
			_ = json.Unmarshal(*record.Report.Pipeline, &result)
		}
		if record.Report.State != "succeeded" {
			t.Fatalf("probe build: %s\n%s", record.Report.Note, result.Logs)
		}
		defer docker.ImageRemove(ctx, result.Image, client.ImageRemoveOptions{Force: true})
		got := map[string]string{}
		for _, match := range regexp.MustCompile(`PROBE ([a-z-]+)=(reached|refused)`).FindAllStringSubmatch(result.Logs, -1) {
			got[match[1]] = match[2]
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("build reached %v, want %v\n%s", got, want, result.Logs)
		}
		// The build sees only its own network, which is the owned build network.
		n, err := docker.NetworkInspect(ctx, buildNetworkName, client.NetworkInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		subnet := n.Network.IPAM.Config[0].Subnet
		for _, match := range regexp.MustCompile(`IFACE (\S+) (\S+)`).FindAllStringSubmatch(result.Logs, -1) {
			address, err := netip.ParsePrefix(match[2])
			if match[1] != "lo" && (err != nil || !subnet.Contains(address.Addr())) {
				t.Fatalf("build sees %s %s outside the build network %s", match[1], match[2], subnet)
			}
		}
		if !strings.Contains(result.Logs, "The build network refused") {
			t.Fatal("refusals not reported in the build log")
		}
	}
	c, record := execute(command)
	check(record, c)

	// BuildKit fetches an ADD source itself, from the build container's network.
	added := command
	added.Build = new(*command.Build)
	added.Build.Dockerfile = "Dockerfile.add"
	if _, record := execute(added); record.Report.State != "failed" {
		t.Fatal("a build fetched instance metadata through ADD", record.Report.Note)
	}

	// A Git provider on the runner itself can't be reached, so the build stops before it starts.
	onRunner := command
	onRunner.Connection.BaseURL = refusedHost.URL
	if _, record := execute(onRunner); record.Report.State != "failed" || !strings.Contains(record.Report.Note, "is on this server") {
		t.Fatal("a build with its Git provider on the runner was not refused up front", record.Report.State, record.Report.Note)
	}

	interrupt = true
	c, record = execute(command)
	if interrupt {
		t.Fatal("the build was not interrupted")
	}
	check(record, c)
}
