package awsfake

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type xTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type xBoundary struct {
	PermissionsBoundaryType string `xml:"PermissionsBoundaryType"`
	PermissionsBoundaryArn  string `xml:"PermissionsBoundaryArn"`
}

type xUser struct {
	Path                string     `xml:"Path"`
	UserName            string     `xml:"UserName"`
	UserID              string     `xml:"UserId"`
	Arn                 string     `xml:"Arn"`
	CreateDate          string     `xml:"CreateDate"`
	PermissionsBoundary *xBoundary `xml:"PermissionsBoundary,omitempty"`
	Tags                *[]xTag    `xml:"Tags>member,omitempty"`
}

// single is a result that holds one user.
type single struct {
	User xUser `xml:"User"`
}

func (u *user) xml(full bool) xUser {
	x := xUser{Path: u.path, UserName: u.name, UserID: u.id, Arn: "arn:aws:iam::" + Account + ":user" + u.path + u.name, CreateDate: u.created.UTC().Format(time.RFC3339)}
	if full {
		if u.boundary != "" {
			x.PermissionsBoundary = &xBoundary{"PermissionsBoundaryPolicy", u.boundary}
		}
		var tags []xTag
		for _, k := range slices.Sorted(maps.Keys(u.tags)) {
			tags = append(tags, xTag{k, u.tags[k]})
		}
		if len(tags) > 0 {
			x.Tags = &tags
		}
	}
	return x
}

// query handles an IAM or STS call.
func (a *AWS) query(w http.ResponseWriter, who caller, action string, f form) {
	a.mu.Lock()
	defer a.mu.Unlock()
	me := a.users[who.key.user]
	if who.service == "sts" {
		switch action {
		case "GetCallerIdentity":
			if me == nil {
				a.queryError(w, "InvalidClientTokenId")
				return
			}
			account := me.account
			if account == "" {
				account = Account
			}
			respond(w, "sts", action, struct {
				Arn     string `xml:"Arn"`
				UserID  string `xml:"UserId"`
				Account string `xml:"Account"`
			}{"arn:aws:iam::" + account + ":user" + me.path + me.name, "AIDAFAKE", account})
		case "GetSessionToken":
			// No policy can stop an IAM user minting session credentials.
			if who.session {
				a.queryError(w, "AccessDenied")
				return
			}
			s := &key{id: fmt.Sprintf("ASIAFAKE%012d", a.serial+1), secret: random(), token: random(), user: who.key.user}
			a.serial++
			a.sessions[s.id] = s
			respond(w, "sts", action, struct {
				Credentials struct {
					AccessKeyID     string `xml:"AccessKeyId"`
					SecretAccessKey string `xml:"SecretAccessKey"`
					SessionToken    string `xml:"SessionToken"`
					Expiration      string `xml:"Expiration"`
				} `xml:"Credentials"`
			}{Credentials: struct {
				AccessKeyID     string `xml:"AccessKeyId"`
				SecretAccessKey string `xml:"SecretAccessKey"`
				SessionToken    string `xml:"SessionToken"`
				Expiration      string `xml:"Expiration"`
			}{s.id, s.secret, s.token, time.Now().Add(12 * time.Hour).UTC().Format(time.RFC3339)}})
		default:
			a.queryError(w, "InvalidAction")
		}
		return
	}
	admin, ok := a.admins[who.key.user]
	if !ok || who.session || admin.Denied[action] {
		a.queryError(w, "AccessDenied")
		return
	}
	name := f.Get("UserName")
	target := a.users[name]
	// The admin policy's resources are users under its path, and its edits need the boundary.
	reach := func(p, boundary string) bool {
		return strings.HasPrefix(p, admin.Within) && (admin.Boundary == "" || boundary == admin.Boundary)
	}
	// missing answers a call by name on a user that doesn't exist.
	missing := func() {
		if a.reportMissing || cmpString(admin.Within, "/") == "/" {
			a.queryError(w, "NoSuchEntity")
		} else {
			a.queryError(w, "AccessDenied")
		}
	}
	switch action {
	case "ListUsers":
		prefix := cmpString(f.Get("PathPrefix"), "/")
		if !strings.HasPrefix(prefix, admin.Within) {
			a.queryError(w, "AccessDenied")
			return
		}
		var names []string
		for n, u := range a.users {
			if strings.HasPrefix(u.path, prefix) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		start := 0
		if m := f.Get("Marker"); m != "" {
			start, _ = strconv.Atoi(m)
		}
		limit := 100
		if m := f.Get("MaxItems"); m != "" {
			limit, _ = strconv.Atoi(m)
		}
		end := min(len(names), start+limit)
		page := []xUser{}
		for _, n := range names[start:end] {
			page = append(page, a.users[n].xml(false))
		}
		result := struct {
			Users       []xUser `xml:"Users>member"`
			IsTruncated bool    `xml:"IsTruncated"`
			Marker      string  `xml:"Marker,omitempty"`
		}{Users: page, IsTruncated: end < len(names)}
		if result.IsTruncated {
			result.Marker = strconv.Itoa(end)
		}
		respond(w, "iam", action, result)
	case "GetUser":
		if target == nil {
			missing()
			return
		}
		if !strings.HasPrefix(target.path, admin.Within) {
			a.queryError(w, "AccessDenied")
			return
		}
		respond(w, "iam", action, single{target.xml(true)})
	case "CreateUser":
		p := cmpString(f.Get("Path"), "/")
		boundary := f.Get("PermissionsBoundary")
		if !reach(p, boundary) {
			a.queryError(w, "AccessDenied")
			return
		}
		if target != nil {
			a.queryError(w, "EntityAlreadyExists")
			return
		}
		if _, ok := a.policies[boundary]; boundary != "" && !ok {
			a.queryError(w, "NoSuchEntity")
			return
		}
		u := &user{id: a.userID(), name: name, path: p, boundary: boundary, tags: map[string]string{}, policies: map[string]string{}, created: time.Now()}
		for i := 1; f.Get("Tags.member."+strconv.Itoa(i)+".Key") != ""; i++ {
			u.tags[f.Get("Tags.member."+strconv.Itoa(i)+".Key")] = f.Get("Tags.member." + strconv.Itoa(i) + ".Value")
		}
		a.users[name] = u
		respond(w, "iam", action, single{u.xml(true)})
	default:
		if target == nil {
			missing()
			return
		}
		if !reach(target.path, target.boundary) {
			a.queryError(w, "AccessDenied")
			return
		}
		a.edit(w, action, target, f)
	}
}

// edit handles a call on an existing user.
func (a *AWS) edit(w http.ResponseWriter, action string, target *user, f form) {
	switch action {
	case "PutUserPolicy":
		var doc any
		if json.Unmarshal([]byte(f.Get("PolicyDocument")), &doc) != nil {
			a.queryError(w, "MalformedPolicyDocument")
			return
		}
		target.policies[f.Get("PolicyName")] = f.Get("PolicyDocument")
		respond(w, "iam", action, nil)
	case "DeleteUserPolicy":
		if _, ok := target.policies[f.Get("PolicyName")]; !ok {
			a.queryError(w, "NoSuchEntity")
			return
		}
		delete(target.policies, f.Get("PolicyName"))
		respond(w, "iam", action, nil)
	case "ListAccessKeys":
		type meta struct {
			UserName    string `xml:"UserName"`
			AccessKeyID string `xml:"AccessKeyId"`
			Status      string `xml:"Status"`
			CreateDate  string `xml:"CreateDate"`
		}
		var out []meta
		for _, id := range slices.Sorted(maps.Keys(a.keys)) {
			if a.keys[id].user == target.name {
				out = append(out, meta{target.name, id, "Active", time.Now().UTC().Format(time.RFC3339)})
			}
		}
		respond(w, "iam", action, struct {
			AccessKeyMetadata []meta `xml:"AccessKeyMetadata>member"`
			IsTruncated       bool   `xml:"IsTruncated"`
		}{AccessKeyMetadata: out})
	case "CreateAccessKey":
		n := 0
		for _, k := range a.keys {
			if k.user == target.name {
				n++
			}
		}
		if n >= 2 {
			a.queryError(w, "LimitExceeded")
			return
		}
		k := a.issue(target.name)
		respond(w, "iam", action, struct {
			AccessKey struct {
				UserName        string `xml:"UserName"`
				AccessKeyID     string `xml:"AccessKeyId"`
				Status          string `xml:"Status"`
				SecretAccessKey string `xml:"SecretAccessKey"`
				CreateDate      string `xml:"CreateDate"`
			} `xml:"AccessKey"`
		}{AccessKey: struct {
			UserName        string `xml:"UserName"`
			AccessKeyID     string `xml:"AccessKeyId"`
			Status          string `xml:"Status"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			CreateDate      string `xml:"CreateDate"`
		}{target.name, k.id, "Active", k.secret, time.Now().UTC().Format(time.RFC3339)}})
	case "DeleteAccessKey":
		k := a.keys[f.Get("AccessKeyId")]
		if k == nil || k.user != target.name {
			a.queryError(w, "NoSuchEntity")
			return
		}
		delete(a.keys, k.id)
		respond(w, "iam", action, nil)
	case "DeleteUser":
		for _, k := range a.keys {
			if k.user == target.name {
				a.queryError(w, "DeleteConflict")
				return
			}
		}
		if len(target.policies) > 0 {
			a.queryError(w, "DeleteConflict")
			return
		}
		delete(a.users, target.name)
		respond(w, "iam", action, nil)
	default:
		a.queryError(w, "InvalidAction")
	}
}

func cmpString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// s3 handles a path-style S3 call.
