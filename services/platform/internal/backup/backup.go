package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

const ResticVersion = "0.19.1"
const Recipe = 1

type Destination struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Root     string    `json:"root"`
	Endpoint string    `json:"endpoint"`
	Bucket   string    `json:"bucket"`
	Region   string    `json:"region"`
}
type Access struct {
	DatabasePassword string `json:"databasePassword"`
	AccessKey        string `json:"accessKey"`
	SecretKey        string `json:"secretKey"`
	Password         string `json:"password"`
}
type Schedule struct {
	Hour     int    `json:"hour"`
	Minute   int    `json:"minute"`
	Timezone string `json:"timezone"`
}
type Policy struct {
	ID            uuid.UUID       `json:"id"`
	Name          string          `json:"name"`
	WorkspaceID   uuid.UUID       `json:"workspaceId"`
	EnvironmentID uuid.UUID       `json:"environmentId"`
	ServerID      uuid.UUID       `json:"serverId"`
	DestinationID uuid.UUID       `json:"destinationId"`
	ResourceID    uuid.UUID       `json:"resourceId"`
	Generation    int64           `json:"generation"`
	Method        string          `json:"method"`
	Schedule      Schedule        `json:"schedule"`
	Retain        int             `json:"retain"`
	Enabled       bool            `json:"enabled"`
	Command       []string        `json:"command,omitempty"`
	Plan          deployment.Plan `json:"plan"`
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
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

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
}
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
		if !filepath.IsAbs(d.Root) || filepath.Clean(d.Root) != d.Root || d.Root == "/" || d.Endpoint != "" || d.Bucket != "" || a.AccessKey != "" || a.SecretKey != "" {
			return errors.New("invalid filesystem destination")
		}
	case "s3":
		u, err := url.Parse(d.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || d.Root != "" || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(d.Bucket) || a.AccessKey == "" || a.SecretKey == "" || len(d.Region) > 80 || strings.ContainsAny(d.Region, "\x00\r\n") {
			return errors.New("invalid S3 destination")
		}
	default:
		return errors.New("invalid destination kind")
	}
	return nil
}
func (s Schedule) Validate() error {
	if s.Timezone == "Local" || s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 || len(s.Timezone) > 100 {
		return errors.New("invalid schedule")
	}
	_, err := time.LoadLocation(s.Timezone)
	return err
}

// Each local date runs once, at the first occurrence of a repeated time. A
// nonexistent wall time is skipped. UTC cursors never move backwards.
func (s Schedule) Next(after time.Time) time.Time {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}
	}
	start := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for t := start; t.Before(start.Add(8 * 24 * time.Hour)); t = t.Add(time.Minute) {
		local := t.In(loc)
		if local.Hour() != s.Hour || local.Minute() != s.Minute {
			continue
		}
		repeated := false
		for prior := t.Add(-3 * time.Hour); prior.Before(t); prior = prior.Add(time.Minute) {
			earlier := prior.In(loc)
			if earlier.Year() == local.Year() && earlier.YearDay() == local.YearDay() && earlier.Hour() == s.Hour && earlier.Minute() == s.Minute {
				repeated = true
				break
			}
		}
		if repeated {
			continue
		}
		return t
	}
	return time.Time{}
}
func (p Policy) Validate() error {
	if p.ID == uuid.Nil || p.WorkspaceID == uuid.Nil || p.EnvironmentID == uuid.Nil || p.ServerID == uuid.Nil || p.ResourceID == uuid.Nil || p.Generation < 1 || p.Name == "" || len(p.Name) > 80 || p.Retain < 1 || p.Retain > 100 || p.Schedule.Validate() != nil || p.Plan.Validate(true) != nil || p.Plan.EnvironmentID != p.EnvironmentID || p.Plan.ServerID != p.ServerID {
		return errors.New("invalid backup policy")
	}
	if p.Method != "postgres-logical" && p.Method != "quiesced-files" && p.Method != "container-job" {
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
func stringInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

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
