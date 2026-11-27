package awsfake

import (
	"encoding/json/v2"
	"encoding/xml"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (a *AWS) s3(w http.ResponseWriter, r *http.Request, who caller, body []byte) {
	bucket, object, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	var action, resource string
	conditions := map[string]string{}
	switch {
	case object == "" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		action, resource = "s3:ListBucket", "arn:aws:s3:::"+bucket
		conditions["s3:prefix"] = r.URL.Query().Get("prefix")
	case object != "" && r.Method == http.MethodGet:
		action, resource = "s3:GetObject", "arn:aws:s3:::"+bucket+"/"+object
	case object != "" && r.Method == http.MethodPut:
		action, resource = "s3:PutObject", "arn:aws:s3:::"+bucket+"/"+object
	case object != "" && r.Method == http.MethodDelete:
		action, resource = "s3:DeleteObject", "arn:aws:s3:::"+bucket+"/"+object
	default:
		s3Error(w, "NotImplemented")
		return
	}
	if who.session {
		conditions["aws:TokenIssueTime"] = time.Now().UTC().Format(time.RFC3339)
	}
	a.count(action)
	if a.Before != nil {
		a.Before(action)
	}
	if failed := a.injected(action); failed != "" {
		s3Error(w, failed)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	region, ok := a.buckets[bucket]
	if !ok {
		s3Error(w, "NoSuchBucket")
		return
	}
	if signed := regexp.MustCompile(`/([0-9]{8})/([a-z0-9-]+)/s3/`).FindStringSubmatch(r.Header.Get("Authorization")); signed == nil || signed[2] != region {
		s3Error(w, "AuthorizationHeaderMalformed")
		return
	}
	if !a.allowed(who.key.user, action, resource, conditions) {
		s3Error(w, "AccessDenied")
		return
	}
	objects := a.objects[bucket]
	switch action {
	case "s3:ListBucket":
		prefix := r.URL.Query().Get("prefix")
		var names []string
		for n := range objects {
			if strings.HasPrefix(n, prefix) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		type content struct {
			Key  string `xml:"Key"`
			Size int    `xml:"Size"`
		}
		var contents []content
		for _, n := range names {
			contents = append(contents, content{n, len(objects[n])})
		}
		raw, _ := xml.Marshal(struct {
			XMLName     xml.Name  `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
			Name        string    `xml:"Name"`
			Prefix      string    `xml:"Prefix"`
			KeyCount    int       `xml:"KeyCount"`
			IsTruncated bool      `xml:"IsTruncated"`
			Contents    []content `xml:"Contents"`
		}{Name: bucket, Prefix: prefix, KeyCount: len(contents), Contents: contents})
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(raw)
	case "s3:GetObject":
		content, ok := objects[object]
		if !ok {
			s3Error(w, "NoSuchKey")
			return
		}
		_, _ = w.Write(content)
	case "s3:PutObject":
		objects[object] = decodeChunked(r, body)
		w.WriteHeader(http.StatusOK)
	case "s3:DeleteObject":
		delete(objects, object)
		w.WriteHeader(http.StatusNoContent)
	}
}

// decodeChunked returns an upload's content, undoing aws-chunked encoding.
func decodeChunked(r *http.Request, body []byte) []byte {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return body
	}
	var out []byte
	rest := string(body)
	for {
		line, after, ok := strings.Cut(rest, "\r\n")
		if !ok {
			return out
		}
		size, _ := strconv.ParseInt(strings.SplitN(line, ";", 2)[0], 16, 64)
		if size == 0 || int(size) > len(after) {
			return out
		}
		out = append(out, after[:size]...)
		rest = strings.TrimPrefix(after[size:], "\r\n")
	}
}

// allowed evaluates a user's inline policies and boundary: an explicit deny in either wins, and
// otherwise both must allow.
func (a *AWS) allowed(name, action, resource string, conditions map[string]string) bool {
	u := a.users[name]
	if u == nil {
		return false
	}
	identity := effect{}
	for _, doc := range u.policies {
		identity.add(evaluate(doc, action, resource, conditions))
	}
	if identity.deny || !identity.allow {
		return false
	}
	if u.boundary == "" {
		return true
	}
	bounded := evaluate(a.policies[u.boundary], action, resource, conditions)
	return bounded.allow && !bounded.deny
}

type effect struct{ allow, deny bool }

func (e *effect) add(o effect) { e.allow, e.deny = e.allow || o.allow, e.deny || o.deny }

func evaluate(document, action, resource string, conditions map[string]string) effect {
	var doc struct {
		Statement []map[string]any `json:"Statement"`
	}
	if json.Unmarshal([]byte(document), &doc) != nil {
		return effect{}
	}
	var e effect
	for _, s := range doc.Statement {
		if !anyMatch(s["Action"], action, true) || !anyMatch(s["Resource"], resource, false) || !conditionsHold(s["Condition"], conditions) {
			continue
		}
		switch s["Effect"] {
		case "Allow":
			e.allow = true
		case "Deny":
			e.deny = true
		}
	}
	return e
}

func values(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func anyMatch(patterns any, value string, fold bool) bool {
	for _, p := range values(patterns) {
		if glob(p, value, fold) {
			return true
		}
	}
	return false
}

// glob matches IAM's * and ? wildcards.
func glob(pattern, value string, fold bool) bool {
	if fold {
		pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	}
	re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$"
	ok, _ := regexp.MatchString(re, value)
	return ok
}

func conditionsHold(raw any, request map[string]string) bool {
	operators, _ := raw.(map[string]any)
	for operator, body := range operators {
		keys, _ := body.(map[string]any)
		for k, want := range keys {
			got, present := request[k]
			switch operator {
			case "StringLike":
				if !present || !anyMatch(want, got, false) {
					return false
				}
			case "StringEquals":
				if !present || !slices.Contains(values(want), got) {
					return false
				}
			case "Null":
				if (values(want)[0] == "true") == present {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
