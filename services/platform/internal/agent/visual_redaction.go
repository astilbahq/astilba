package agent

import (
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"
)

func redactValues(text string, values map[string]string) string {
	patterns := map[string]bool{}
	for _, value := range values {
		if value == "" {
			continue
		}
		for _, part := range append([]string{value}, strings.Split(value, "\n")...) {
			if part == "" {
				continue
			}
			patterns[part] = true
			patterns[url.QueryEscape(part)] = true
			patterns[strings.TrimPrefix(url.UserPassword("", part).String(), ":")] = true
		}
	}
	// Mark every matching interval before replacing. Consuming one match first can
	// hide the start of another secret and expose its suffix.
	coverage := make([]int, len(text)+1)
	for value := range patterns {
		for start := 0; start < len(text); {
			index := strings.Index(text[start:], value)
			if index < 0 {
				break
			}
			index += start
			coverage[index]++
			coverage[index+len(value)]--
			start = index + 1
		}
	}
	var out strings.Builder
	depth := 0
	redacting := false
	for i := 0; i < len(text); i++ {
		depth += coverage[i]
		if depth > 0 {
			if !redacting {
				out.WriteString("[redacted]")
			}
			redacting = true
		} else {
			out.WriteByte(text[i])
			redacting = false
		}
	}
	return out.String()
}

func safeVisualError(err error, secrets map[string]string) string {
	text := redactValues(err.Error(), secrets)
	if len(text) > 900 {
		text = text[:900]
	}
	return text
}

func readVisualLog(reader io.Reader, tty bool, values map[string]string, limit int) (string, error) {
	// Discard an incomplete sample; redacting a cut secret cannot protect its prefix.
	out := boundedOutput{limit: 1 << 20, stopOnLimit: true}
	var err error
	if tty {
		_, err = io.Copy(&out, reader)
	} else {
		_, err = stdcopy.StdCopy(&out, &out, reader)
	}
	if err != nil || out.truncated {
		return "", errors.New("log sample incomplete or too large")
	}
	text := redactValues(out.b.String(), values)
	if len(text) > limit {
		text = text[:limit]
	}
	return text, nil
}
