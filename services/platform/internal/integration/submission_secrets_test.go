package integration

import (
	"context"
	"encoding/json/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/google/uuid"
)

// A saved draft is frozen when it is submitted, so a value can become a runtime secret while the
// submission waits. Management checks the files again before every attempt writes to the
// repository, and a runner takes no part.
func TestSubmissionRechecksRuntimeSecretsBeforeWriting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	x := newSubmissionFixture(t, ctx)
	b := x.owner
	// The provider fails every write until a test lets it, so each submission's first attempt
	// fails and is retried after River's first backoff.
	refusedWrites := &x.writes

	inContent := func(canary string) func(*composeproject.Bundle) {
		return func(bundle *composeproject.Bundle) { bundle.Files[0].Content += "# " + canary + "\n" }
	}
	// A file's path names a tree entry, so a value in it is written too.
	inFileName := func(canary string) func(*composeproject.Bundle) {
		return func(bundle *composeproject.Bundle) {
			bundle.Files = append(bundle.Files, composeproject.File{Path: "config/" + canary + ".env", Content: "MODE=plain\n"})
		}
	}
	prepare := x.prepare
	submit := func(t *testing.T, path string, draft api.Revision) api.SourceSubmission {
		t.Helper()
		return x.submit(t, nil, path, draft)
	}
	await := x.await
	retrying, settled := submissionRetrying, submissionFinished

	// The submission is checked once more before each attempt, so a value that becomes a runtime
	// secret while it waits never reaches the provider.
	key := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\n-----END PRIVATE KEY-----\n"
	for name, form := range map[string]struct {
		plant        func(*composeproject.Bundle)
		value, leaks string
		many         bool
	}{
		"a known value": {plant: inContent("queued-secret-canary"), value: "queued-secret-canary", leaks: "queued-secret-canary"},
		"a file name":   {plant: inFileName("queued-name-canary"), value: "queued-name-canary", leaks: "queued-name-canary"},
		"a key in an indented block": {plant: func(bundle *composeproject.Bundle) {
			bundle.Files = append(bundle.Files, composeproject.File{Path: "config/key.yaml", Content: "key: |\n  " + strings.ReplaceAll(strings.TrimSpace(key), "\n", "\n  ") + "\n"})
		}, value: key, leaks: "MzEfYyjiWA4R4"},
		"a URL-encoded file name":   {plant: inFileName("p%40ss-word-123"), value: "p@ss-word-123", leaks: "ss-word-123"},
		"over the validation limit": {plant: inContent("not-a-secret"), many: true},
	} {
		t.Run(name+" is refused before the next attempt writes", func(t *testing.T) {
			b := *b
			b.t = t
			path, draft := prepare(t, "Queued "+name, form.plant)
			large := func(i int) {
				b.call("POST", path+"/secrets", map[string]string{"name": "LARGE_" + strconv.Itoa(i), "value": strings.Repeat("v", 64<<10)}, 201, nil)
			}
			// Exactly at the limit while queued, so one more version crosses it in a single step.
			if form.many {
				for i := range 16 {
					large(i)
				}
			}
			queued := submit(t, path, draft)
			await(t, queued.Id, retrying)
			before := refusedWrites.Load()
			if form.many {
				large(16)
			} else {
				b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": form.value}, 201, nil)
			}
			state, note := await(t, queued.Id, settled)
			if state != "failed" || !strings.Contains(note, "Nothing was written") || !strings.Contains(note, "runtime secret") || form.leaks != "" && strings.Contains(note, form.leaks) {
				t.Fatal("the submission was not refused cleanly", state, note)
			}
			if refusedWrites.Load() != before {
				t.Fatal("an attempt tried to write after the files became secret")
			}
			// The saved draft meets the same check when submitted again.
			if !form.many {
				refusal := string(b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 400, nil))
				if !strings.Contains(refusal, "source.containsRuntimeSecret") || strings.Contains(refusal, form.leaks) {
					t.Fatal("submitting again did not refuse the secret, or echoed it", refusal)
				}
			}
		})
	}

	t.Run("submitting refuses a runtime secret stored with surrounding whitespace", func(t *testing.T) {
		b := *b
		b.t = t
		canary := "pasted-secret-canary"
		for name, plant := range map[string]func(*composeproject.Bundle){"content": func(bundle *composeproject.Bundle) { bundle.Files[0].Content += "# TOKEN=\"" + canary + "\"\n" }, "file name": inFileName(canary)} {
			path, draft := prepare(t, "Pasted secret in "+name, plant)
			b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary + "\n"}, 201, nil)
			refusal := string(b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 400, nil))
			if !strings.Contains(refusal, "source.containsRuntimeSecret") || strings.Contains(refusal, canary) {
				t.Fatal("submitting did not refuse the trimmed value, or echoed it", name, refusal)
			}
		}
	})
	// Superseded versions count only while they fit, so rotating can't block an environment.
	t.Run("rotating a large secret never reaches the limit", func(t *testing.T) {
		b := *b
		b.t = t
		path, draft := prepare(t, "Rotated", inContent("not-a-secret"))
		var large api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": "LARGE", "value": strings.Repeat("v", 64<<10)}, 201, &large)
		for i := range 20 {
			b.call("POST", path+"/secrets/"+large.SecretId.String()+"/versions", map[string]string{"value": strings.Repeat(strconv.Itoa(i%10), 64<<10)}, 201, nil)
		}
		queued := submit(t, path, draft)
		if _, note := await(t, queued.Id, retrying); strings.Contains(note, "runtime secret") {
			t.Fatal("rotations blocked the submission", note)
		}
	})
	// A value under 12 bytes matches ordinary text and can be guessed, so it is not searched for,
	// and the member who saved it is warned.
	t.Run("a one-byte value does not block a submission", func(t *testing.T) {
		b := *b
		b.t = t
		path, draft := prepare(t, "Short value", inContent("a-file-full-of-the-letter-a"))
		var short api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": "SHORT", "value": "a"}, 201, &short)
		if short.TooShortToCheck == nil || !*short.TooShortToCheck {
			t.Fatal("saving a one-byte value did not warn", short)
		}
		if _, note := await(t, submit(t, path, draft).Id, retrying); strings.Contains(note, "runtime secret") {
			t.Fatal("a one-byte value blocked the submission", note)
		}
	})
	// A version just replaced may still be valid where it was issued, so it is still searched for,
	// until an owner retires it.
	t.Run("retiring a replaced version clears its match", func(t *testing.T) {
		b := *b
		b.t = t
		canary := "replaced-secret-canary"
		path, draft := prepare(t, "Replaced", inContent(canary))
		var replaced api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, &replaced)
		b.call("POST", path+"/secrets/"+replaced.SecretId.String()+"/versions", map[string]string{"value": "fresh-secret-value"}, 201, nil)
		refusal := string(b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 400, nil))
		if !strings.Contains(refusal, "source.containsRuntimeSecret") {
			t.Fatal("a replaced version was not searched for", refusal)
		}
		b.call("DELETE", path+"/secrets/"+replaced.SecretId.String()+"/versions/"+replaced.Id.String(), nil, 204, nil)
		if _, note := await(t, submit(t, path, draft).Id, retrying); strings.Contains(note, "runtime secret") {
			t.Fatal("the retired version still blocked the submission", note)
		}
	})
	// A value replaced three times is no longer searched for, and retiring a version between never
	// makes it recent again: retired versions still count toward recency.
	t.Run("retiring a version does not bring an older one back", func(t *testing.T) {
		b := *b
		b.t = t
		canary := "aged-out-secret-canary"
		path, draft := prepare(t, "Aged out", inContent(canary))
		var first, second api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, &first)
		for i, value := range []string{"aged-out-second-value", "aged-out-third-value", "aged-out-fourth-value"} {
			var v api.SecretVersion
			b.call("POST", path+"/secrets/"+first.SecretId.String()+"/versions", map[string]string{"value": value}, 201, &v)
			if i == 0 {
				second = v
			}
		}
		b.call("DELETE", path+"/secrets/"+second.SecretId.String()+"/versions/"+second.Id.String(), nil, 204, nil)
		if _, note := await(t, submit(t, path, draft).Id, retrying); strings.Contains(note, "runtime secret") {
			t.Fatal("retiring a version brought an aged-out value back into the check", note)
		}
	})
	// The check searches for a bounded number of versions, and fails closed past it until an owner
	// retires secrets the environment no longer needs.
	t.Run("more current versions than the check searches for are refused", func(t *testing.T) {
		b := *b
		b.t = t
		path, draft := prepare(t, "Many secrets", inContent("not-a-secret"))
		var last api.SecretVersion
		for i := 0; i < 257; {
			// Sealing draws on the broker's per-workspace budget, which this outruns.
			status, raw := b.request("POST", path+"/secrets", map[string]string{"name": "SECRET_" + strconv.Itoa(i), "value": "many-secrets-value-" + strconv.Itoa(i)})
			switch status {
			case 201:
				if err := json.Unmarshal(raw, &last); err != nil {
					t.Fatal(err)
				}
				i++
			case 429:
				time.Sleep(100 * time.Millisecond)
			default:
				t.Fatal("creating a secret:", status, string(raw))
			}
		}
		refusal := string(b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 400, nil))
		if !strings.Contains(refusal, "source.secretBudget") {
			t.Fatal("the version bound did not fail closed", refusal)
		}
		b.call("DELETE", path+"/secrets/"+last.SecretId.String(), nil, 204, nil)
		if _, note := await(t, submit(t, path, draft).Id, retrying); strings.Contains(note, "runtime secret") {
			t.Fatal("the bound still refused after a secret was retired", note)
		}
	})
	t.Run("a draft can rename a secret file name out of its baseline", func(t *testing.T) {
		canary := "baseline-file-name-canary"
		rename := func(bundle *composeproject.Bundle) { bundle.Files[1].Path = "config/app.env" }
		path, draft := prepare(t, "Renamed baseline", rename, composeproject.File{Path: "config/" + canary + ".env", Content: "MODE=plain\n"})
		b := *b
		b.t = t
		b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, nil)
		queued := submit(t, path, draft)
		if _, note := await(t, queued.Id, retrying); strings.Contains(note, "runtime secret") {
			t.Fatal("the renamed baseline file was refused", note)
		}
	})
}
