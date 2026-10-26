package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/distribution/reference"
	"github.com/google/uuid"
)

const BuilderImage = "moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c"
const BuilderVersion = "v0.34.1"

type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type Connection struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Provider     string    `json:"provider"`
	BaseURL      string    `json:"baseUrl"`
	Repository   string    `json:"repository"`
	RepositoryID int64     `json:"repositoryId"`
	Private      bool      `json:"private"`
}
type Access struct {
	API     Credential `json:"api"`
	Clone   Credential `json:"clone"`
	Webhook string     `json:"webhook"`
}
type Registry struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Repository string    `json:"repository"`
}
type RegistryAccess struct {
	Push Credential `json:"push"`
	Pull Credential `json:"pull"`
}
type BuildSecret struct {
	Name      string    `json:"name"`
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}
type Source struct {
	ConnectionID     uuid.UUID  `json:"connectionId"`
	Branch           string     `json:"branch"`
	RunnerID         uuid.UUID  `json:"runnerId"`
	FallbackRunnerID *uuid.UUID `json:"fallbackRunnerId,omitempty"`
	Entrypoints      []string   `json:"entrypoints"`
	Paths            []string   `json:"paths"`
	AutoDeploy       bool       `json:"autoDeploy"`
}
type BuildConfig struct {
	Builder          string               `json:"builder,omitempty"`
	Railpack         *RailpackOptions     `json:"railpack,omitempty"`
	ConnectionID     uuid.UUID            `json:"connectionId"`
	Branch           string               `json:"branch"`
	RunnerID         uuid.UUID            `json:"runnerId"`
	FallbackRunnerID *uuid.UUID           `json:"fallbackRunnerId,omitempty"`
	RegistryID       *uuid.UUID           `json:"registryId,omitempty"`
	Context          string               `json:"context"`
	Dockerfile       string               `json:"dockerfile"`
	Architecture     string               `json:"architecture"`
	Arguments        map[string]string    `json:"arguments"`
	Secrets          map[string]uuid.UUID `json:"secrets"`
	AutoBuild        bool                 `json:"autoBuild"`
	AutoDeploy       bool                 `json:"autoDeploy"`
	Budget           *BuildBudget         `json:"budget,omitempty"`
	WatchPaths       []string             `json:"watchPaths,omitempty"`
}
type Artifact struct {
	ID           uuid.UUID  `json:"id"`
	BuildID      uuid.UUID  `json:"buildId"`
	Image        string     `json:"image"`
	Digest       string     `json:"digest"`
	Architecture string     `json:"architecture"`
	InputHash    string     `json:"inputHash"`
	Commit       string     `json:"commit"`
	ServerID     *uuid.UUID `json:"serverId,omitempty"`
	RegistryID   *uuid.UUID `json:"registryId,omitempty"`
}
type PreviewSource struct {
	RequestID      uuid.UUID `json:"requestId"`
	Generation     int64     `json:"generation"`
	Number         int       `json:"number"`
	HeadRepository int64     `json:"headRepository"`
}

type Command struct {
	Preview       *PreviewSource         `json:"preview,omitempty"`
	SubmissionID  uuid.UUID              `json:"submissionId"`
	Action        string                 `json:"action"`
	Connection    Connection             `json:"connection"`
	Commit        string                 `json:"commit"`
	Branch        string                 `json:"branch"`
	RevisionID    uuid.UUID              `json:"revisionId"`
	UnitID        uuid.UUID              `json:"unitId"`
	AppID         uuid.UUID              `json:"appId"`
	Source        *Source                `json:"source,omitempty"`
	Bundle        *composeproject.Bundle `json:"bundle,omitempty"`
	Build         *BuildConfig           `json:"build,omitempty"`
	InputHash     string                 `json:"inputHash"`
	CompareCommit string                 `json:"compareCommit,omitempty"`
	CacheScope    string                 `json:"cacheScope,omitempty"`
	Railpack      *RailpackInputs        `json:"railpack,omitempty"`
	Registry      *Registry              `json:"registry,omitempty"`
	SubmittedAt   time.Time              `json:"submittedAt"`
	// Values exist only in the assigned agent's transport and encrypted journal.
	Access         *Access           `json:"access,omitempty"`
	BuildSecrets   map[string]string `json:"buildSecrets,omitempty"`
	RegistryAccess *RegistryAccess   `json:"registryAccess,omitempty"`
}

func Hash(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c Command) Digest() string {
	c.Access = nil
	c.BuildSecrets = nil
	c.RegistryAccess = nil
	return Hash(c)
}

type Result struct {
	Commit       string                `json:"commit"`
	Files        []composeproject.File `json:"files,omitempty"`
	ReviewURL    string                `json:"reviewUrl,omitempty"`
	ReviewNumber int                   `json:"reviewNumber,omitempty"`
	Image        string                `json:"image,omitempty"`
	Digest       string                `json:"digest,omitempty"`
	Architecture string                `json:"architecture,omitempty"`
	Logs         string                `json:"logs,omitempty"`
	Cache        string                `json:"cache,omitempty"`
	Skipped      bool                  `json:"skipped,omitempty"`
	Railpack     *RailpackSummary      `json:"railpack,omitempty"`
}

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
var inputName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func ValidCommit(s string) bool { return commitPattern.MatchString(s) && strings.Trim(s, "0") != "" }
func ValidRepository(s string) bool {
	if len(s) > 240 || strings.Count(s, "/") < 1 {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
				return false
			}
		}
	}
	return true
}
func ValidBranch(s string) bool {
	if s == "@" || len(s) == 0 || len(s) > 200 || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") {
		return false
	}
	for _, c := range s {
		if c <= 32 || c == 127 || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".lock") {
			return false
		}
	}
	return true
}
func ValidContext(s string) bool { return s == "." || composeproject.ValidPath(s) }
func (b BuildConfig) Validate() error {
	if err := b.validateBuilder(); err != nil {
		return err
	}
	if b.ConnectionID == uuid.Nil || b.RunnerID == uuid.Nil || !ValidBranch(b.Branch) || !ValidContext(b.Context) || !b.UsesRailpack() && (!composeproject.ValidPath(b.Dockerfile) || path.Clean(b.Dockerfile) != b.Dockerfile) || (b.Architecture != "amd64" && b.Architecture != "arm64") || len(b.Arguments) > 32 || len(b.Secrets) > 16 {
		return errors.New("invalid build configuration")
	}
	for k, v := range b.Arguments {
		if !inputName.MatchString(k) || len(v) > 4096 || strings.ContainsRune(v, 0) {
			return errors.New("invalid build argument")
		}
	}
	for k, v := range b.Secrets {
		if !inputName.MatchString(k) || strings.HasPrefix(k, "GIT_AUTH") || v == uuid.Nil {
			return errors.New("invalid declared build secret")
		}
	}
	if b.FallbackRunnerID != nil && (*b.FallbackRunnerID == uuid.Nil || *b.FallbackRunnerID == b.RunnerID) {
		return errors.New("invalid fallback runner")
	}
	if err := b.Limits().Validate(); err != nil {
		return err
	}
	if len(b.WatchPaths) > 16 {
		return &BuildValidationError{"watchPaths", "Use at most 16 watch paths."}
	}
	seen := map[string]bool{}
	for _, p := range b.WatchPaths {
		if !composeproject.ValidPath(p) || utf8.RuneCountInString(p) > 240 || strings.ContainsAny(p, "*?[]") {
			return &BuildValidationError{"watchPaths", "Use repository-relative watch paths of at most 240 characters, without leading or trailing slashes, parent traversal or wildcards."}
		}
		if seen[p] {
			return &BuildValidationError{"watchPaths", "Remove duplicate watch paths."}
		}
		seen[p] = true
	}
	return nil
}
func (s Source) Validate() error {
	if s.ConnectionID == uuid.Nil || s.RunnerID == uuid.Nil || !ValidBranch(s.Branch) || len(s.Paths) == 0 || len(s.Paths) > 16 || len(s.Entrypoints) == 0 || len(s.Entrypoints) > 8 {
		return errors.New("invalid Git source")
	}
	seen := map[string]bool{}
	for _, p := range s.Paths {
		if !composeproject.ValidPath(p) || seen[p] {
			return errors.New("invalid source path")
		}
		seen[p] = true
	}
	for _, p := range s.Entrypoints {
		if !seen[p] {
			return errors.New("entrypoint is not in selected files")
		}
	}
	if s.FallbackRunnerID != nil && (*s.FallbackRunnerID == uuid.Nil || *s.FallbackRunnerID == s.RunnerID) {
		return errors.New("invalid fallback runner")
	}
	return nil
}
func ValidRegistry(repo string) bool {
	r, e := reference.ParseNormalizedNamed(repo)
	if e != nil {
		return false
	}
	_, tag := r.(reference.Tagged)
	_, digest := r.(reference.Digested)
	return !tag && !digest && strings.HasPrefix(repo, reference.Domain(r)+"/") && len(repo) <= 200
}
func (c Command) Validate() error {
	if c.Action == "build-preview" && c.Preview == nil {
		return errors.New("missing preview build authority")
	}
	if c.Preview != nil && (c.Action != "build-preview" || c.Preview.RequestID == uuid.Nil || c.Preview.Generation < 1 || c.Preview.Number < 1 || c.Preview.Number > 1000000000 || c.Preview.HeadRepository < 1 || c.Build == nil || len(c.Build.Secrets) > 0 || c.Registry != nil || c.Build.AutoDeploy || c.Build.AutoBuild || c.Build.FallbackRunnerID != nil || c.Build.RegistryID != nil || c.Build.Budget == nil) {
		return errors.New("invalid preview build")
	}
	if !ValidCommit(c.Commit) || !ValidBranch(c.Branch) || !ValidRepository(c.Connection.Repository) || c.RevisionID == uuid.Nil || c.UnitID == uuid.Nil {
		return errors.New("invalid pipeline identity")
	}
	switch c.Action {
	case "import":
		if c.Source == nil {
			return errors.New("missing source")
		}
		return c.Source.Validate()
	case "submit":
		if c.SubmissionID == uuid.Nil || c.Source == nil || c.Source.Validate() != nil || c.Bundle == nil {
			return errors.New("missing source bundle")
		}
		return c.Bundle.Validate()
	case "build", "build-preview":
		if c.Build == nil || c.AppID == uuid.Nil || len(c.InputHash) != 64 {
			return errors.New("missing build inputs")
		}
		if c.CompareCommit != "" && (!ValidCommit(c.CompareCommit) || len(c.Build.WatchPaths) == 0) {
			return errors.New("invalid watched-build baseline")
		}
		if c.CacheScope != "" && (!ValidCacheID(c.CacheScope) || !c.Build.CanCache()) {
			return errors.New("invalid project cache scope")
		}
		if c.Build.UsesRailpack() {
			if c.Railpack == nil || *c.Railpack != RailpackFor(c.Build.Architecture) {
				return errors.New("unsupported frozen Railpack inputs")
			}
		} else if c.Railpack != nil {
			return errors.New("Railpack inputs require the Railpack builder")
		}
		return c.Build.Validate()
	default:
		return errors.New("unsupported pipeline action")
	}
}

func EncryptionIdentity(ws, id uuid.UUID, kind string) string {
	return "pipeline:" + kind + ":" + ws.String() + ":" + id.String()
}

func (c Command) IsBuild() bool { return c.Action == "build" || c.Action == "build-preview" }
