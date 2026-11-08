package composeproject

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/go-units"
)

// Load reads a bundle that is about to become a new draft or plan, and also refuses what
// a unit on a shared host must not be able to declare.
func Load(ctx context.Context, b Bundle, environment map[string]string) (*Project, error) {
	p, err := load(ctx, b, environment)
	if err != nil {
		return nil, err
	}
	if err = p.admit(); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadStored reads a bundle already saved in a revision, frozen plan or archive. Recovery,
// restores, cleanup and backups re-read those, and an offline bundle cannot be edited, so
// a rule added since it was saved must not stop it loading. A new plan refuses it instead.
func LoadStored(ctx context.Context, b Bundle, environment map[string]string) (*Project, error) {
	return load(ctx, b, environment)
}

func (p *Project) admit() error {
	for _, name := range p.Model.ServiceNames() {
		service := p.Model.Services[name]
		// The compiler adds the service name as an alias, and Docker DNS answers for a
		// hostname as it does for an alias.
		if problem := runtimeNameProblem(name); problem != "" {
			return p.At("services", name, "", "rename this service: "+problem)
		}
		if problem := runtimeNameProblem(service.Hostname); problem != "" {
			return p.At("services", name, "hostname", "change this hostname: "+problem)
		}
		for _, network := range service.Networks {
			if network == nil {
				continue
			}
			for _, alias := range network.Aliases {
				if problem := runtimeNameProblem(alias); problem != "" {
					return p.At("services", name, "networks", "change this alias: "+problem)
				}
			}
		}
		if message := unsupportedLogging(service.Logging); message != "" {
			return p.At("services", name, "logging", message)
		}
	}
	return nil
}

var reservedRuntimeNames = regexp.MustCompile(`^(service-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|astilba-)`)

// runtimeNameProblem says why a unit may not declare a DNS name, which could answer for
// one the runtime dials or gives its own resources. Docker DNS ignores case, and looks up
// a dotted name as a whole on every network before it treats the last part as a network,
// so a reserved name stays reserved whatever follows it. Only a placeholder for a secret
// reaches this check, and the host would use a value nothing has checked.
func runtimeNameProblem(name string) string {
	if strings.ContainsRune(name, 0) {
		return "use a plain value, because a secret cannot be checked against reserved names"
	}
	if reservedRuntimeNames.MatchString(strings.ToLower(name)) {
		return "names starting with service-<uuid> or astilba- are reserved for the runtime"
	}
	return ""
}

// maxContainerLogBytes is the most a logging block may keep for one container.
const maxContainerLogBytes = 1 << 30

// unsupportedLogging says what to change in a logging block, or nothing if it is fine.
// Other drivers make the host's Docker daemon, outside any container's network, deliver
// to an address the file chooses, so only drivers that write locally are accepted. A host
// owner can still send logs elsewhere with the daemon's default driver and options, which
// also fill in whatever a block leaves out. Sizes and defaults are those of the two drivers,
// and the daemon refuses to start a container on compression with only one log file.
func unsupportedLogging(logging *types.LoggingConfig) string {
	if logging == nil {
		return ""
	}
	options := logging.Options
	switch logging.Driver {
	case "", "json-file", "local":
	case "none":
		if len(options) > 0 {
			return "remove these logging options: the none driver takes no options"
		}
	default:
		return "change the logging driver to json-file, local or none, or remove the logging block to use the host's default"
	}
	for key := range options {
		if key != "max-size" && key != "max-file" && key != "compress" {
			return "remove this logging option: only max-size, max-file and compress are supported"
		}
	}
	local := logging.Driver == "local"
	size, files := int64(0), int64(1)
	if local {
		size, files = 20_000_000, 5
	}
	value, sized := options["max-size"]
	if sized {
		var err error
		if size, err = units.FromHumanSize(value); err != nil || size <= 0 || size > maxContainerLogBytes {
			return "set logging max-size to a size such as 10m, at most 1g"
		}
	} else if !local && (logging.Driver == "json-file" || len(options) > 0) {
		return "add logging max-size, such as 10m, because this driver keeps logs without limit otherwise"
	}
	if value, ok := options["max-file"]; ok {
		count, err := strconv.ParseInt(value, 10, 32)
		if err != nil || count < 1 {
			return "set logging max-file to a whole number of at least 1"
		}
		files = count
	}
	if size > 0 && files > maxContainerLogBytes/size {
		return "reduce logging max-size or max-file: together they can keep at most 1 GiB per container"
	}
	compress := local
	if value, ok := options["compress"]; ok {
		var err error
		if compress, err = strconv.ParseBool(value); err != nil {
			return "set logging compress to true or false"
		}
	}
	if compress && files < 2 {
		return "set logging max-file to at least 2 or compress to false, because compression only applies to rotated files"
	}
	return ""
}
