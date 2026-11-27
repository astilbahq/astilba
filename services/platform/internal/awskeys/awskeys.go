// Package awskeys mints and revokes the AWS IAM users that hold backup storage keys, with an
// admin credential an owner supplies. Each minted user reaches one object prefix in one bucket,
// is created under its workspace's IAM path with the account's permissions boundary, and its
// policy denies every request made with temporary credentials, since any IAM user can mint
// session credentials that outlive its access key. See docs/backups.md#managed-keys.
//
// Management calls only endpoints this package fixes: IAM and STS in the aws partition and the
// bucket region's S3 endpoint, through outbound.Client, so no owner input chooses a host. The
// clients are built from an explicit configuration with static credentials, never the SDK's
// default chain, so no environment variable or shared file can redirect a call or lend other
// credentials. Provider error bodies never leave this package: callers get the fixed errors below.
package awskeys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/outbound"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

var (
	// ErrCredential is a credential AWS doesn't accept.
	ErrCredential = errors.New("AWS did not accept the credential")
	// ErrDenied is a call the credential's policy doesn't allow.
	ErrDenied = errors.New("AWS refused the call: the credential's policy does not allow it")
	// ErrNotFound is a user, or a bucket, that doesn't exist.
	ErrNotFound = errors.New("not found at AWS")
	// ErrConflict is a user AWS won't delete while something management didn't add is attached
	// to it, such as a console password, an MFA device, a group or another policy.
	ErrConflict = errors.New("AWS won't delete the user while something else is attached to it")
	// ErrUnlisted is a user AWS refused a call on by name, and that isn't listed under its path.
	// For a user that doesn't exist, IAM may answer a call by name with AccessDenied rather than
	// NoSuchEntity, since it checks the call against the user's ARN without its path, which a
	// path-scoped admin policy doesn't match. ListUsers can lag a create, so only a caller that
	// knows the user, if it exists, is older than that lag may take this as gone.
	ErrUnlisted = errors.New("AWS refused a call on the user, and it isn't listed under its path")
	// ErrUnavailable is any other failure: the network, a timeout or an AWS error.
	ErrUnavailable = errors.New("AWS could not be reached or failed")
	// ErrNotAdmin is a credential that isn't an IAM user in the aws partition outside the
	// /astilba/ path: the root user, a role session, another partition, or a user whose own
	// path is where minted users live, which the admin policy could then rewrite.
	ErrNotAdmin = errors.New("the credential must belong to an IAM user in the aws partition, outside the /astilba/ path")
)

// Credential is an IAM user's long-term access key.
type Credential struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// Valid reports whether c has the shape of an AWS access key.
func (c Credential) Valid() bool {
	return accessKeyID.MatchString(c.AccessKeyID) && len(c.SecretAccessKey) >= 16 && len(c.SecretAccessKey) <= 128 && !strings.ContainsAny(c.SecretAccessKey, " \t\r\n\x00")
}

var (
	accessKeyID = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
	accountID   = regexp.MustCompile(`^[0-9]{12}$`)
	// region matches a commercial region of the aws partition: not GovCloud (us-gov-…), whose
	// partition differs, and not China (cn-…), which is checked separately.
	region = regexp.MustCompile(`^[a-z]{2}-[a-z]+-[0-9]{1,2}$`)
	// boundaryARN names a customer-managed policy in the aws partition.
	boundaryARN = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:policy/[A-Za-z0-9+=,.@_/-]{1,512}$`)
)

// ValidRegion reports whether r is a commercial region of the aws partition.
func ValidRegion(r string) bool {
	return region.MatchString(r) && !strings.HasPrefix(r, "cn-")
}

// ValidBoundary reports whether arn names a customer-managed policy in account.
func ValidBoundary(arn, account string) bool {
	return boundaryARN.MatchString(arn) && strings.HasPrefix(arn, "arn:aws:iam::"+account+":policy/")
}

// Path is the IAM path a workspace's minted users live under.
func Path(workspace string) string { return "/astilba/" + workspace + "/" }

// Identity is who a credential belongs to.
type Identity struct {
	Account string
	ARN     string
}

// Spec is one key to mint.
type Spec struct {
	// User is the IAM user's name, unique for ever, and Path its workspace's path.
	User, Path string
	// Boundary is the permissions boundary every minted user has.
	Boundary string
	Bucket   string
	Region   string
	// Scope is the object prefix the key reaches. It ends with "/".
	Scope string
	// Tags are recorded on the user, so the account check can tell whose it is.
	Tags map[string]string
}

// User is a minted user the account check found.
type User struct {
	Name     string
	Tags     map[string]string
	Boundary string
}

// Client calls AWS. Its zero value is not usable: use New, or NewFixture in tests.
type Client struct {
	http  *http.Client
	iam   string
	sts   string
	s3    func(region string) string
	path  bool
	sleep func(context.Context, time.Duration) error
}

// New is the client production uses, with AWS's own endpoints.
func New() *Client {
	return &Client{
		http:  limited(outbound.Client(false, 20*time.Second)),
		iam:   "https://iam.amazonaws.com",
		sts:   "https://sts.amazonaws.com",
		s3:    func(r string) string { return "https://s3." + r + ".amazonaws.com" },
		sleep: sleep,
	}
}

// NewFixture is a client for a fake AWS at a loopback HTTP base URL, for tests.
func NewFixture(base string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, errors.New("an AWS fixture is a loopback HTTP origin")
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("an AWS fixture is a loopback HTTP origin")
	}
	origin := u.Scheme + "://" + u.Host
	return &Client{
		http:  limited(outbound.Client(true, 20*time.Second)),
		iam:   origin,
		sts:   origin,
		s3:    func(string) string { return origin },
		path:  true,
		sleep: func(ctx context.Context, _ time.Duration) error { return sleep(ctx, time.Millisecond) },
	}, nil
}

// responseLimit bounds what AWS can make management read for one call.
const responseLimit = 1 << 20

type limitedTransport struct{ next http.RoundTripper }

func (t limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(r)
	if err == nil {
		response.Body = struct {
			io.Reader
			io.Closer
		}{io.LimitReader(response.Body, responseLimit), response.Body}
	}
	return response, err
}

func limited(c *http.Client) *http.Client {
	c.Transport = limitedTransport{c.Transport}
	return c
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) config(cred Credential, region string) aws.Config {
	return aws.Config{
		Region:     region,
		HTTPClient: c.http,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: cred.AccessKeyID, SecretAccessKey: cred.SecretAccessKey, Source: "astilba"}, nil
		}),
		RetryMaxAttempts: 3,
	}
}

func (c *Client) iamClient(cred Credential) *iam.Client {
	return iam.NewFromConfig(c.config(cred, "us-east-1"), func(o *iam.Options) { o.BaseEndpoint = aws.String(c.iam) })
}

func (c *Client) stsClient(cred Credential) *sts.Client {
	return sts.NewFromConfig(c.config(cred, "us-east-1"), func(o *sts.Options) { o.BaseEndpoint = aws.String(c.sts) })
}

func (c *Client) s3Client(cred Credential, region string) *s3.Client {
	return s3.NewFromConfig(c.config(cred, region), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(c.s3(region))
		o.UsePathStyle = c.path
	})
}

// classify maps an SDK error to one of this package's errors.
func classify(err error) error {
	if err == nil {
		return nil
	}
	// A call cut short by its deadline stays a timeout, which callers record as one.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchEntity", "NoSuchBucket":
			return ErrNotFound
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
			return ErrDenied
		case "InvalidClientTokenId", "SignatureDoesNotMatch", "InvalidAccessKeyId", "ExpiredToken":
			return ErrCredential
		case "DeleteConflict":
			return ErrConflict
		}
	}
	return ErrUnavailable
}

// Identify returns who cred belongs to. It refuses a credential that isn't an IAM user of the
// aws partition, or one whose user lives under /astilba/.
func (c *Client) Identify(ctx context.Context, cred Credential) (Identity, error) {
	out, err := c.stsClient(cred).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Identity{}, classify(err)
	}
	id := Identity{Account: aws.ToString(out.Account), ARN: aws.ToString(out.Arn)}
	prefix := "arn:aws:iam::" + id.Account + ":user/"
	if !accountID.MatchString(id.Account) || !strings.HasPrefix(id.ARN, prefix) || strings.HasPrefix(strings.TrimPrefix(id.ARN, prefix), "astilba/") {
		return Identity{}, ErrNotAdmin
	}
	return id, nil
}

// CanList checks that cred may list the users under path, which an admin policy that isn't
// attached, or doesn't reach the path, refuses. GetCallerIdentity needs no permission, so it
// can't show this.
func (c *Client) CanList(ctx context.Context, cred Credential, path string) error {
	_, err := c.iamClient(cred).ListUsers(ctx, &iam.ListUsersInput{PathPrefix: aws.String(path), MaxItems: aws.Int32(1)})
	return classify(err)
}

// Policy is the inline policy of a user minted for spec.
func Policy(spec Spec) string {
	type statement struct {
		Sid       string                       `json:"Sid"`
		Effect    string                       `json:"Effect"`
		Action    []string                     `json:"Action"`
		Resource  []string                     `json:"Resource"`
		Condition map[string]map[string]string `json:"Condition,omitempty"`
	}
	bucket := "arn:aws:s3:::" + spec.Bucket
	document := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{"2012-10-17", []statement{
		{Sid: "Objects", Effect: "Allow", Action: []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"}, Resource: []string{bucket + "/" + spec.Scope + "*"}},
		{Sid: "List", Effect: "Allow", Action: []string{"s3:ListBucket"}, Resource: []string{bucket}, Condition: map[string]map[string]string{"StringLike": {"s3:prefix": spec.Scope + "*"}}},
		// Session credentials outlive the access key they were minted with, and no policy can
		// stop a user minting them, so they may do nothing.
		{Sid: "NoSessions", Effect: "Deny", Action: []string{"*"}, Resource: []string{"*"}, Condition: map[string]map[string]string{"Null": {"aws:TokenIssueTime": "false"}}},
	}}
	raw, _ := stablejson.Marshal(document)
	return string(raw)
}

// denyAll is the policy a user being revoked holds until it is deleted.
const denyAll = `{"Version":"2012-10-17","Statement":[{"Sid":"Revoked","Effect":"Deny","Action":["*"],"Resource":["*"]}]}`

const policyName = "astilba-backup"

// Mint creates spec's user, its policy and an access key, and returns the key. A user left by
// an earlier attempt is reused if it is the same one, and any access key it already has is
// deleted first, since its secret was lost with that attempt.
func (c *Client) Mint(ctx context.Context, cred Credential, spec Spec) (Credential, error) {
	if !strings.HasSuffix(spec.Scope, "/") || spec.User == "" || !strings.HasPrefix(spec.Path, "/astilba/") || spec.Bucket == "" || !ValidRegion(spec.Region) {
		return Credential{}, fmt.Errorf("awskeys: invalid spec")
	}
	client := c.iamClient(cred)
	tags := make([]iamtypes.Tag, 0, len(spec.Tags))
	for k, v := range spec.Tags {
		tags = append(tags, iamtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	_, err := client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(spec.User), Path: aws.String(spec.Path), PermissionsBoundary: aws.String(spec.Boundary), Tags: tags})
	var exists *iamtypes.EntityAlreadyExistsException
	if errors.As(err, &exists) {
		if err = c.sameUser(ctx, client, spec); err != nil {
			return Credential{}, err
		}
	} else if err != nil {
		return Credential{}, classify(err)
	}
	if _, err = client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(spec.User), PolicyName: aws.String(policyName), PolicyDocument: aws.String(Policy(spec))}); err != nil {
		return Credential{}, classify(err)
	}
	if err = c.deleteKeys(ctx, client, spec.User); err != nil {
		return Credential{}, err
	}
	out, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(spec.User)})
	if err != nil || out.AccessKey == nil {
		return Credential{}, classify(err)
	}
	return Credential{AccessKeyID: aws.ToString(out.AccessKey.AccessKeyId), SecretAccessKey: aws.ToString(out.AccessKey.SecretAccessKey)}, nil
}

// sameUser checks that an existing user with spec's name is the one an earlier attempt made.
func (c *Client) sameUser(ctx context.Context, client *iam.Client, spec Spec) error {
	out, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(spec.User)})
	if err != nil || out.User == nil {
		return classify(err)
	}
	u := out.User
	if aws.ToString(u.Path) != spec.Path || u.PermissionsBoundary == nil || aws.ToString(u.PermissionsBoundary.PermissionsBoundaryArn) != spec.Boundary {
		return ErrUnavailable
	}
	got := map[string]string{}
	for _, t := range u.Tags {
		got[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	for k, v := range spec.Tags {
		if got[k] != v {
			return ErrUnavailable
		}
	}
	return nil
}

func (c *Client) deleteKeys(ctx context.Context, client *iam.Client, user string) error {
	out, err := client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(user)})
	if err != nil {
		return classify(err)
	}
	for _, k := range out.AccessKeyMetadata {
		if _, err = client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(user), AccessKeyId: k.AccessKeyId}); err != nil && classify(err) != ErrNotFound {
			return classify(err)
		}
	}
	return nil
}

// Probe lists inside spec's scope with key until it works or ctx ends, which checks the bucket,
// its region and the key, through IAM's propagation delay. The key's containment comes from its
// policy; this doesn't try to prove it.
func (c *Client) Probe(ctx context.Context, key Credential, spec Spec) error {
	client := c.s3Client(key, spec.Region)
	wait := 500 * time.Millisecond
	var last error
	for {
		_, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(spec.Bucket), Prefix: aws.String(spec.Scope), MaxKeys: aws.Int32(1)})
		if err == nil {
			return nil
		}
		e := classify(err)
		// A call cut short by the deadline says less than AWS's last answer.
		if ctx.Err() != nil && last != nil {
			return last
		}
		// A key AWS hasn't propagated yet is unknown or denied for a while.
		if e != ErrCredential && e != ErrDenied {
			return e
		}
		last = e
		if c.sleep(ctx, wait) != nil {
			return e
		}
		wait = min(2*wait, 5*time.Second)
	}
}

// Revoke takes user's access away and deletes it: a deny-all policy first, so the user can do
// nothing even while its keys are deleted, then its access keys, its policy and the user. It
// reports gone when the user no longer exists, and ErrUnlisted when IAM refused the user by name
// and doesn't list it under path. A deny-all AWS refuses, for example because someone
// with the admin key filled the user's inline policy quota, doesn't stop the rest: deleting the
// access keys already leaves the user unable to act. A user AWS won't delete gets its deny-all back before
// Revoke reports ErrConflict, so it stays unable to act until it is deleted. The caller must have
// checked that cred belongs to the account the user was minted in, or a missing user proves
// nothing.
func (c *Client) Revoke(ctx context.Context, cred Credential, path, user string) error {
	client := c.iamClient(cred)
	deny := func() error {
		_, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String(policyName), PolicyDocument: aws.String(denyAll)})
		return classify(err)
	}
	denied := deny()
	if errors.Is(denied, ErrDenied) {
		denied = c.unlisted(ctx, client, path, user, denied)
	}
	if errors.Is(denied, ErrNotFound) || errors.Is(denied, ErrUnlisted) {
		return denied
	}
	if err := c.deleteKeys(ctx, client, user); err != nil {
		return errors.Join(denied, err)
	}
	// With its access keys gone the user can sign nothing, deny-all or not.
	if _, err := client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String(policyName)}); err != nil && classify(err) != ErrNotFound {
		return errors.Join(denied, classify(err))
	}
	_, err := client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	if err = classify(err); errors.Is(err, ErrConflict) {
		return errors.Join(err, deny())
	}
	return err
}

// Users lists the users under path, with their tags and boundaries. It stops at limit users.
func (c *Client) Users(ctx context.Context, cred Credential, path string, limit int) ([]User, error) {
	client := c.iamClient(cred)
	var out []User
	pages := iam.NewListUsersPaginator(client, &iam.ListUsersInput{PathPrefix: aws.String(path)})
	for pages.HasMorePages() && len(out) < limit {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, classify(err)
		}
		for _, u := range page.Users {
			if len(out) >= limit {
				break
			}
			got, err := client.GetUser(ctx, &iam.GetUserInput{UserName: u.UserName})
			// A user deleted since the listing is skipped, however IAM answers for it by name.
			err = classify(err)
			if errors.Is(err, ErrDenied) {
				err = c.unlisted(ctx, client, path, aws.ToString(u.UserName), err)
			}
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnlisted) {
				continue
			} else if err != nil || got.User == nil {
				return nil, err
			}
			user := User{Name: aws.ToString(u.UserName), Tags: map[string]string{}}
			if got.User.PermissionsBoundary != nil {
				user.Boundary = aws.ToString(got.User.PermissionsBoundary.PermissionsBoundaryArn)
			}
			for _, t := range got.User.Tags {
				user.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
			out = append(out, user)
		}
	}
	return out, nil
}

// AccessKeys lists the access key IDs a user has.
func (c *Client) AccessKeys(ctx context.Context, cred Credential, user string) ([]string, error) {
	out, err := c.iamClient(cred).ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(user)})
	if err != nil {
		return nil, classify(err)
	}
	ids := make([]string, 0, len(out.AccessKeyMetadata))
	for _, k := range out.AccessKeyMetadata {
		ids = append(ids, aws.ToString(k.AccessKeyId))
	}
	return ids, nil
}

// DeleteAccessKey deletes one of a user's access keys. A key already gone counts as deleted.
func (c *Client) DeleteAccessKey(ctx context.Context, cred Credential, user, id string) error {
	_, err := c.iamClient(cred).DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(user), AccessKeyId: aws.String(id)})
	if err = classify(err); errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// UserExists asks IAM for one user under path by name, which a listing that may lag can't settle
// on its own. A user IAM refuses by name is missing only if it isn't listed either; the caller
// asks only about users old enough to be listed.
func (c *Client) UserExists(ctx context.Context, cred Credential, path, user string) (bool, error) {
	client := c.iamClient(cred)
	_, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(user)})
	if err = classify(err); errors.Is(err, ErrDenied) {
		err = c.unlisted(ctx, client, path, user, err)
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnlisted) {
		return false, nil
	}
	return err == nil, err
}

// unlisted settles a call on user that IAM refused with denied: ErrUnlisted if no user of that
// name is listed under path, and denied if one is, or if the listing fails too.
func (c *Client) unlisted(ctx context.Context, client *iam.Client, path, user string, denied error) error {
	pages := iam.NewListUsersPaginator(client, &iam.ListUsersInput{PathPrefix: aws.String(path)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return errors.Join(denied, classify(err))
		}
		for _, u := range page.Users {
			if aws.ToString(u.UserName) == user {
				return denied
			}
		}
	}
	return ErrUnlisted
}
