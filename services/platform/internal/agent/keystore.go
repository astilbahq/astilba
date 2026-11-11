package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// KeyStore seals the agent's private keys, and outstanding enrollment codes, to this machine, so
// that a copy of the state directory alone can't authenticate as the agent.
type KeyStore interface {
	Seal(ctx context.Context, plain []byte) ([]byte, error)
	Unseal(ctx context.Context, sealed []byte) ([]byte, error)
	Protection() api.AgentCapabilitiesKeyProtection
}

// errKeyStoreSetup says sealing needs a one-time setup the running service can't do itself.
var errKeyStoreSetup = errors.New("systemd credential host key is missing; run `sudo systemd-creds setup` once")

const credentialName = "astilba-agent-key"

// SystemKeyStore seals with systemd-creds. With a TPM it binds to the chip alone: no PCRs, since
// systemd 255 otherwise binds to PCR 7, which Secure Boot and firmware updates change. Without one
// it uses the host key in /var/lib/systemd, which lives outside the agent's state directory.
func SystemKeyStore() (KeyStore, error) {
	if _, err := exec.LookPath("systemd-creds"); err != nil {
		return nil, errors.New("systemd-creds is not available; the agent needs it to seal its key")
	}
	if _, err := os.Stat("/dev/tpmrm0"); err == nil {
		return systemdCreds{tpm: true}, nil
	}
	if _, err := os.Stat("/var/lib/systemd/credential.secret"); err != nil {
		return nil, errKeyStoreSetup
	}
	return systemdCreds{}, nil
}

// SetupSystemKeyStore creates systemd's host key when a host without a TPM lacks one. It needs to
// write /var/lib/systemd, which the agent service's sandbox forbids, so the enroll and transfer
// commands run it.
func SetupSystemKeyStore(ctx context.Context) error {
	if _, err := os.Stat("/dev/tpmrm0"); err == nil {
		return nil
	}
	if _, err := os.Stat("/var/lib/systemd/credential.secret"); err == nil {
		return nil
	}
	return run(ctx, nil, "systemd-creds", "setup")
}

type systemdCreds struct{ tpm bool }

func (s systemdCreds) Protection() api.AgentCapabilitiesKeyProtection {
	if s.tpm {
		return api.Tpm
	}
	return api.HostKey
}

func (s systemdCreds) Seal(ctx context.Context, plain []byte) ([]byte, error) {
	key := "--with-key=host"
	args := []string{"encrypt", "--name=" + credentialName}
	if s.tpm {
		args = append(args, "--with-key=tpm2", "--tpm2-pcrs=")
	} else {
		args = append(args, key)
	}
	return capture(ctx, plain, "systemd-creds", append(args, "-", "-")...)
}

func (s systemdCreds) Unseal(ctx context.Context, sealed []byte) ([]byte, error) {
	return capture(ctx, sealed, "systemd-creds", "decrypt", "--name="+credentialName, "-", "-")
}

func capture(ctx context.Context, in []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(in)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("systemd-creds could not seal or unseal the agent key")
	}
	return out.Bytes(), nil
}

func run(ctx context.Context, in []byte, name string, args ...string) error {
	_, err := capture(ctx, in, name, args...)
	return err
}
