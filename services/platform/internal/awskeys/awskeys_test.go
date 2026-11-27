package awskeys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/awskeys/awsfake"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// boundary is the documented permissions boundary: object access in the backup bucket only.
const boundary = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListBucket"],"Resource":["arn:aws:s3:::backups","arn:aws:s3:::backups/*"]}]}`

type fixture struct {
	aws    *awsfake.AWS
	client *Client
	admin  Credential
	spec   Spec
}

func setup(t *testing.T) fixture {
	t.Helper()
	fake := awsfake.New()
	t.Cleanup(fake.Close)
	arn := fake.AddPolicy("astilba-backup-boundary", boundary)
	path := Path("8d2c3c51-6a1a-4d2e-9a43-5f0c1b7f0a11")
	admin := fake.AddAdmin(awsfake.Admin{Name: "astilba-admin", Path: "/", Within: path, Boundary: arn})
	fake.AddBucket("backups", "eu-west-2")
	fake.AddBucket("elsewhere", "eu-west-2")
	client, err := NewFixture(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{fake, client, credential(admin), Spec{User: "astilba-k1", Path: path, Boundary: arn, Bucket: "backups", Region: "eu-west-2", Scope: "servers/one/", Tags: map[string]string{"astilba:server": "one"}}}
}

func credential(c awsfake.Credential) Credential {
	return Credential{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey}
}

func (f fixture) s3(key Credential) *s3.Client { return f.client.s3Client(key, "eu-west-2") }

func reach(t *testing.T, client *s3.Client, bucket, name string) error {
	t.Helper()
	ctx := context.Background()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(name), Body: strings.NewReader("snapshot")}); err != nil {
		return classify(err)
	}
	if _, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(name)}); err != nil {
		return classify(err)
	}
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(name)})
	return classify(err)
}

func TestMintedKeyReachesOnlyItsScope(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.Probe(ctx, key, f.spec); err != nil {
		t.Fatal(err)
	}
	client := f.s3(key)
	if err = reach(t, client, "backups", "servers/one/data/1"); err != nil {
		t.Fatalf("inside its scope: %v", err)
	}
	f.aws.PutObject("backups", "servers/two/data/1", []byte("other"))
	for _, name := range []string{"servers/two/data/1", "servers/one", "servers/onex/data/1", "data/1"} {
		if err = reach(t, client, "backups", name); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err = reach(t, client, "elsewhere", "servers/one/data/1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("another bucket: %v", err)
	}
	for _, prefix := range []string{"servers/", "servers/two/", ""} {
		_, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("backups"), Prefix: aws.String(prefix)})
		if !errors.Is(classify(err), ErrDenied) {
			t.Fatalf("listing %q: %v", prefix, err)
		}
	}
	if _, ok := f.aws.Object("backups", "servers/two/data/1"); !ok {
		t.Fatal("another server's object was deleted")
	}
	if _, err = f.client.iamClient(key).ListUsers(ctx, nil); !errors.Is(classify(err), ErrDenied) {
		t.Fatalf("IAM: %v", err)
	}
	if got := f.aws.Tags("astilba-k1"); got["astilba:server"] != "one" {
		t.Fatalf("tags %v", got)
	}
}

func TestSessionCredentialsDoNothing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	// Any IAM user can mint session credentials, and they would outlive the access key.
	out, err := f.client.stsClient(key).GetSessionToken(ctx, &sts.GetSessionTokenInput{})
	if err != nil {
		t.Fatal(err)
	}
	c := out.Credentials
	session := s3.NewFromConfig(aws.Config{Region: "eu-west-2", HTTPClient: f.client.http, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: aws.ToString(c.AccessKeyId), SecretAccessKey: aws.ToString(c.SecretAccessKey), SessionToken: aws.ToString(c.SessionToken)}, nil
	})}, func(o *s3.Options) { o.BaseEndpoint, o.UsePathStyle = aws.String(f.aws.URL), true })
	if err = reach(t, session, "backups", "servers/one/data/1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("session credentials: %v", err)
	}
}

func TestRevokeTakesTheKeyAway(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.Revoke(ctx, f.admin, f.spec.Path, f.spec.User); err != nil {
		t.Fatal(err)
	}
	if users := f.aws.Users("/astilba/"); len(users) != 0 {
		t.Fatalf("users left: %v", users)
	}
	if err = reach(t, f.s3(key), "backups", "servers/one/data/1"); !errors.Is(err, ErrCredential) {
		t.Fatalf("revoked key: %v", err)
	}
	// Under the path-scoped admin policy IAM refuses the missing user by name.
	if err = f.client.Revoke(ctx, f.admin, f.spec.Path, f.spec.User); !errors.Is(err, ErrUnlisted) {
		t.Fatalf("revoking again: %v", err)
	}
}

func TestRevokeDeniesFirstAndResumes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	f.aws.Fail("DeleteAccessKey", "ServiceFailure", "ServiceFailure", "ServiceFailure")
	if err = f.client.Revoke(ctx, f.admin, f.spec.Path, f.spec.User); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failing revocation: %v", err)
	}
	// The key still exists, but the deny-all policy already stops it.
	if len(f.aws.Keys(f.spec.User)) != 1 {
		t.Fatal("the key should survive the failed call")
	}
	if err = reach(t, f.s3(key), "backups", "servers/one/data/1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("half-revoked key: %v", err)
	}
	if err = f.client.Revoke(ctx, f.admin, f.spec.Path, f.spec.User); err != nil {
		t.Fatal(err)
	}
	if len(f.aws.Users("/astilba/")) != 0 {
		t.Fatal("the user should be gone")
	}
}

func TestMintAgainReplacesALostKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.aws.Fail("CreateAccessKey", "ServiceFailure", "ServiceFailure", "ServiceFailure")
	if _, err := f.client.Mint(ctx, f.admin, f.spec); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failing mint: %v", err)
	}
	first, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if keys := f.aws.Keys(f.spec.User); len(keys) != 1 || keys[0] != second.AccessKeyID {
		t.Fatalf("keys %v", keys)
	}
	if err = reach(t, f.s3(first), "backups", "servers/one/data/1"); !errors.Is(err, ErrCredential) {
		t.Fatalf("the lost key: %v", err)
	}
	other := f.spec
	other.Tags = map[string]string{"astilba:server": "two"}
	if _, err = f.client.Mint(ctx, f.admin, other); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a user with the same name but another server's tags: %v", err)
	}
}

func TestAdminPolicyConfinesTheAdmin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	outside := f.spec
	outside.Path = Path("another")
	if _, err := f.client.Mint(ctx, f.admin, outside); !errors.Is(err, ErrDenied) {
		t.Fatalf("another workspace's path: %v", err)
	}
	unbounded := f.spec
	unbounded.Boundary = f.aws.AddPolicy("wide", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)
	if _, err := f.client.Mint(ctx, f.admin, unbounded); !errors.Is(err, ErrDenied) {
		t.Fatalf("another boundary: %v", err)
	}
	if err := f.client.CanList(ctx, f.admin, f.spec.Path); err != nil {
		t.Fatal(err)
	}
	f.aws.SetAdmin(awsfake.Admin{Name: "astilba-admin", Path: "/", Within: f.spec.Path, Boundary: f.spec.Boundary, Denied: map[string]bool{"ListUsers": true}})
	if err := f.client.CanList(ctx, f.admin, f.spec.Path); !errors.Is(err, ErrDenied) {
		t.Fatalf("a detached admin policy: %v", err)
	}
}

func TestIdentify(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, err := f.client.Identify(ctx, f.admin)
	if err != nil || id.Account != awsfake.Account || id.ARN != "arn:aws:iam::"+awsfake.Account+":user/astilba-admin" {
		t.Fatalf("%+v %v", id, err)
	}
	inside := credential(f.aws.AddAdmin(awsfake.Admin{Name: "inner", Path: "/astilba/x/"}))
	if _, err = f.client.Identify(ctx, inside); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("an admin under /astilba/: %v", err)
	}
	wrong := f.admin
	wrong.SecretAccessKey = strings.Repeat("0", 40)
	if _, err = f.client.Identify(ctx, wrong); !errors.Is(err, ErrCredential) {
		t.Fatalf("a wrong secret: %v", err)
	}
}

func TestProbeWaitsForPropagation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.aws.Lag = 3
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.Probe(ctx, key, f.spec); err != nil {
		t.Fatal(err)
	}
	missing := f.spec
	missing.Bucket = "missing"
	if err = f.client.Probe(ctx, key, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing bucket: %v", err)
	}
	f.aws.Lag = 1000
	key, err = f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err = f.client.Probe(short, key, f.spec); !errors.Is(err, ErrCredential) {
		t.Fatalf("a key that never propagates: %v", err)
	}
}

func TestUsersListsTagsAndBoundaries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, name := range []string{"astilba-k1", "astilba-k2", "astilba-k3"} {
		spec := f.spec
		spec.User = name
		if _, err := f.client.Mint(ctx, f.admin, spec); err != nil {
			t.Fatal(err)
		}
	}
	users, err := f.client.Users(ctx, f.admin, f.spec.Path, 2)
	if err != nil || len(users) != 2 || users[0].Name != "astilba-k1" || users[0].Boundary != f.spec.Boundary || users[0].Tags["astilba:server"] != "one" {
		t.Fatalf("%+v %v", users, err)
	}
}

func TestPolicyIsStable(t *testing.T) {
	got := Policy(Spec{Bucket: "backups", Scope: "servers/one/"})
	want := `{"Version":"2012-10-17","Statement":[{"Sid":"Objects","Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload"],"Resource":["arn:aws:s3:::backups/servers/one/*"]},{"Sid":"List","Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::backups"],"Condition":{"StringLike":{"s3:prefix":"servers/one/*"}}},{"Sid":"NoSessions","Effect":"Deny","Action":["*"],"Resource":["*"],"Condition":{"Null":{"aws:TokenIssueTime":"false"}}}]}`
	if got != want {
		t.Fatal(got)
	}
}

func TestFixtureIsLoopbackOnly(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1:1", "http://example.com", "http://10.0.0.1:80", "http://127.0.0.1:1/path", "http://u:p@127.0.0.1:1"} {
		if _, err := NewFixture(base); err == nil {
			t.Fatal(base)
		}
	}
}

func TestValidation(t *testing.T) {
	for r, ok := range map[string]bool{"eu-west-2": true, "us-east-1": true, "us-gov-west-1": false, "cn-north-1": false, "eu-west": false, "EU-WEST-2": false} {
		if ValidRegion(r) != ok {
			t.Fatal(r)
		}
	}
	if !ValidBoundary("arn:aws:iam::123456789012:policy/astilba/boundary", "123456789012") || ValidBoundary("arn:aws:iam::123456789012:policy/b", "210987654321") || ValidBoundary("arn:aws-cn:iam::123456789012:policy/b", "123456789012") {
		t.Fatal("boundary")
	}
	if (Credential{AccessKeyID: "AKIAFAKE000000000001", SecretAccessKey: "s3cr3t-value-of-forty-characters-long!!"}).Valid() != true || (Credential{AccessKeyID: "akia", SecretAccessKey: "x"}).Valid() {
		t.Fatal("credential")
	}
}

func TestRevokeKeepsDenyingAUserItCantDelete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// A user made outside management, with a policy of its own that DeleteUser trips over.
	key := f.aws.CreateUser("astilba-found", f.spec.Path, f.spec.Boundary, nil, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]}]}`)
	if err := f.client.Revoke(ctx, f.admin, f.spec.Path, "astilba-found"); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoking a user with another policy: %v", err)
	}
	if len(f.aws.Keys("astilba-found")) != 0 {
		t.Fatal("its access keys should be gone")
	}
	if f.aws.Policy("astilba-found", policyName) != denyAll {
		t.Fatal("the user should keep the deny-all while it can't be deleted")
	}
	if err := reach(t, f.s3(credential(key)), "backups", "servers/one/data/1"); err == nil {
		t.Fatal("the user's old key should do nothing")
	}
}

// A deny-all AWS refuses, as when someone with the admin key filled the user's inline policy
// quota, doesn't keep the user's access keys alive.
func TestRevokeGoesOnWhenTheDenyAllIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	f.aws.Fail("PutUserPolicy", "LimitExceeded")
	if err = f.client.Revoke(ctx, f.admin, f.spec.Path, f.spec.User); err != nil {
		t.Fatal(err)
	}
	if users := f.aws.Users("/astilba/"); len(users) != 0 {
		t.Fatalf("users left: %v", users)
	}
	if err = reach(t, f.s3(key), "backups", "servers/one/data/1"); !errors.Is(err, ErrCredential) {
		t.Fatalf("revoked key: %v", err)
	}
}

// A user that doesn't exist is answered by name with NoSuchEntity, or, as IAM is reported to do
// under a path-scoped admin policy, with AccessDenied. Either way it is settled as missing, and a
// refusal for a user that is listed stays a refusal.
func TestAMissingUserIsMissingWhateverIAMAnswers(t *testing.T) {
	for _, report := range []bool{false, true} {
		f := setup(t)
		ctx := context.Background()
		f.aws.ReportMissingUsers(report)
		gone := ErrUnlisted
		if report {
			gone = ErrNotFound
		}
		if err := f.client.Revoke(ctx, f.admin, f.spec.Path, "astilba-never-made"); !errors.Is(err, gone) {
			t.Fatalf("reporting missing users %v, revoking one: %v", report, err)
		}
		if exists, err := f.client.UserExists(ctx, f.admin, f.spec.Path, "astilba-never-made"); err != nil || exists {
			t.Fatalf("reporting missing users %v, a missing user exists %v: %v", report, exists, err)
		}
		if _, err := f.client.Mint(ctx, f.admin, f.spec); err != nil {
			t.Fatal(err)
		}
		if exists, err := f.client.UserExists(ctx, f.admin, f.spec.Path, f.spec.User); err != nil || !exists {
			t.Fatalf("reporting missing users %v, a minted user exists %v: %v", report, exists, err)
		}
		// An admin policy that no longer allows the call refuses a user that is still listed.
		f.aws.SetAdmin(awsfake.Admin{Name: "astilba-admin", Path: "/", Within: f.spec.Path, Boundary: f.spec.Boundary, Denied: map[string]bool{"PutUserPolicy": true, "ListAccessKeys": true, "GetUser": true}})
		if err := f.client.Revoke(ctx, f.admin, f.spec.Path, f.spec.User); !errors.Is(err, ErrDenied) || errors.Is(err, ErrUnlisted) {
			t.Fatalf("reporting missing users %v, revoking a listed user the policy refuses: %v", report, err)
		}
		if _, err := f.client.UserExists(ctx, f.admin, f.spec.Path, f.spec.User); !errors.Is(err, ErrDenied) {
			t.Fatalf("reporting missing users %v, asking about a listed user the policy refuses: %v", report, err)
		}
	}
}

// A minted key works until it is revoked, wherever its user is moved, and no admin policy can
// change that answer.
func TestKeyWorksUntilTheKeyIsGone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key, err := f.client.Mint(ctx, f.admin, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	f.aws.MoveUser(f.spec.User, "/elsewhere/")
	if works, err := f.client.KeyWorks(ctx, key); err != nil || !works {
		t.Fatalf("a moved user's key works %v: %v", works, err)
	}
	f.aws.DeleteUser(f.spec.User)
	if works, err := f.client.KeyWorks(ctx, key); err != nil || works {
		t.Fatalf("a deleted user's key works %v: %v", works, err)
	}
}
