package awsfake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

var authorization = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([A-Z0-9]+)/([0-9]{8})/([a-z0-9-]+)/([a-z0-9]+)/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

// caller is who signed a request.
type caller struct {
	key     *key
	session bool
	service string
}

// authenticate checks a request's SigV4 signature against the key it names.
func (a *AWS) authenticate(r *http.Request, body []byte) (caller, string) {
	m := authorization.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		return caller{}, "MissingAuthenticationToken"
	}
	id, region, service, signed := m[1], m[3], m[4], strings.Split(m[5], ";")
	a.mu.Lock()
	k, session := a.keys[id], false
	if k == nil {
		k, session = a.sessions[id], true
	}
	if k != nil && !session && k.uses < k.lag {
		k.uses++
		k = nil
	}
	a.mu.Unlock()
	if k == nil || (session && r.Header.Get("X-Amz-Security-Token") != k.token) || (!session && r.Header.Get("X-Amz-Security-Token") != "") {
		if service == "s3" {
			return caller{}, "InvalidAccessKeyId"
		}
		return caller{}, "InvalidClientTokenId"
	}
	when, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return caller{}, "SignatureDoesNotMatch"
	}
	again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	again.ContentLength = r.ContentLength
	for _, h := range signed {
		if h != "host" && h != "content-length" {
			again.Header[http.CanonicalHeaderKey(h)] = r.Header.Values(h)
		}
	}
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if hash == "" {
		sum := sha256.Sum256(body)
		hash = hex.EncodeToString(sum[:])
	}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = service == "s3" })
	creds := aws.Credentials{AccessKeyID: k.id, SecretAccessKey: k.secret, SessionToken: k.token}
	if err = signer.SignHTTP(context.Background(), creds, again, hash, service, region, when); err != nil || again.Header.Get("Authorization") != r.Header.Get("Authorization") {
		return caller{}, "SignatureDoesNotMatch"
	}
	return caller{key: k, session: session, service: service}, ""
}

func (a *AWS) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	who, code := a.authenticate(r, body)
	action := ""
	if who.service == "iam" || who.service == "sts" || code != "" && r.Method == http.MethodPost && r.URL.Path == "/" {
		form, _ := parseForm(body)
		action = form.Get("Action")
		if code != "" {
			a.queryError(w, code)
			return
		}
		a.count(action)
		if a.Before != nil {
			a.Before(action)
		}
		if failed := a.injected(action); failed != "" {
			a.queryError(w, failed)
			return
		}
		a.query(w, who, action, form)
		return
	}
	if code != "" {
		s3Error(w, code)
		return
	}
	a.s3(w, r, who, body)
}

func (a *AWS) count(action string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls[action]++
}

func (a *AWS) injected(action string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.failures[action]; len(f) > 0 {
		a.failures[action] = f[1:]
		return f[0]
	}
	return ""
}

type form map[string][]string

func (f form) Get(k string) string {
	if v := f[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func parseForm(body []byte) (form, error) {
	r, _ := http.NewRequest(http.MethodPost, "http://fake/", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err := r.ParseForm()
	return form(r.PostForm), err
}

var status = map[string]int{
	"NoSuchEntity": 404, "NoSuchBucket": 404, "NoSuchKey": 404, "EntityAlreadyExists": 409, "DeleteConflict": 409,
	"AccessDenied": 403, "InvalidClientTokenId": 403, "SignatureDoesNotMatch": 403, "InvalidAccessKeyId": 403,
	"MissingAuthenticationToken": 403, "ServiceFailure": 500, "Throttling": 400, "ValidationError": 400, "PermanentRedirect": 301,
}

func (*AWS) queryError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(cmp(status[code], 400))
	fmt.Fprintf(w, `<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>fake</Message></Error><RequestId>fake</RequestId></ErrorResponse>`, code)
}

func s3Error(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(cmp(status[code], 400))
	fmt.Fprintf(w, `<Error><Code>%s</Code><Message>fake</Message></Error>`, code)
}

func cmp(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func respond(w http.ResponseWriter, service, action string, result any) {
	ns := "https://iam.amazonaws.com/doc/2010-05-08/"
	if service == "sts" {
		ns = "https://sts.amazonaws.com/doc/2011-06-15/"
	}
	var inner strings.Builder
	if result != nil {
		e := xml.NewEncoder(&inner)
		_ = e.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: action + "Result"}})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse xmlns="%s">%s<ResponseMetadata><RequestId>fake</RequestId></ResponseMetadata></%sResponse>`, action, ns, inner.String(), action)
}
