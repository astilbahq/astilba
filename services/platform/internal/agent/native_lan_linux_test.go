//go:build linux

package agent

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// nativeLAN is a network namespace the host routes to as it would to a neighbour on its LAN
// or VPC. Builds never reach the runner itself, so native fixtures that builds use, such as
// a private Git provider or registry, listen here.
type nativeLAN struct {
	ns      string
	Address string
}

var nativeLANs atomic.Int32

// startNativeLAN creates the namespace at 10.199.N.2, with any extra addresses routed to it
// from the host as well, and removes it when the test ends.
func startNativeLAN(t *testing.T, extra ...string) nativeLAN {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	n := 40 + int(nativeLANs.Add(1))%200
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	lan := nativeLAN{ns: "astilba-lan-" + suffix, Address: fmt.Sprintf("10.199.%d.2", n)}
	veth := "aslan" + suffix
	run("ip", "netns", "add", lan.ns)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "delete", lan.ns).Run() })
	run("ip", "link", "add", veth, "type", "veth", "peer", "name", veth+"p")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "delete", veth).Run() })
	run("ip", "link", "set", veth+"p", "netns", lan.ns)
	run("ip", "address", "add", fmt.Sprintf("10.199.%d.1/30", n), "dev", veth)
	run("ip", "link", "set", veth, "up")
	inside := func(args ...string) { run(append([]string{"ip", "netns", "exec", lan.ns, "ip"}, args...)...) }
	inside("address", "add", lan.Address+"/30", "dev", veth+"p")
	for _, address := range extra {
		run("ip", "route", "add", address+"/32", "dev", veth)
		inside("address", "add", address+"/32", "dev", veth+"p")
	}
	inside("link", "set", veth+"p", "up")
	inside("link", "set", "lo", "up")
	return lan
}

// listen opens a TCP listener inside the namespace. The socket stays there whichever
// thread later accepts on it.
func (l nativeLAN) listen(t *testing.T, address string) net.Listener {
	t.Helper()
	runtime.LockOSThread()
	host, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer host.Close()
	target, err := os.Open("/run/netns/" + l.ns)
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer target.Close()
	if err = unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	listener, listenErr := net.Listen("tcp", address)
	// A thread left in the namespace must never run other goroutines, so it stays locked
	// and exits with this one if it can't return.
	if err = unix.Setns(int(host.Fd()), unix.CLONE_NEWNET); err != nil {
		t.Fatal("cannot leave the fixture namespace", err)
	}
	runtime.UnlockOSThread()
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}
