//go:build ignore

// Runs inside the built image for scripts/platform-image-test.mjs. It uses the standard
// library's system roots, as the server's clients do. Any HTTP response means the
// certificate chain verified; the status doesn't matter. A refused certificate always
// fails. An unreachable host doesn't, as long as another one verified, so a third party's
// outage can't hold up the merge queue.
package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	client := &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	verified := false
	for _, url := range os.Args[1:] {
		if err := request(client, url); err != nil {
			fmt.Println(url, "unreachable:", err)
		} else {
			verified = true
		}
	}
	if !verified {
		fail("no host could be reached")
	}
}

func request(client *http.Client, url string) error {
	var err error
	for _, delay := range []time.Duration{0, 20 * time.Second, 40 * time.Second} {
		time.Sleep(delay)
		var response *http.Response
		if response, err = client.Head(url); err == nil {
			response.Body.Close()
			fmt.Println(url, response.Status)
			return nil
		}
		var refused *tls.CertificateVerificationError
		if errors.As(err, &refused) {
			fail(err)
		}
	}
	return err
}

func fail(reason any) {
	fmt.Fprintln(os.Stderr, reason)
	os.Exit(1)
}
