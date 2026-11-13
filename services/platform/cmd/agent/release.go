package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// releaseCommand creates a release signing key, describes a built agent for signing, or signs it
// into a release catalog. See the release procedure in docs/agent.md.
func releaseCommand(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	keyPath := f.String("key", "", "private signing key PEM")
	out := f.String("out", "", "release catalog directory, or the unsigned manifest to write")
	binary := f.String("artifact", "", "agent executable, as the release workflow built it")
	manifest := f.String("manifest", "", "the release workflow's unsigned manifest for the artifact")
	development := f.Bool("development", false, "sign a development build, whose Go build information records no Git checkout to check its commit against")
	if e := f.Parse(args); e != nil {
		return e
	}
	if command == "release-manifest" {
		return releaseManifest(*binary, *out)
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
	raw, e = os.ReadFile(*manifest)
	if e != nil {
		return e
	}
	var r api.AgentRelease
	if e = json.Unmarshal(raw, &r, json.RejectUnknownMembers(true)); e != nil || r.Signature != "" {
		return errors.New("the manifest must be the release workflow's unsigned manifest")
	}
	if e = agentrelease.Check(r); e != nil {
		return e
	}
	data, e := os.ReadFile(*binary)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != r.Size || hex.EncodeToString(sum[:]) != r.Sha256 {
		return errors.New("the artifact is not the one its manifest describes")
	}
	if e = checkBuild(*binary, r, !*development); e != nil {
		return e
	}
	if !*development {
		if e = checkSigner(r); e != nil {
			return e
		}
	}
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
	b, _ := stablejson.MarshalIndent(r, "", "  ")
	if e = writeNew(filepath.Join(*out, r.Version+"-"+string(r.Arch)+".json"), b, 0644); e != nil {
		return e
	}
	fmt.Printf("Signed:\n%s\n", b)
	return nil
}

// releaseManifest describes a built agent for signing: what it reports about itself when run here,
// on its own architecture, with its size and digest. The release workflow runs it on each
// architecture's runner.
func releaseManifest(path, out string) error {
	path, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	described, e := exec.Command(path, "describe").Output()
	if e != nil {
		return fmt.Errorf("run the artifact's describe: %w", e)
	}
	var identity agentrelease.Identity
	if e = json.Unmarshal(described, &identity, json.RejectUnknownMembers(true)); e != nil {
		return fmt.Errorf("read the artifact's describe output: %w", e)
	}
	sum := sha256.Sum256(data)
	r := api.AgentRelease{Version: identity.Version, MinimumVersion: identity.MinimumVersion, Commit: identity.Commit, Protocol: api.AgentReleaseProtocol(identity.Protocol), Arch: api.AgentReleaseArch(identity.Arch), Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:])}
	if e = agentrelease.Check(r); e != nil {
		return e
	}
	if e = checkBuild(path, r, true); e != nil {
		return e
	}
	b, _ := stablejson.MarshalIndent(r, "", "  ")
	return writeNew(out, b, 0644)
}

// checkBuild compares a release with the artifact's Go build information: the target it was built
// for and the unmodified Git checkout it was built from, which must be the release's commit. Only
// a development build, signed with --development, may lack that record. The version and minimum
// version are checked against the binary's own describe output, by release-manifest on each
// architecture and by every host before it installs the release.
func checkBuild(path string, r api.AgentRelease, requireCheckout bool) error {
	info, e := buildinfo.ReadFile(path)
	if e != nil {
		return fmt.Errorf("read the artifact's Go build information: %w", e)
	}
	if info.Path != "github.com/astilbahq/astilba/services/platform/cmd/agent" {
		return fmt.Errorf("the artifact is %s, not the agent", info.Path)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != "linux" || settings["CGO_ENABLED"] != "0" || settings["GOARCH"] != string(r.Arch) {
		return fmt.Errorf("a release for %s is built for linux/%s with CGO_ENABLED=0", r.Arch, r.Arch)
	}
	revision, ok := settings["vcs.revision"]
	if !ok && requireCheckout {
		return errors.New("the artifact's build recorded no Git checkout to check its commit against; release builds come from the release workflow")
	}
	if ok && (revision != r.Commit || settings["vcs.modified"] != "false") {
		return fmt.Errorf("the manifest names commit %s, but the artifact was built from %s with modified=%s", r.Commit, revision, settings["vcs.modified"])
	}
	return nil
}

// checkSigner requires release-sign itself to be built from the release's commit, unmodified, as
// scripts/agent-release-sign.sh builds it, and the release's minimum version to be the one that
// commit embeds. The artifact's Go build information records its commit but not the -ldflags that
// could override its minimum version, so this ties the signed minimum version to the reviewed file.
func checkSigner(r api.AgentRelease) error {
	info, ok := debug.ReadBuildInfo()
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if !ok || settings["vcs.revision"] != r.Commit || settings["vcs.modified"] != "false" {
		return fmt.Errorf("release-sign must be built from commit %s, unmodified; scripts/agent-release-sign.sh builds it so", r.Commit)
	}
	if minimumVersion != "" || strings.TrimSpace(sourceMinimumVersion) != r.MinimumVersion {
		return fmt.Errorf("the manifest's minimum version %s is not %s, the one commit %s embeds", r.MinimumVersion, strings.TrimSpace(sourceMinimumVersion), r.Commit)
	}
	return nil
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
