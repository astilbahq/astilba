package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/moby/moby/client"
)

// A heartbeat taken while a container is removed reports the rest of the host's containers; an
// inspect that fails otherwise leaves the inventory unknown.
func TestCapacityInventoryLeavesOutARemovedContainer(t *testing.T) {
	for name, tc := range map[string]struct {
		inspect   int
		known     bool
		unbounded []string
	}{
		"removed after the list": {inspect: http.StatusNotFound, known: true, unbounded: []string{"unbounded"}},
		"failing its inspect":    {inspect: http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			containers := map[string]string{
				"bounded":   `{"Memory":536870912,"NanoCpus":500000000}`,
				"unbounded": `{"Memory":0,"NanoCpus":0}`,
				"ending":    `{"Memory":0,"NanoCpus":0}`,
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
				id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
				switch {
				case path == "/info":
					fmt.Fprint(w, `{"NCPU":4,"MemTotal":8589934592}`)
				case path == "/containers/json":
					fmt.Fprint(w, `[{"Id":"bounded"},{"Id":"ending"},{"Id":"unbounded"}]`)
					// The release command's container ends as the list is answered.
					if tc.inspect == http.StatusNotFound {
						delete(containers, "ending")
					}
				case id == "ending" && tc.inspect != http.StatusNotFound:
					w.WriteHeader(tc.inspect)
					fmt.Fprint(w, `{"message":"inspect failed"}`)
				case containers[id] != "":
					fmt.Fprintf(w, `{"Id":%q,"HostConfig":%s}`, id, containers[id])
				default:
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprintf(w, `{"message":"No such container: %s"}`, id)
				}
			}))
			t.Cleanup(server.Close)
			docker, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.54"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { docker.Close() })
			inventory, err := capacityInventory(t.Context(), docker)
			if !tc.known {
				if err == nil {
					t.Fatal("an inventory Docker could not inspect is reported")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if inventory.CpuMillis != 4000 || inventory.MemoryMiB != 8192 || inventory.UnboundedInventoryHash != hostnetwork.StableInventory(tc.unbounded) {
				t.Fatalf("%+v", inventory)
			}
		})
	}
}
