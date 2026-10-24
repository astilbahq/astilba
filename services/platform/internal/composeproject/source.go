package composeproject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/format"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
)

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type Origin struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}
type Bundle struct {
	Namespace   uuid.UUID `json:"namespace"`
	Entrypoints []string  `json:"entrypoints"`
	Files       []File    `json:"files"`
	Origin      *Origin   `json:"origin,omitempty"`
}
type Diagnostic struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

func (e *Diagnostic) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.Path, e.Line, e.Column, e.Message)
}

type Source struct {
	File  File
	Root  *yaml.Node
	Model map[string]any
}
type Project struct {
	Model       *types.Project
	Sources     []Source
	Files       map[string]string
	Permissions []string
	Ports       []string
}

func ResourceID(namespace uuid.UUID, kind, name string) uuid.UUID {
	return uuid.NewSHA1(namespace, []byte("compose/"+kind+"/"+name))
}
func ValidPath(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\:\x00\r\n")
}
func Resolve(base, name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if !ValidPath(name) || strings.Contains(name, "$") {
		return "", errors.New("use an explicit relative path inside the source bundle")
	}
	return path.Join(base, name), nil
}
func fail(file string, n *yaml.Node, message string) error {
	d := &Diagnostic{Path: file, Line: 1, Column: 1, Message: message}
	if n != nil {
		d.Line = n.Line
		d.Column = n.Column
	}
	return d
}
func value(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func parse(file File) (Source, error) {
	s := Source{File: file}
	var doc yaml.Node
	d := yaml.NewDecoder(strings.NewReader(file.Content))
	if err := d.Decode(&doc); err != nil {
		diagnostic := &Diagnostic{Path: file.Path, Line: 1, Column: 1, Message: "invalid YAML: " + err.Error()}
		if match := regexp.MustCompile(`line ([0-9]+)`).FindStringSubmatch(err.Error()); len(match) > 1 {
			diagnostic.Line, _ = strconv.Atoi(match[1])
		}
		return s, diagnostic
	}
	var extra yaml.Node
	if d.Decode(&extra) != io.EOF || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return s, fail(file.Path, nil, "provide one Compose mapping per file")
	}
	s.Root = doc.Content[0]
	visits := 0
	var check func(*yaml.Node, int) error
	check = func(n *yaml.Node, depth int) error {
		visits++
		if visits > 20000 || depth > 64 {
			return fail(file.Path, n, "YAML structure exceeds the supported limit")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || seen[k.Value] {
					return fail(file.Path, k, "complex or repeated mapping key")
				}
				seen[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if err := check(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(s.Root, 0); err != nil {
		return s, err
	}
	if s.Root.Decode(&s.Model) != nil {
		return s, fail(file.Path, s.Root, "invalid Compose mapping")
	}
	return s, nil
}
func (b Bundle) Validate() error {
	if b.Namespace == uuid.Nil || len(b.Files) == 0 || len(b.Files) > 16 || len(b.Entrypoints) == 0 || len(b.Entrypoints) > 8 {
		return fail("", nil, "provide a namespace, 1–16 files and 1–8 Compose entrypoints")
	}
	files := map[string]bool{}
	total := 0
	for _, f := range b.Files {
		total += len(f.Content)
		if !ValidPath(f.Path) || files[f.Path] || len(f.Content) > 65536 || total > 65536 || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return fail(f.Path, nil, "invalid, repeated or oversized UTF-8 source file")
		}
		for name := range files {
			if strings.HasPrefix(name, f.Path+"/") || strings.HasPrefix(f.Path, name+"/") {
				return fail(f.Path, nil, "a file cannot also be a bundle directory")
			}
		}
		files[f.Path] = true
	}
	seen := map[string]bool{}
	for _, p := range b.Entrypoints {
		if !files[p] || seen[p] {
			return fail(p, nil, "entrypoint must identify one unique bundled file")
		}
		seen[p] = true
	}
	if b.Origin != nil {
		o := b.Origin
		u, err := url.Parse(o.Repository)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fail("", nil, "source repository must be an HTTPS URL without credentials")
		}
		if (o.Provider != "github" && o.Provider != "gitlab" && o.Provider != "gitea") || !regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`).MatchString(o.Commit) || len(o.Repository) == 0 || len(o.Repository) > 2048 {
			return fail("", nil, "record an exact Git source commit")
		}
	}
	return nil
}
func allowed(model map[string]any, keys string, file string, node *yaml.Node) error {
	for k := range model {
		if strings.HasPrefix(k, "x-") {
			continue
		}
		if !strings.Contains(" "+keys+" ", " "+k+" ") {
			return fail(file, value(node, k), "unsupported Compose property: "+k)
		}
	}
	return nil
}
func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func stringList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := []string{}
		for _, v := range x {
			if s, ok := v.(string); ok {
				out = append(out, s)
			} else {
				return nil
			}
		}
		return out
	}
	return nil
}

// Load receives every interpolation value explicitly and never reads the host environment or files.
func Load(ctx context.Context, b Bundle, environment map[string]string) (*Project, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	p := &Project{Files: map[string]string{}, Permissions: []string{}, Ports: []string{}}
	for _, f := range b.Files {
		p.Files[f.Path] = f.Content
	}
	details := types.ConfigDetails{WorkingDir: "/astilba-bundle", Environment: types.Mapping(environment)}
	seen, active := map[string]bool{}, map[string]bool{}
	declarations := map[string]string{}
	var add func(string, string, bool) error
	add = func(filename, base string, included bool) error {
		if active[filename] {
			return fail(filename, nil, "include cycle")
		}
		if seen[filename] {
			return fail(filename, nil, "file is included more than once")
		}
		active[filename] = true
		content, ok := p.Files[filename]
		if !ok {
			return fail(filename, nil, "referenced file is absent from the source bundle")
		}
		src, err := parse(File{filename, content})
		if err != nil {
			return err
		}
		if err = allowed(src.Model, "version name services networks volumes configs secrets include", filename, src.Root); err != nil {
			return err
		}
		if inc, ok := src.Model["include"]; ok {
			names := stringList(inc)
			if len(names) == 0 {
				return fail(filename, value(src.Root, "include"), "include supports a list of explicit local file paths")
			}
			for _, n := range names {
				target, e := Resolve(path.Dir(filename), n)
				if e != nil {
					return fail(filename, value(src.Root, "include"), e.Error())
				}
				if e = add(target, path.Dir(target), true); e != nil {
					return e
				}
			}
			delete(src.Model, "include")
		}
		for _, section := range []string{"networks", "volumes"} {
			for name, raw := range asMap(src.Model[section]) {
				m := asMap(raw)
				if m == nil && raw != nil {
					return fail(filename, value(src.Root, section), "resource must be a mapping")
				}
				keys := "driver"
				if section == "networks" {
					keys += " internal"
				}
				if err := allowed(m, keys, filename, value(value(src.Root, section), name)); err != nil {
					return err
				}
			}
		}
		for _, section := range []string{"services", "networks", "volumes", "configs", "secrets"} {
			for name := range asMap(src.Model[section]) {
				key := section + "/" + name
				if old, exists := declarations[key]; exists && (included || strings.HasPrefix(old, "include:")) {
					return fail(filename, value(src.Root, section), "included resource conflicts with "+key)
				}
				if included {
					declarations[key] = "include:" + filename
				} else {
					declarations[key] = filename
				}
			}
		}
		services := asMap(src.Model["services"])
		for name, raw := range services {
			service := asMap(raw)
			node := value(value(src.Root, "services"), name)
			if service == nil {
				return fail(filename, node, "service must be a mapping")
			}
			if err = allowed(service, "image command entrypoint environment env_file labels label_file ports expose volumes networks depends_on healthcheck restart stop_grace_period stop_signal user working_dir hostname domainname init read_only tmpfs cap_add cap_drop privileged security_opt cpus cpu_shares mem_limit mem_reservation memswap_limit pids_limit shm_size ulimits tty stdin_open logging configs secrets", filename, node); err != nil {
				return err
			}
			for _, key := range []string{"env_file", "label_file"} {
				if raw, exists := service[key]; exists {
					names := stringList(raw)
					if len(names) == 0 {
						return fail(filename, value(node, key), key+" requires explicit bundled file paths")
					}
					paths := make([]any, 0, len(names))
					for _, name := range names {
						target, err := Resolve(base, name)
						if err != nil {
							return fail(filename, value(node, key), err.Error())
						}
						if _, ok := p.Files[target]; !ok {
							return fail(filename, value(node, key), "referenced file is absent: "+target)
						}
						paths = append(paths, target)
					}
					service[key] = paths
				}
			}
			for _, key := range []string{"configs", "secrets"} {
				if refs, ok := service[key].([]any); ok {
					for _, ref := range refs {
						if mapping := asMap(ref); mapping != nil {
							if err := allowed(mapping, "source target", filename, value(node, key)); err != nil {
								return err
							}
						}
					}
				}
			}
			if v, ok := service["volumes"]; ok {
				list, ok := v.([]any)
				if !ok {
					return fail(filename, value(node, "volumes"), "volumes must be a list")
				}
				for i, raw := range list {
					if text, ok := raw.(string); ok {
						vol, e := format.ParseVolume(text)
						if e != nil {
							return fail(filename, value(node, "volumes"), "invalid volume mount")
						}
						if vol.Type == types.VolumeTypeBind {
							target, e := Resolve(base, vol.Source)
							if e != nil {
								return fail(filename, value(node, "volumes"), e.Error())
							}
							vol.Source = target
						}
						list[i] = mapVolume(vol)
					} else {
						m := asMap(raw)
						if m == nil {
							return fail(filename, value(node, "volumes"), "invalid volume mount")
						}
						if m["type"] == "bind" {
							name, _ := m["source"].(string)
							target, e := Resolve(base, name)
							if e != nil {
								return fail(filename, value(node, "volumes"), e.Error())
							}
							m["source"] = target
						}
					}
				}
			}
		}
		for _, section := range []string{"configs", "secrets"} {
			for name, raw := range asMap(src.Model[section]) {
				m := asMap(raw)
				node := value(value(src.Root, section), name)
				if m == nil {
					return fail(filename, node, "file resource must be a mapping")
				}
				keys := "file content"
				if section == "secrets" {
					keys = "environment"
				}
				if e := allowed(m, keys, filename, node); e != nil {
					return e
				}
				if file, ok := m["file"].(string); ok {
					target, e := Resolve(base, file)
					if e != nil {
						return fail(filename, node, e.Error())
					}
					if _, ok := p.Files[target]; !ok {
						return fail(filename, node, "referenced file is absent: "+target)
					}
					m["file"] = target
				}
				if section == "secrets" {
					name, ok := m["environment"].(string)
					if !ok || environment[name] == "" {
						return fail(filename, node, "secrets require an explicitly supplied secret variable")
					}
				}
			}
		}
		p.Sources = append(p.Sources, src)
		details.ConfigFiles = append(details.ConfigFiles, types.ConfigFile{Filename: filename, Config: src.Model})
		seen[filename] = true
		delete(active, filename)
		return nil
	}
	for _, name := range b.Entrypoints {
		if err := add(name, path.Dir(b.Entrypoints[0]), false); err != nil {
			return nil, err
		}
	}
	project, err := loader.LoadWithContext(ctx, details, func(o *loader.Options) {
		o.SetProjectName("astilba", true)
		o.ResolvePaths = false
		o.SkipInclude = true
		o.SkipExtends = true
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
	})
	if err != nil {
		message := "Compose validation failed: " + err.Error()
		if match := regexp.MustCompile(`services\.([a-zA-Z0-9_-]+)(?:\.([a-zA-Z0-9_-]+))?`).FindStringSubmatch(err.Error()); len(match) > 1 {
			key := ""
			if len(match) > 2 {
				key = match[2]
			}
			return nil, p.At("services", match[1], key, message)
		}
		return nil, fail(b.Entrypoints[0], nil, message)
	}
	p.Model = project
	if len(project.Services) > 20 || len(project.Networks) > 20 || len(project.Volumes) > 40 {
		return nil, fail(b.Entrypoints[0], nil, "supports at most 20 services/networks and 40 named volumes")
	}
	for name, v := range project.Networks {
		if v.External || (v.Driver != "" && v.Driver != "bridge") || len(v.DriverOpts) > 0 || v.Ipam.Driver != "" || len(v.Ipam.Config) > 0 || v.Name != "astilba_"+name {
			return nil, p.At("networks", name, "", "external names, drivers and IPAM are unsupported")
		}
	}
	for name, v := range project.Volumes {
		if v.External || (v.Driver != "" && v.Driver != "local") || len(v.DriverOpts) > 0 || v.Name != "astilba_"+name {
			return nil, p.At("volumes", name, "", "external names and volume drivers are unsupported")
		}
	}
	for name, service := range project.Services {
		// Resolve bundled files after Compose merges and interpolates the source model.
		service.Environment = service.Environment.Resolve(func(key string) (string, bool) { v, ok := environment[key]; return v, ok })
		values := service.Environment.ToMapping()
		lookup := func(key string) (string, bool) {
			if v, ok := environment[key]; ok {
				return v, true
			}
			if v, ok := service.Environment[key]; ok && v != nil {
				return *v, true
			}
			return "", false
		}
		for _, file := range service.EnvFiles {
			data, ok := p.Files[file.Path]
			if !ok {
				return nil, p.At("services", name, "env_file", "referenced file is absent from the bundle")
			}
			if err := dotenv.ParseWithFormat(strings.NewReader(data), file.Path, values, lookup, ""); err != nil {
				return nil, fail(file.Path, nil, "invalid environment file")
			}
		}
		service.Environment = values.ToMappingWithEquals().OverrideBy(service.Environment)
		service.EnvFiles = nil
		labels := types.MappingWithEquals{}
		for _, file := range service.LabelFiles {
			data, ok := p.Files[file]
			if !ok {
				return nil, p.At("services", name, "label_file", "referenced file is absent from the bundle")
			}
			values := types.Mapping{}
			if err := dotenv.ParseWithFormat(strings.NewReader(data), file, values, func(key string) (string, bool) {
				v, ok := labels[key]
				if ok && v != nil {
					return *v, true
				}
				return "", false
			}, ""); err != nil {
				return nil, fail(file, nil, "invalid label file")
			}
			labels.OverrideBy(values.ToMappingWithEquals())
		}
		service.Labels = types.NewLabelsFromMappingWithEquals(labels.OverrideBy(service.Labels.ToMappingWithEquals()))
		service.LabelFiles = nil
		if _, err := reference.ParseNormalizedNamed(service.Image); err != nil || len(service.Image) > 512 {
			return nil, p.At("services", name, "image", "provide a valid image reference; builds are a separate workflow")
		}
		if service.Privileged {
			p.Permissions = append(p.Permissions, name+": privileged container (host-level access)")
		}
		if len(service.CapAdd) > 0 {
			p.Permissions = append(p.Permissions, name+": adds Linux capabilities "+strings.Join(service.CapAdd, ", "))
		}
		for dep, settings := range service.DependsOn {
			if _, ok := project.Services[dep]; !ok || !settings.Required || (settings.Condition != "service_started" && settings.Condition != "service_healthy") {
				return nil, p.At("services", name, "depends_on", "dependencies must be required running services with started or healthy conditions")
			}
		}
		for _, network := range service.Networks {
			if network != nil && (network.Ipv4Address != "" || network.Ipv6Address != "" || network.MacAddress != "") {
				return nil, p.At("services", name, "networks", "static network addresses are unsupported")
			}
		}
		for _, option := range service.SecurityOpt {
			if option != "no-new-privileges:true" && option != "no-new-privileges" {
				return nil, p.At("services", name, "security_opt", "only no-new-privileges is supported")
			}
		}
		for key := range service.Labels {
			if strings.HasPrefix(key, "com.astilba.") || strings.HasPrefix(key, "com.docker.compose.") {
				return nil, p.At("services", name, "labels", "runtime ownership labels are reserved")
			}
		}
		for key, v := range service.Environment {
			if v == nil {
				if text, ok := environment[key]; ok {
					service.Environment[key] = &text
				} else {
					delete(service.Environment, key)
				}
			}
		}

		for _, mount := range service.Volumes {
			if !path.IsAbs(mount.Target) || path.Clean(mount.Target) != mount.Target || mount.Target == "/" || strings.ContainsAny(mount.Target, "\x00\r\n") {
				return nil, p.At("services", name, "volumes", "mount target must be a clean absolute container path other than root")
			}
			if mount.Type == "bind" {
				if !mount.ReadOnly || p.Files[mount.Source] == "" {
					if _, ok := p.Files[mount.Source]; !ok || !mount.ReadOnly {
						return nil, p.At("services", name, "volumes", "bind mounts must be read-only bundled files")
					}
				}
				if mount.Bind != nil && (mount.Bind.Propagation != "" || mount.Bind.SELinux != "" || mount.Bind.Recursive != "") {
					return nil, p.At("services", name, "volumes", "host bind options are unsupported")
				}
			} else if mount.Type != "volume" || mount.Source == "" {
				return nil, p.At("services", name, "volumes", "use a named volume or read-only bundled file")
			}
		}
		for _, port := range service.Ports {
			n, e := strconv.Atoi(port.Published)
			if e != nil || n < 1024 || n > 65535 || n == 8080 || (port.Protocol != "tcp" && port.Protocol != "udp") || (port.HostIP != "" && port.HostIP != "0.0.0.0" && port.HostIP != "127.0.0.1") {
				return nil, p.At("services", name, "ports", "use a fixed TCP/UDP host port 1024–65535 (excluding reserved 8080) on loopback or all interfaces")
			}
			claim := fmt.Sprintf("%s:%d", port.Protocol, n)
			for _, old := range p.Ports {
				if old == claim {
					return nil, p.At("services", name, "ports", "host port is already used in this project")
				}
			}
			p.Ports = append(p.Ports, claim)
			address := port.HostIP
			if address == "" {
				address = "daemon default interfaces"
			}
			p.Permissions = append(p.Permissions, fmt.Sprintf("%s: publishes %s on %s to container port %d", name, claim, address, port.Target))
		}
		project.Services[name] = service
	}
	sort.Strings(p.Permissions)
	sort.Strings(p.Ports)
	return p, nil
}
func mapVolume(v types.ServiceVolumeConfig) map[string]any {
	raw, _ := json.Marshal(v)
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	return result
}

func (p *Project) At(section, name, key, message string) error {
	for i := len(p.Sources) - 1; i >= 0; i-- {
		s := p.Sources[i]
		n := value(value(s.Root, section), name)
		if n != nil {
			if key != "" {
				if v := value(n, key); v != nil {
					n = v
				}
			}
			return fail(s.File.Path, n, message)
		}
	}
	return fail("", nil, message)
}

func (p *Project) SourceLocation(section, name, key string) Diagnostic {
	e := p.At(section, name, key, "").(*Diagnostic)
	return *e
}
func ParseDiagnostic(err error) Diagnostic {
	var d *Diagnostic
	if errors.As(err, &d) {
		return *d
	}
	return Diagnostic{Line: 1, Column: 1, Message: err.Error()}
}

// EditImage changes one direct scalar token, leaving every other byte intact.
func EditImage(ctx context.Context, b Bundle, environment map[string]string, service, image string) (Bundle, error) {
	if _, err := reference.ParseNormalizedNamed(image); err != nil {
		return b, fail("", nil, "invalid image reference")
	}
	p, err := Load(ctx, b, environment)
	if err != nil {
		return b, err
	}
	index := -1
	var target *yaml.Node
	for _, src := range p.Sources {
		services := value(src.Root, "services")
		node := value(services, service)
		if node == nil {
			continue
		}
		if node.Kind != yaml.MappingNode || node.Anchor != "" || node.Style&yaml.FlowStyle != 0 || value(node, "<<") != nil {
			return b, fail(src.File.Path, node, "ambiguous service; use the file editor")
		}
		if n := value(node, "image"); n != nil {
			for i, f := range b.Files {
				if f.Path == src.File.Path {
					index = i
					target = n
				}
			}
		}
	}
	if index < 0 || target.Kind != yaml.ScalarNode || target.Tag != "!!str" || target.Anchor != "" || strings.Contains(target.Value, "$") {
		file := b.Entrypoints[0]
		if index >= 0 {
			file = b.Files[index].Path
		}
		return b, fail(file, target, "image is missing, aliased, anchored or interpolated; use the file editor")
	}
	f := b.Files[index]
	start, end, err := scalarSpan([]byte(f.Content), target)
	if err != nil {
		return b, fail(f.Path, target, "image is not an unambiguous single-line scalar; use the file editor")
	}
	replacement := image
	if target.Style == yaml.DoubleQuotedStyle {
		replacement = strconv.Quote(image)
	} else if target.Style == yaml.SingleQuotedStyle {
		replacement = "'" + image + "'"
	}
	result := b
	result.Files = append([]File(nil), b.Files...)
	result.Files[index].Content = f.Content[:start] + replacement + f.Content[end:]
	check, err := Load(ctx, result, environment)
	if err != nil {
		return b, err
	}
	if check.Model.Services[service].Image != image {
		return b, fail(f.Path, target, "edit does not own the effective image; use the file editor")
	}
	return result, nil
}
func scalarSpan(data []byte, n *yaml.Node) (int, int, error) {
	lines := bytes.Split(data, []byte{'\n'})
	if n.Line < 1 || n.Line > len(lines) {
		return 0, 0, errors.New("invalid line")
	}
	line := lines[n.Line-1]
	column := 0
	for count := 1; count < n.Column; count++ {
		if column >= len(line) {
			return 0, 0, errors.New("invalid column")
		}
		_, size := utf8.DecodeRune(line[column:])
		column += size
	}
	start := column
	for _, l := range lines[:n.Line-1] {
		start += len(l) + 1
	}
	end := column
	if end >= len(line) {
		return 0, 0, errors.New("missing scalar")
	}
	switch n.Style {
	case yaml.DoubleQuotedStyle:
		if line[end] != '"' {
			break
		}
		end++
		for end < len(line) {
			if line[end] == '\\' {
				end += 2
				continue
			}
			if line[end] == '"' {
				return start, start + end - column + 1, nil
			}
			end++
		}
	case yaml.SingleQuotedStyle:
		if line[end] != '\'' {
			break
		}
		end++
		for end < len(line) {
			if line[end] == '\'' {
				if end+1 < len(line) && line[end+1] == '\'' {
					end += 2
					continue
				}
				return start, start + end - column + 1, nil
			}
			end++
		}
	case 0:
		for end < len(line) && !strings.ContainsRune(" \t\r#", rune(line[end])) {
			end++
		}
		if string(line[column:end]) == n.Value {
			return start, start + end - column, nil
		}
	}
	return 0, 0, errors.New("ambiguous scalar")
}

// RenameVolume edits explicit volume scalars only. Includes and comments are
// preserved; ambiguous aliases, interpolation and inherited mounts are refused.
func RenameVolume(ctx context.Context, b Bundle, environment map[string]string, old, next string) (Bundle, error) {
	p, err := Load(ctx, b, environment)
	if err != nil {
		return b, err
	}
	if _, ok := p.Model.Volumes[old]; !ok {
		return b, errors.New("source volume absent")
	}
	if _, ok := p.Model.Volumes[next]; ok {
		return b, errors.New("target volume already declared")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,100}$`).MatchString(next) {
		return b, errors.New("invalid volume name")
	}
	type edit struct {
		start, end int
		value      string
	}
	edits := map[string][]edit{}
	add := func(src Source, n *yaml.Node, replacement string, key bool) error {
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || n.Anchor != "" || strings.Contains(n.Value, "$") {
			return fail(src.File.Path, n, "volume needs an explicit scalar; use the file editor")
		}
		start, end, err := scalarSpan([]byte(src.File.Content), n)
		if err != nil && key && n.Style == 0 {
			colon := *n
			colon.Value += ":"
			start, end, err = scalarSpan([]byte(src.File.Content), &colon)
			end--
		}
		if err != nil {
			return err
		}
		if n.Style == yaml.SingleQuotedStyle {
			replacement = "'" + strings.ReplaceAll(replacement, "'", "''") + "'"
		} else if n.Style == yaml.DoubleQuotedStyle {
			replacement = strconv.Quote(replacement)
		}
		edits[src.File.Path] = append(edits[src.File.Path], edit{start, end, replacement})
		return nil
	}
	definitions := 0
	for _, src := range p.Sources {
		volumes := value(src.Root, "volumes")
		if volumes != nil && volumes.Kind == yaml.MappingNode {
			for i := 0; i < len(volumes.Content); i += 2 {
				if volumes.Content[i].Value == old {
					definitions++
					if err = add(src, volumes.Content[i], next, true); err != nil {
						return b, err
					}
				}
			}
		}
		services := value(src.Root, "services")
		if services == nil || services.Kind != yaml.MappingNode {
			continue
		}
		for i := 1; i < len(services.Content); i += 2 {
			mounts := value(services.Content[i], "volumes")
			if mounts == nil || mounts.Kind != yaml.SequenceNode {
				continue
			}
			for _, n := range mounts.Content {
				if n.Kind == yaml.MappingNode {
					source := value(n, "source")
					if source != nil && source.Value == old {
						if err = add(src, source, next, false); err != nil {
							return b, err
						}
					}
				} else if n.Kind == yaml.ScalarNode && strings.HasPrefix(n.Value, old+":") {
					if err = add(src, n, next+strings.TrimPrefix(n.Value, old), false); err != nil {
						return b, err
					}
				}
			}
		}
	}
	if definitions != 1 {
		return b, errors.New("volume has ambiguous definitions; use the file editor")
	}
	result := b
	result.Files = append([]File(nil), b.Files...)
	for i, f := range result.Files {
		list := edits[f.Path]
		sort.Slice(list, func(i, j int) bool { return list[i].start > list[j].start })
		for _, e := range list {
			f.Content = f.Content[:e.start] + e.value + f.Content[e.end:]
		}
		result.Files[i] = f
	}
	after, err := Load(ctx, result, environment)
	if err != nil {
		return b, err
	}
	if _, exists := after.Model.Volumes[old]; exists {
		return b, errors.New("an inherited volume reference remains")
	}
	if _, exists := after.Model.Volumes[next]; !exists {
		return b, errors.New("renamed volume is missing")
	}
	for name, service := range p.Model.Services {
		service.Volumes = append([]types.ServiceVolumeConfig(nil), service.Volumes...)
		for i := range service.Volumes {
			if service.Volumes[i].Type == "volume" && service.Volumes[i].Source == old {
				service.Volumes[i].Source = next
			}
		}
		want, _ := json.Marshal(service)
		got, _ := json.Marshal(after.Model.Services[name])
		if !bytes.Equal(want, got) {
			return b, errors.New("volume edit changed unrelated service configuration")
		}
	}
	return result, nil
}
