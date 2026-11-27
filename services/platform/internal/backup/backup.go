package backup

import (
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

const ResticVersion = "0.19.1"
const Recipe = 1
const QuiescedFiles = "quiesced-files"

type Destination struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Root     string    `json:"root"`
	Endpoint string    `json:"endpoint"`
	Bucket   string    `json:"bucket"`
	Region   string    `json:"region"`
	// Prefix is where in an S3 bucket the destination's repositories are, so its keys can be
	// limited to it. Destinations without one keep theirs at the bucket's root.
	Prefix string `json:"prefix,omitempty"`
}

// bucketPattern is an S3 destination's bucket.
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// prefixPattern is a destination's prefix: names of letters, digits, '.', '_' and '-' that start
// with a letter or digit, joined by '/'.
var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// ValidPrefix says whether prefix can be an S3 destination's. A prefix can't begin with astilba,
// where destinations without one keep their repositories, or keys limited to it would reach them.
func ValidPrefix(prefix string) bool {
	return len(prefix) <= 256 && prefixPattern.MatchString(prefix) && strings.Split(prefix, "/")[0] != "astilba"
}

// scope is the part of d's bucket its repositories are under, which its keys are limited to.
func (d Destination) scope() string {
	if d.Prefix == "" {
		return "astilba"
	}
	return d.Prefix
}

// Repository is restic's repository for policy p's backups on d.
func (d Destination) Repository(p Policy) string {
	if d.Kind == "filesystem" {
		return filepath.Join(d.Root, p.WorkspaceID.String(), p.ID.String(), "repository")
	}
	key := "astilba/" + p.WorkspaceID.String() + "/" + p.ID.String()
	if d.Prefix != "" {
		key = d.Prefix + "/" + key
	}
	return "s3:" + d.Endpoint + "/" + d.Bucket + "/" + key
}

type Access struct {
	DatabasePassword string `json:"databasePassword"`
	AccessKey        string `json:"accessKey"`
	SecretKey        string `json:"secretKey"`
	Password         string `json:"password"`
}
type Schedule struct {
	Hour            int    `json:"hour"`
	Minute          int    `json:"minute"`
	Timezone        string `json:"timezone"`
	Kind            string `json:"kind,omitempty"`
	Expression      string `json:"expression,omitempty"`
	IntervalMinutes int    `json:"intervalMinutes,omitempty"`
}
type Policy struct {
	Coordinated    bool            `json:"coordinated,omitempty"`
	ID             uuid.UUID       `json:"id"`
	Name           string          `json:"name"`
	WorkspaceID    uuid.UUID       `json:"workspaceId"`
	EnvironmentID  uuid.UUID       `json:"environmentId"`
	ServerID       uuid.UUID       `json:"serverId"`
	DestinationID  uuid.UUID       `json:"destinationId"`
	ResourceID     uuid.UUID       `json:"resourceId"`
	Generation     int64           `json:"generation"`
	Method         string          `json:"method"`
	Schedule       Schedule        `json:"schedule"`
	TimeoutSeconds int             `json:"timeoutSeconds,omitempty"`
	Retain         int             `json:"retain"`
	Enabled        bool            `json:"enabled"`
	Command        []string        `json:"command,omitempty"`
	Plan           deployment.Plan `json:"plan"`
}
type Installation struct {
	Policy       Policy      `json:"policy"`
	Destination  Destination `json:"destination"`
	Access       Access      `json:"access"`
	RepositoryID string      `json:"repositoryId"`
	Next         time.Time   `json:"next"`
}
type Command struct {
	Action       string       `json:"action"`
	Installation Installation `json:"installation"`
	ScheduledAt  *time.Time   `json:"scheduledAt,omitempty"`
	RunID        uuid.UUID    `json:"runId"`
	Snapshot     string       `json:"snapshot"`
	TargetID     uuid.UUID    `json:"targetId"`
}

func (c Command) Digest() string {
	c.Installation.Access = Access{}
	c.Installation.RepositoryID = ""
	c.Installation.Next = time.Time{}
	return Hash(c)
}
func Hash(v any) string { return stablejson.Hash(v) }

type Role struct {
	Name string `json:"name"`
}
type Manifest struct {
	Recipe        int       `json:"recipe"`
	PolicyID      uuid.UUID `json:"policyId"`
	RunID         uuid.UUID `json:"runId"`
	EnvironmentID uuid.UUID `json:"environmentId"`
	ResourceID    uuid.UUID `json:"resourceId"`
	Method        string    `json:"method"`
	Image         string    `json:"image"`
	Database      string    `json:"database"`
	Username      string    `json:"username"`
	CreatedAt     time.Time `json:"createdAt"`
	Roles         []Role    `json:"roles"`
	Extensions    []string  `json:"extensions"`
	SHA256        string    `json:"sha256"`
	Bytes         int64     `json:"bytes"`
	// DumpSettings are the value of each of DumpSettings that the dump ran with. Manifests
	// written before they were recorded leave them out. Only a snapshot's own manifest holds them:
	// management doesn't use them, and a controller from before them refuses a report with them.
	DumpSettings map[string]string `json:"dumpSettings,omitempty"`
}

// Extensions are the only extensions a database the recipe backs up may hold.
var Extensions = []string{"hstore", "plpgsql"}

// DumpSettings are the settings that pg_dump leaves to its session and that change what it writes:
// how timestamps with a time zone, bytea and money values print, whether definitions the server
// prints quote every identifier, the dump's encoding, and how its literals are quoted.
var DumpSettings = []string{"TimeZone", "bytea_output", "lc_monetary", "quote_all_identifiers", "client_encoding", "standard_conforming_strings"}

type Run struct {
	ManifestHash string     `json:"manifestHash"`
	ID           uuid.UUID  `json:"id"`
	PolicyID     uuid.UUID  `json:"policyId"`
	Generation   int64      `json:"generation"`
	ScheduledAt  time.Time  `json:"scheduledAt"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	State        string     `json:"state"`
	Note         string     `json:"note"`
	Snapshot     string     `json:"snapshot"`
	Verified     bool       `json:"verified"`
	Protected    bool       `json:"protected"`
	Pruned       bool       `json:"pruned"`
	ExitCode     *int       `json:"exitCode"`
	Manifest     *Manifest  `json:"manifest"`
	TargetID     uuid.UUID  `json:"targetId"`
	// RestoreImage is the PostgreSQL image whose server filled the restore target, which may be a
	// later release of the archive's major. Runs before it was recorded restored with the
	// archive's own image.
	RestoreImage string `json:"restoreImage,omitempty"`
	// RestoreRoles is the role model of a logical restore target's cluster: domain.PostgresOwner
	// from an agent that restores as the database user. A run without it made the database user
	// the target's superuser.
	RestoreRoles string `json:"restoreRoles,omitempty"`
}
type Status struct {
	Enabled    bool      `json:"enabled"`
	PolicyID   uuid.UUID `json:"policyId"`
	Generation int64     `json:"generation"`
	Next       time.Time `json:"next"`
	Runs       []Run     `json:"runs"`
}

var snapshotPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func SnapshotID(s string) bool { return snapshotPattern.MatchString(s) }
func ValidateDestination(d Destination, a Access) error {
	if d.ID == uuid.Nil || len(d.Name) < 1 || len(d.Name) > 80 || strings.ContainsAny(d.Name, "\x00\r\n") {
		return errors.New("invalid destination")
	}
	if strings.ContainsAny(a.AccessKey+a.SecretKey+a.Password, "\x00\r\n") || len(a.AccessKey) > 512 || len(a.SecretKey) > 8192 {
		return errors.New("invalid credential")
	}
	switch d.Kind {
	case "filesystem":
		if !filepath.IsAbs(d.Root) || filepath.Clean(d.Root) != d.Root || d.Root == "/" || d.Endpoint != "" || d.Bucket != "" || d.Prefix != "" || a.AccessKey != "" || a.SecretKey != "" {
			return errors.New("invalid filesystem destination")
		}
	case "s3":
		if !validEndpoint(d.Endpoint) || d.Root != "" || !bucketPattern.MatchString(d.Bucket) || awsAlias(d) || a.AccessKey == "" || a.SecretKey == "" || len(d.Region) > 80 || strings.ContainsAny(d.Region, "\x00\r\n") || d.Prefix != "" && !ValidPrefix(d.Prefix) {
			return errors.New("invalid S3 destination")
		}
	default:
		return errors.New("invalid destination kind")
	}
	return nil
}
func (p Policy) Validate() error {
	if p.ID == uuid.Nil || p.WorkspaceID == uuid.Nil || p.EnvironmentID == uuid.Nil || p.ServerID == uuid.Nil || p.ResourceID == uuid.Nil || p.Generation < 1 || p.TimeoutSeconds < 0 || p.TimeoutSeconds > 1800 || p.Name == "" || len(p.Name) > 80 || p.Retain < 1 || p.Retain > 100 || p.Schedule.Validate() != nil || p.Plan.Validate(true) != nil || p.Plan.EnvironmentID != p.EnvironmentID || p.Plan.ServerID != p.ServerID {
		return errors.New("invalid backup policy")
	}
	if p.Method != "postgres-logical" && p.Method != QuiescedFiles && p.Method != "container-job" {
		return errors.New("unsupported consistency method")
	}
	if p.Method == "container-job" && (len(p.Command) < 1 || len(p.Command) > 64) {
		return errors.New("job command required")
	}
	if p.Method != "container-job" && p.DestinationID == uuid.Nil {
		return errors.New("destination required")
	}
	if p.Method != "container-job" && len(p.Command) > 0 {
		return errors.New("backup cannot have job command")
	}
	for _, s := range p.Command {
		if len(s) > 4096 || strings.ContainsRune(s, 0) {
			return errors.New("invalid job command")
		}
	}
	return nil
}
func RunID(p Policy, at time.Time) uuid.UUID {
	return uuid.NewSHA1(p.ID, []byte(stringInt(p.Generation)+":"+at.UTC().Format(time.RFC3339)))
}
func stringInt(n int64) string { b, _ := stablejson.Marshal(n); return string(b) }

// Only catalogued snapshots can be forgotten. Unknown snapshots, active restores
// and the newest demonstrated recovery point are never retention candidates.
func Prunable(runs []Run, retain int) []string {
	snapshots := map[string]Run{}
	for _, r := range runs {
		if r.State != "succeeded" || r.Pruned || !SnapshotID(r.Snapshot) {
			continue
		}
		old, exists := snapshots[r.Snapshot]
		if exists {
			r.Protected = r.Protected || old.Protected
			r.Verified = r.Verified || old.Verified
		}
		snapshots[r.Snapshot] = r
	}
	list := []Run{}
	for _, r := range snapshots {
		list = append(list, r)
	}
	instant := func(r Run) time.Time {
		if r.Manifest != nil {
			return r.Manifest.CreatedAt
		}
		return r.ScheduledAt
	}
	sort.Slice(list, func(i, j int) bool { return instant(list[i]).After(instant(list[j])) })
	verified := ""
	for _, r := range list {
		if r.Verified {
			verified = r.Snapshot
			break
		}
	}
	out := []string{}
	for i, r := range list {
		if i >= retain && !r.Protected && r.Snapshot != verified {
			out = append(out, r.Snapshot)
		}
	}
	return out
}

func (p Policy) RequiresQuiescence() bool { return p.Method == QuiescedFiles }

// CoordinatedDue reports whether the management sweep owes p an occurrence at now.
// Agents skip these policies in their own schedule, so nothing else would start it.
func (p Policy) CoordinatedDue(status Status, now time.Time) bool {
	return p.Coordinated && p.RequiresQuiescence() && p.Enabled && status.Enabled && status.PolicyID == p.ID && status.Generation == p.Generation && !status.Next.IsZero() && !now.Before(status.Next)
}
