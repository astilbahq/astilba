package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/automationclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "development"

const usage = `Usage: astilba-cli [--origin HTTPS_ORIGIN] [--token-file FILE] [--development] COMMAND

Credentials: ASTILBA_TOKEN, or a private token file on Unix. ASTILBA_ORIGIN sets the origin.
--development permits HTTP only for an explicit loopback IP.

Commands:
  context
  draft get
  draft update --base UUID --file SPECIFICATION_JSON
  plan prepare --base UUID --server UUID
  plan get --id UUID
  plan apply --id UUID --key UUID
  build targets
  build run --application UUID --base UUID --target-hash HASH --key UUID
  operation get --id UUID
  mcp                 Run the stdio MCP server
  version

Mutations are never retried automatically. Reuse an explicit key to inspect or
resume a previously accepted build/apply request. Saving a draft does not deploy.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func privateToken(path string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("--token-file is supported on Unix; use ASTILBA_TOKEN on Windows")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open token file")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return "", errors.New("token file must be a regular file readable only by its owner")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil || len(raw) > 256 {
		return "", errors.New("invalid token file")
	}
	return strings.TrimSpace(string(raw)), nil
}

func run(ctx context.Context, args []string) error {
	global := flag.NewFlagSet("astilba-cli", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	origin := global.String("origin", os.Getenv("ASTILBA_ORIGIN"), "management origin")
	tokenFile := global.String("token-file", "", "private token file")
	development := global.Bool("development", false, "allow loopback HTTP")
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = global.Args()
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}
	if len(args) == 0 {
		return errors.New(usage)
	}
	token := os.Getenv("ASTILBA_TOKEN")
	if *tokenFile != "" {
		var err error
		token, err = privateToken(*tokenFile)
		if err != nil {
			return err
		}
	}
	client, err := automationclient.New(*origin, token, *development)
	if err != nil {
		return err
	}
	v, err := client.Context(ctx)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		switch args[0] {
		case "context":
			return output(v)
		case "mcp":
			return client.MCP(v, version).Run(ctx, &mcp.StdioTransport{MaxLineLength: 256 << 10})
		}
		return errors.New("unknown command; use --help")
	}
	command := args[0] + " " + args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var id, base, server, key, application, hash, file string
	switch command {
	case "draft get", "build targets":
	case "draft update":
		flags.StringVar(&base, "base", "", "base revision UUID")
		flags.StringVar(&file, "file", "", "specification JSON file or - for stdin")
	case "plan prepare":
		flags.StringVar(&base, "base", "", "base revision UUID")
		flags.StringVar(&server, "server", "", "deployment server UUID")
	case "plan get", "operation get":
		flags.StringVar(&id, "id", "", "plan or operation UUID")
	case "plan apply":
		flags.StringVar(&id, "id", "", "reviewed plan UUID")
		flags.StringVar(&key, "key", "", "idempotency UUID")
	case "build run":
		flags.StringVar(&application, "application", "", "configured application UUID")
		flags.StringVar(&base, "base", "", "base revision UUID")
		flags.StringVar(&hash, "target-hash", "", "expected target configuration hash")
		flags.StringVar(&key, "key", "", "idempotency UUID")
	default:
		return errors.New("unknown command; use --help")
	}
	if err = flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var out any
	switch command {
	case "draft get":
		out, err = client.Draft(ctx, v)
	case "build targets":
		out, err = client.Targets(ctx, v)
	case "draft update":
		baseID, e := automationclient.ID(base)
		if e != nil {
			return e
		}
		var input io.Reader = os.Stdin
		if file == "" {
			return errors.New("--file is required")
		}
		if file != "-" {
			f, e := os.Open(file)
			if e != nil {
				return errors.New("cannot open specification file")
			}
			defer f.Close()
			input = f
		}
		raw, e := io.ReadAll(io.LimitReader(input, 150001))
		if e != nil || len(raw) > 150000 || !json.Valid(raw) {
			return errors.New("specification must be valid JSON within the API size limit")
		}
		out, err = client.UpdateDraft(ctx, v, api.DraftUpdate{BaseRevisionId: baseID, Specification: raw})
	case "plan prepare":
		baseID, e := automationclient.ID(base)
		if e != nil {
			return e
		}
		serverID, e := automationclient.ID(server)
		if e != nil {
			return e
		}
		out, err = client.PreparePlan(ctx, v, api.VisualPlanRequest{BaseRevisionId: baseID, ServerId: serverID})
	case "plan get", "operation get", "plan apply":
		identity, e := automationclient.ID(id)
		if e != nil {
			return e
		}
		if command == "plan get" {
			out, err = client.Plan(ctx, v, identity)
		} else if command == "operation get" {
			out, err = client.Operation(ctx, v, identity)
		} else {
			requestKey, e := automationclient.ID(key)
			if e != nil {
				return e
			}
			out, err = client.ApplyPlan(ctx, v, identity, requestKey)
		}
	case "build run":
		appID, e := automationclient.ID(application)
		if e != nil {
			return e
		}
		baseID, e := automationclient.ID(base)
		if e != nil {
			return e
		}
		requestKey, e := automationclient.ID(key)
		if e != nil {
			return e
		}
		out, err = client.RunBuild(ctx, v, appID, api.RunConfiguredBuild{BaseRevisionId: baseID, TargetHash: hash, IdempotencyKey: requestKey})
	}
	if err != nil {
		return err
	}
	return output(out)
}

func output(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
