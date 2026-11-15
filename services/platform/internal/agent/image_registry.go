package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/distribution/reference"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/system"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/net/http/httpproxy"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// registryImage is where the agent reads a deployment's image: the registry, or a mirror of it,
// that Docker would read. The image is named by digest and the agent checks every blob against
// its digest, so whichever serves it, the content is the image's.
type registryImage interface {
	// manifest is the manifest or index with this digest, of at most limit bytes.
	manifest(ctx context.Context, d digest.Digest, limit int64) (data []byte, mediaType string, err error)
	// blob is the blob desc names. The caller checks its size and digest.
	blob(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error)
}

type orasImage struct {
	repo *remote.Repository
	// fallback marks an insecure registry over plain HTTP, which Docker tries only when it
	// cannot reach the registry over HTTPS at all.
	fallback bool
}

func plainHTTPFallback(source registryImage) bool {
	image, ok := source.(orasImage)
	return ok && image.fallback
}

func (o orasImage) manifest(ctx context.Context, d digest.Digest, limit int64) ([]byte, string, error) {
	desc, rc, err := o.repo.Manifests().FetchReference(ctx, d.String())
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, "", err
	}
	return data, desc.MediaType, nil
}

func (o orasImage) blob(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	return o.repo.Blobs().Fetch(ctx, desc)
}

// dockerCertsDirectory is where Docker reads a registry's certificate authorities and client
// certificates, in a directory named after the registry's host and port.
var dockerCertsDirectory = "/etc/docker/certs.d"

// registryImages are the places Docker would read the repository named from, in the order the
// agent tries them: Docker Hub's mirrors from the daemon's configuration, then the registry
// itself, over HTTPS and, for a registry the daemon treats as insecure, over HTTP after it.
// Only the registry itself receives the credential. The agent reaches each through the
// daemon's proxy.
func registryImages(ctx context.Context, info system.Info, named reference.Named, credential *pipeline.Credential) ([]registryImage, error) {
	proxy, err := dockerProxy(info)
	if err != nil {
		return nil, err
	}
	config := info.RegistryConfig
	domain, path := reference.Domain(named), reference.Path(named)
	var images []registryImage
	add := func(host, repository string, insecure, plainHTTP bool, credential *pipeline.Credential) error {
		image, err := newRegistryImage(host, repository, insecure, plainHTTP, credential, proxy)
		if err != nil {
			return &imageLoadRefused{why: "its repository name is not valid"}
		}
		images = append(images, image)
		return nil
	}
	fallback := func(host, repository string, credential *pipeline.Credential) error {
		if err := add(host, repository, true, true, credential); err != nil {
			return err
		}
		image := images[len(images)-1].(orasImage)
		image.fallback = true
		images[len(images)-1] = image
		return nil
	}
	if domain == "docker.io" && config != nil {
		for _, mirror := range config.Mirrors {
			u, err := url.Parse(mirror)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				continue
			}
			repository := path
			if prefix := strings.Trim(u.Path, "/"); prefix != "" {
				repository = prefix + "/" + path
			}
			if err = add(u.Host, repository, u.Scheme == "http", u.Scheme == "http", nil); err != nil {
				return nil, err
			}
		}
	}
	host := domain
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	secure := registrySecure(ctx, config, domain)
	if err = add(host, path, !secure, false, credential); err != nil {
		return nil, err
	}
	if !secure {
		if err = fallback(host, path, credential); err != nil {
			return nil, err
		}
	}
	return images, nil
}

// dockerProxy is the proxy Docker reaches registries through: the daemon's, as its info gives
// it, or the agent's own environment's when the daemon has none. Docker's info hides a proxy's
// password, so a daemon proxy with one is used only through the agent's environment.
func dockerProxy(info system.Info) (func(*http.Request) (*url.URL, error), error) {
	daemon := httpproxy.Config{HTTPProxy: info.HTTPProxy, HTTPSProxy: info.HTTPSProxy, NoProxy: info.NoProxy}
	if daemon.HTTPProxy == "" && daemon.HTTPSProxy == "" {
		return http.ProxyFromEnvironment, nil
	}
	for _, proxy := range []string{daemon.HTTPProxy, daemon.HTTPSProxy} {
		if u, err := url.Parse(proxy); proxy != "" && (err != nil || u.User != nil) {
			environment := httpproxy.FromEnvironment()
			if environment.HTTPProxy == "" && environment.HTTPSProxy == "" {
				return nil, &imageLoadRefused{why: "Docker reaches registries through a proxy with a password, which Docker does not show the agent", fix: "The server's owner sets the same proxy in the agent's environment, with HTTPS_PROXY and NO_PROXY, then you {retry}."}
			}
			return http.ProxyFromEnvironment, nil
		}
	}
	proxy := daemon.ProxyFunc()
	return func(r *http.Request) (*url.URL, error) { return proxy(r.URL) }, nil
}

// registrySecure reports whether Docker requires a verified TLS connection to the registry,
// as Docker decides it: by the registry's own entry in the daemon's configuration, or else by
// whether any of its addresses is in an insecure range, 127.0.0.0/8 by default.
func registrySecure(ctx context.Context, config *registrytypes.ServiceConfig, name string) bool {
	insecure := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	if config != nil {
		if index := config.IndexConfigs[name]; index != nil {
			return index.Secure
		}
		insecure = config.InsecureRegistryCIDRs
	}
	host, _, err := net.SplitHostPort(name)
	if err != nil {
		host = name
	}
	var addresses []netip.Addr
	if address, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{address}
	} else if resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host); err == nil {
		for _, address := range resolved {
			addresses = append(addresses, address.Unmap())
		}
	}
	for _, address := range addresses {
		for _, prefix := range insecure {
			if prefix.Contains(address) {
				return false
			}
		}
	}
	return true
}

// newRegistryImage reads repository from host, trusting what Docker trusts for that host:
// the system's authorities and those in its directory under /etc/docker/certs.d, with any
// client certificate there. An insecure registry's certificate is not verified, as Docker does
// not verify it, and plainHTTP reads it without TLS.
func newRegistryImage(host, repository string, insecure, plainHTTP bool, credential *pipeline.Credential, proxy func(*http.Request) (*url.URL, error)) (registryImage, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, InsecureSkipVerify: insecure}
	directory := filepath.Join(dockerCertsDirectory, host)
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".crt"):
			if pem, err := os.ReadFile(filepath.Join(directory, name)); err == nil {
				roots.AppendCertsFromPEM(pem)
			}
		case strings.HasSuffix(name, ".cert"):
			pair, err := tls.LoadX509KeyPair(filepath.Join(directory, name), filepath.Join(directory, strings.TrimSuffix(name, ".cert")+".key"))
			if err == nil {
				tlsConfig.Certificates = append(tlsConfig.Certificates, pair)
			}
		}
	}
	transport := &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	credentials := auth.StaticCredential(host, auth.EmptyCredential)
	if credential != nil && (credential.Username != "" || credential.Password != "") {
		credentials = auth.StaticCredential(host, auth.Credential{Username: credential.Username, Password: credential.Password})
	}
	repo := &remote.Repository{
		Client:           &auth.Client{Client: &http.Client{Transport: transport}, Credential: credentials, Cache: auth.NewCache(), Header: http.Header{"User-Agent": {"astilba-agent"}}},
		Reference:        registry.Reference{Registry: host, Repository: repository},
		PlainHTTP:        plainHTTP,
		MaxMetadataBytes: imageMetadataBytes,
	}
	if err := repo.Reference.ValidateRepository(); err != nil {
		return nil, errors.New("the image's repository name is not valid")
	}
	return orasImage{repo: repo}, nil
}
