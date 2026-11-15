package agent

import (
	"net/netip"
	"reflect"
	"testing"

	registrytypes "github.com/moby/moby/api/types/registry"
)

func TestDockerHubMirrorsFollowDockersOrder(t *testing.T) {
	config := &registrytypes.ServiceConfig{Mirrors: []string{"https://mirror.gcr.io/", "http://10.0.0.5:5000/hub/", "ftp://mirror.example/", "https:///no-host", "://bad"}}
	want := []dockerHubMirror{{host: "mirror.gcr.io"}, {host: "10.0.0.5:5000", prefix: "hub", plainHTTP: true}}
	if got := dockerHubMirrors(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("mirrors = %#v, want %#v", got, want)
	}
	if got := dockerHubMirrors(nil); got != nil {
		t.Fatalf("mirrors without a configuration = %#v", got)
	}
}

func TestBuilderConfigReadsDockerHubThroughTheDaemonsMirrors(t *testing.T) {
	resolver := []netip.Addr{netip.MustParseAddr("192.0.2.53"), netip.MustParseAddr("2001:db8::53")}
	for _, tc := range []struct {
		name, registry string
		mirrors        []dockerHubMirror
		resolvers      []netip.Addr
		want           string
	}{
		{name: "nothing to configure", want: ""},
		{name: "resolvers only", resolvers: resolver, want: "[dns]\n  nameservers = [\"192.0.2.53\", \"2001:db8::53\"]\n"},
		{
			name:     "a registry and an HTTPS mirror",
			registry: "registry.example:5000",
			mirrors:  []dockerHubMirror{{host: "mirror.gcr.io"}},
			want:     "[registry.\"docker.io\"]\n  mirrors = [\"mirror.gcr.io\"]\n[registry.\"registry.example:5000\"]\n  http = false\n  insecure = false\n",
		},
		{
			// BuildKit would read every Docker Hub tag through a plain HTTP mirror without TLS, a
			// loopback one without TLS on the build's own loopback, and one with a path at another
			// repository than Docker does.
			name:    "mirrors BuildKit can't read as Docker does, then HTTPS ones",
			mirrors: []dockerHubMirror{{host: "10.0.0.5:5000", plainHTTP: true}, {host: "localhost:5000"}, {host: "127.0.0.1:5000"}, {host: "[::1]:5000"}, {host: "LOCALHOST"}, {host: "mirror.example", prefix: "hub/proxy"}, {host: "mirror.example"}, {host: "mirror.gcr.io"}, {host: "mirror.gcr.io"}},
			want:    "[registry.\"docker.io\"]\n  mirrors = [\"mirror.example\", \"mirror.gcr.io\"]\n",
		},
		{
			// One table for Docker Hub, which BuildKit requires; pushes still go to Docker Hub itself.
			name:     "a build that pushes to Docker Hub",
			registry: "docker.io",
			mirrors:  []dockerHubMirror{{host: "mirror.gcr.io"}},
			want:     "[registry.\"docker.io\"]\n  http = false\n  insecure = false\n  mirrors = [\"mirror.gcr.io\"]\n",
		},
		{
			name:     "a mirror on the build's own registry",
			registry: "registry.example",
			mirrors:  []dockerHubMirror{{host: "registry.example"}, {host: "registry.example", plainHTTP: true}},
			want:     "[registry.\"docker.io\"]\n  mirrors = [\"registry.example\"]\n[registry.\"registry.example\"]\n  http = false\n  insecure = false\n",
		},
		{
			name:    "an IPv6 mirror",
			mirrors: []dockerHubMirror{{host: "[2001:db8::5]:5000"}},
			want:    "[registry.\"docker.io\"]\n  mirrors = [\"[2001:db8::5]:5000\"]\n",
		},
		{
			name: "mirror names that could end a TOML string or table",
			mirrors: []dockerHubMirror{
				{host: "evil\"]\n[grpc"}, {host: "mirror.example\n[dns]"}, {host: "mirror.example]"}, {host: "[mirror.example"}, {host: "mirror\\example"}, {host: "mirror.example\x07"},
			},
			want: "",
		},
		{
			// The registry host is validated with the command; quoting still keeps it one string.
			name:     "a registry host that could end a TOML string",
			registry: "registry.example\"]\n[grpc",
			want:     "[registry.\"registry.example\\\"]\\n[grpc\"]\n  http = false\n  insecure = false\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := builderConfig(tc.registry, tc.mirrors, tc.resolvers); got != tc.want {
				t.Fatalf("config:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
