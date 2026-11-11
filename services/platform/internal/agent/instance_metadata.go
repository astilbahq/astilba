package agent

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// instanceMetadataNote warns an owner whose host can reach a cloud instance metadata service.
// Builds can't, but deployed services and host processes can, and credentials it hands out
// usually work from anywhere.
const instanceMetadataNote = "Cloud instance metadata answers on this host. Builds can't reach it, but deployed services and host processes can. Remove the instance's role or credentials, or require IMDSv2 with a hop limit of 1."

// instanceMetadataEndpoints are the link-local services most clouds serve metadata from,
// including AWS's IPv6 one.
var instanceMetadataEndpoints = []string{"http://169.254.169.254/", "http://[fd00:ec2::254]/"}

// The probe waits out a timeout on hosts without metadata, so heartbeats reuse its answer.
var instanceMetadata struct {
	sync.Mutex
	checked time.Time
	answers bool
}

const instanceMetadataRecheck = 15 * time.Minute

// instanceMetadataAnswers reports whether any metadata endpoint gives an HTTP response. It
// sends no metadata-specific header, so it reads nothing an IMDSv2 or GCE server guards.
func instanceMetadataAnswers(ctx context.Context, endpoints []string, now time.Time) bool {
	instanceMetadata.Lock()
	defer instanceMetadata.Unlock()
	if !instanceMetadata.checked.IsZero() && now.Sub(instanceMetadata.checked) < instanceMetadataRecheck {
		return instanceMetadata.answers
	}
	probe := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	answers := make(chan bool, len(endpoints))
	for _, endpoint := range endpoints {
		go func() {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				answers <- false
				return
			}
			response, err := probe.Do(request)
			if err == nil {
				response.Body.Close()
			}
			answers <- err == nil
		}()
	}
	found := false
	for range endpoints {
		found = <-answers || found
	}
	// A probe cut short by the caller says nothing, so the next heartbeat asks again.
	if ctx.Err() == nil {
		instanceMetadata.checked, instanceMetadata.answers = now, found
	}
	return found
}
