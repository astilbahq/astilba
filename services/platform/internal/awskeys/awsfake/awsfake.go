// Package awsfake is an in-memory AWS for tests: the IAM, STS and S3 calls awskeys makes, with
// SigV4 signatures checked against the keys it issued, and IAM policies evaluated well enough to
// show what a minted key can and cannot reach. It holds no real credentials and calls nothing.
package awsfake

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"

	"slices"
	"strings"
	"sync"
	"time"
)

// Account is the fake AWS account's ID.
const Account = "123456789012"

// Admin is an IAM user whose key manages minted users, as the documented admin policy allows.
type Admin struct {
	// Name and Path are the admin user's own.
	Name, Path string
	// Account is the AWS account the admin user lives in, if not the fake's Account. Only who its
	// key answers as changes: a key from another account sees the same users.
	Account string
	// Within confines user actions to users under this path, as the admin policy's resources do.
	Within string
	// Boundary is the boundary the admin policy requires on the users it creates and edits.
	Boundary string
	// Denied actions fail with AccessDenied, as when the admin policy is detached.
	Denied map[string]bool
}

// AWS is the fake. Use New.
type AWS struct {
	*httptest.Server
	mu       sync.Mutex
	users    map[string]*user
	keys     map[string]*key
	sessions map[string]*key
	admins   map[string]Admin
	policies map[string]string
	buckets  map[string]string
	objects  map[string]map[string][]byte
	failures map[string][]string
	calls    map[string]int
	// Lag is how many requests an access key issued from now on fails with InvalidAccessKeyId
	// before IAM has propagated it.
	Lag int
	// Before runs before each call is handled, outside the fake's lock, with the call's action.
	Before func(action string)
	serial int
}

type user struct {
	name, path, boundary, account string
	tags                          map[string]string
	policies                      map[string]string
	created                       time.Time
}

type key struct {
	id, secret, token, user string
	uses, lag               int
}

// New starts a fake with no users and no buckets.
func New() *AWS {
	a := &AWS{users: map[string]*user{}, keys: map[string]*key{}, sessions: map[string]*key{}, admins: map[string]Admin{}, policies: map[string]string{}, buckets: map[string]string{}, objects: map[string]map[string][]byte{}, failures: map[string][]string{}, calls: map[string]int{}}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	return a
}

// Credential is a key the fake issued.
type Credential struct{ AccessKeyID, SecretAccessKey, SessionToken string }

// AddAdmin creates an admin user and returns its access key.
func (a *AWS) AddAdmin(admin Admin) Credential {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users[admin.Name] = &user{name: admin.Name, path: admin.Path, account: admin.Account, tags: map[string]string{}, policies: map[string]string{}, created: time.Now()}
	k := a.issue(admin.Name)
	a.admins[admin.Name] = admin
	return Credential{AccessKeyID: k.id, SecretAccessKey: k.secret}
}

// SetAdmin replaces an admin's permissions.
func (a *AWS) SetAdmin(admin Admin) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.admins[admin.Name] = admin
}

// AddPolicy creates a customer-managed policy and returns its ARN.
func (a *AWS) AddPolicy(name, document string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	arn := "arn:aws:iam::" + Account + ":policy/" + name
	a.policies[arn] = document
	return arn
}

// AddBucket creates a bucket in region.
func (a *AWS) AddBucket(name, region string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.buckets[name] = region
	a.objects[name] = map[string][]byte{}
}

// Fail makes the next calls of action fail with these error codes, one each.
func (a *AWS) Fail(action string, codes ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures[action] = append(a.failures[action], codes...)
}

// Calls is how many times action was called.
func (a *AWS) Calls(action string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[action]
}

// Users lists the users under prefix.
func (a *AWS) Users(prefix string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, u := range a.users {
		if strings.HasPrefix(u.path, prefix) {
			out = append(out, u.name)
		}
	}
	slices.Sort(out)
	return out
}

// Keys lists the access key IDs a user has.
func (a *AWS) Keys(name string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, k := range a.keys {
		if k.user == name {
			out = append(out, k.id)
		}
	}
	slices.Sort(out)
	return out
}

// Policy is a user's inline policy, or "".
func (a *AWS) Policy(name, policy string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if u := a.users[name]; u != nil {
		return u.policies[policy]
	}
	return ""
}

// Tags are a user's tags.
func (a *AWS) Tags(name string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if u := a.users[name]; u != nil {
		return maps.Clone(u.tags)
	}
	return nil
}

// Object is an object's content and whether it exists.
func (a *AWS) Object(bucket, name string) ([]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.objects[bucket][name]
	return b, ok
}

// PutObject stores an object directly, as another writer would.
func (a *AWS) PutObject(bucket, name string, content []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[bucket][name] = content
}

// CreateUser makes a user directly, as someone else holding IAM rights would, with an access key.
func (a *AWS) CreateUser(name, userPath, boundary string, tags map[string]string, policy string) Credential {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users[name] = &user{name: name, path: userPath, boundary: boundary, tags: maps.Clone(tags), policies: map[string]string{}, created: time.Now()}
	if policy != "" {
		a.users[name].policies["policy"] = policy
	}
	k := a.issue(name)
	return Credential{AccessKeyID: k.id, SecretAccessKey: k.secret}
}

// IssueKey gives an existing user another access key, as someone with IAM rights could.
func (a *AWS) IssueKey(name string) Credential {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := a.issue(name)
	return Credential{AccessKeyID: k.id, SecretAccessKey: k.secret}
}

// DeleteUser removes a user and its keys directly, as someone with IAM rights would.
func (a *AWS) DeleteUser(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.users, name)
	for id, k := range a.keys {
		if k.user == name {
			delete(a.keys, id)
		}
	}
}

func (a *AWS) issue(name string) *key {
	a.serial++
	k := &key{id: fmt.Sprintf("AKIAFAKE%012d", a.serial), secret: random(), user: name, lag: a.Lag}
	a.keys[k.id] = k
	return k
}

func random() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
