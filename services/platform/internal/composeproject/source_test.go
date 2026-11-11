package composeproject

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func fixture(source string) Bundle {
	return Bundle{Namespace: uuid.New(), Entrypoints: []string{"compose.yaml"}, Files: []File{{Path: "compose.yaml", Content: source}}}
}
func TestExplicitInputsAndSourcePreservingEdit(t *testing.T) {
	t.Setenv("NOT_SUPPLIED", "ambient-value")
	source := "# café\nx-shared: &defaults\n  restart: unless-stopped\nservices:\n  web:\n    <<: *defaults\n    image: 'busybox:1.37.0' # keep me\n    env_file: settings.env\n    environment:\n      VALUE: ${INPUT:-default}\n      AMBIENT: ${NOT_SUPPLIED:-isolated}\n    volumes:\n      - ./config.txt:/config.txt:ro\n      - data:/data\nvolumes:\n  data: {}\nx-extra:\n  text: preserved\n"
	b := fixture(source)
	b.Files = append(b.Files, File{Path: "settings.env", Content: "FILE_VALUE=from-file\n"}, File{Path: "config.txt", Content: "example\n"})
	p, err := Load(context.Background(), b, map[string]string{"INPUT": "supplied"})
	if err != nil {
		t.Fatal(err)
	}
	env := p.Model.Services["web"].Environment
	if *env["VALUE"] != "supplied" || *env["AMBIENT"] != "isolated" || *env["FILE_VALUE"] != "from-file" {
		t.Fatal("interpolation escaped explicit inputs")
	}
	if _, err = EditImage(context.Background(), b, map[string]string{}, "web", "busybox:latest"); err == nil {
		t.Fatal("merged service was treated as an unambiguous edit")
	}
	b.Files[0].Content = strings.Replace(source, "    <<: *defaults\n", "    restart: unless-stopped\n", 1)
	next, err := EditImage(context.Background(), b, map[string]string{}, "web", "busybox:latest")
	if err != nil {
		t.Fatal(err)
	}
	if next.Files[0].Content != strings.Replace(b.Files[0].Content, "'busybox:1.37.0'", "'busybox:latest'", 1) || next.Files[1] != b.Files[1] || next.Files[2] != b.Files[2] {
		t.Fatal("edit changed unrelated source bytes")
	}
}
func TestBundleRejectsEscapesAndUnsupportedSemantics(t *testing.T) {
	for name, source := range map[string]string{
		"include escape":  "include: [../secret.yaml]\nservices: {}\n",
		"remote include":  "include: ['https://example.test/app.yaml']\nservices: {}\n",
		"ambient file":    "services:\n  web:\n    image: busybox:1.37.0\n    env_file: /etc/environment\n",
		"host mount":      "services:\n  web:\n    image: busybox:1.37.0\n    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n",
		"build":           "services:\n  web:\n    build: .\n",
		"external volume": "services:\n  web:\n    image: busybox:1.37.0\nvolumes:\n  data:\n    external: true\n",
		"unknown feature": "services:\n  web:\n    image: busybox:1.37.0\n    use_api_socket: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), fixture(source), nil)
			if err == nil {
				t.Fatal("unsafe/unsupported input accepted")
			}
			d := ParseDiagnostic(err)
			if d.Path == "" || d.Line < 1 || d.Column < 1 {
				t.Fatal("missing source location", d)
			}
		})
	}
	b := fixture("services: {}\n")
	b.Files[0].Path = "../compose.yaml"
	if b.Validate() == nil {
		t.Fatal("bundle traversal accepted")
	}
}

// A proxy shares one resolver across units, so a unit that could declare these names
// could answer for another unit's backend. The dotted form is what the proxy dials.
func TestLoadRejectsNamesTheRuntimeOwns(t *testing.T) {
	const victim = "service-11111111-1111-4111-8111-111111111111"
	const image = "    image: busybox:1.37.0\n"
	aliases := func(name string) string {
		return "services:\n  web:\n" + image + "    networks:\n      default:\n        aliases: [" + name + "]\n"
	}
	hostname := func(name string) string {
		return "services:\n  web:\n" + image + "    hostname: " + name + "\n"
	}
	service := func(name string) string {
		return "services:\n  " + name + ":\n" + image
	}
	const alias, rename, host = "change this alias", "rename this service", "change this hostname"
	for name, test := range map[string]struct{ source, want string }{
		"backend alias":              {aliases(victim), alias},
		"qualified backend alias":    {aliases(victim + ".astilba-network-22222222-2222-4222-8222-222222222222"), alias},
		"uppercase backend alias":    {aliases(strings.ToUpper(victim)), alias},
		"container name alias":       {aliases("astilba-11111111-1111-4111-8111-111111111111"), alias},
		"proxy alias":                {aliases("astilba-ingress"), alias},
		"alias on another network":   {"services:\n  web:\n" + image + "    networks:\n      private:\n        aliases: [" + victim + "]\nnetworks:\n  private: {}\n", alias},
		"interpolated alias":         {aliases(`"${TARGET}"`), alias},
		"interpolated hostname":      {hostname(`"${TARGET}"`), host},
		"backend service name":       {service(victim), rename},
		"qualified service name":     {service(victim + ".astilba-network-22222222-2222-4222-8222-222222222222"), rename},
		"uppercase service name":     {service(strings.ToUpper(victim)), rename},
		"prefixed service name":      {service("astilba-worker"), rename},
		"backend hostname":           {hostname(victim), host},
		"uppercase backend hostname": {hostname(strings.ToUpper(victim)), host},
		"prefixed hostname":          {hostname("astilba-ingress"), host},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), fixture(test.source), map[string]string{"TARGET": victim})
			if err == nil {
				t.Fatal("a name the runtime owns was accepted")
			}
			d := ParseDiagnostic(err)
			if !strings.Contains(d.Message, test.want) || !strings.Contains(d.Message, "reserved for the runtime") || d.Path != "compose.yaml" || d.Line < 2 {
				t.Fatalf("rejected for another reason or without a location: %+v", d)
			}
		})
	}
	for _, name := range []string{"service", "service-a", "service-11111111", "astilba", "my-astilba-api", "web"} {
		for _, source := range []string{aliases(name), service(name), hostname(name)} {
			if _, err := Load(context.Background(), fixture(source), nil); err != nil {
				t.Fatalf("ordinary name %q was rejected: %v", name, err)
			}
		}
	}
}

// A revision's derived networks carry these names back to clients, and the API holds a
// network name to 80 characters, so a longer one would make the saved revision unsendable.
func TestLoadRefusesNetworkNamesTheAPICannotReturn(t *testing.T) {
	source := func(name string) string {
		return "services:\n  web:\n    image: busybox:1.37.0\n    networks: [" + name + "]\nnetworks:\n  " + name + ": {}\n"
	}
	long := strings.Repeat("n", maxNetworkName+1)
	_, err := Load(context.Background(), fixture(source(long)), nil)
	if d := ParseDiagnostic(err); err == nil || !strings.Contains(d.Message, "at most 80 characters") || d.Line < 2 {
		t.Fatalf("a network name the API cannot return was accepted or not located: %v", err)
	}
	if _, err := LoadStored(context.Background(), fixture(source(long)), nil); err != nil {
		t.Fatalf("a stored bundle stopped loading: %v", err)
	}
	if _, err := Load(context.Background(), fixture(source(long[:maxNetworkName])), nil); err != nil {
		t.Fatalf("an 80-character network name was refused: %v", err)
	}
}

// Other drivers make the root Docker daemon connect to an address the file chooses, from
// the host's own network, with no owner review.
func TestLoadAcceptsOnlyBoundedLocalLogging(t *testing.T) {
	logging := func(block string) string {
		return "services:\n  web:\n    image: busybox:1.37.0\n    logging:\n" + block
	}
	options := func(driver string, values ...string) string {
		block := ""
		if driver != "" {
			block = "      driver: " + driver + "\n"
		}
		block += "      options:\n"
		for _, value := range values {
			block += "        " + value + "\n"
		}
		return logging(block)
	}
	for _, source := range []string{
		logging("      driver: local\n"),
		logging("      driver: none\n"),
		options("json-file", "max-size: 10m", `max-file: "3"`),
		options("json-file", "max-size: 10m", "max-file: 3"),
		options("json-file", "max-size: 1g"),
		options("json-file", "max-size: 200m", "max-file: 5", "compress: 'true'"),
		options("local", "max-size: 10m"),
		options("local", "max-file: 3"),
		options("local", "compress: 'false'"),
		options("local", "max-file: 1", "compress: 'false'"),
		options("", "max-size: 10m"),
		options("${DRIVER}", "max-size: ${SIZE}"),
	} {
		if _, err := Load(context.Background(), fixture(source), map[string]string{"DRIVER": "local", "SIZE": "10m"}); err != nil {
			t.Fatalf("supported logging rejected: %v\n%s", err, source)
		}
	}
	for name, test := range map[string]struct {
		source string
		want   string
	}{
		"syslog":              {logging("      driver: syslog\n      options:\n        syslog-address: tcp://127.0.0.1:46379\n"), "change the logging driver"},
		"syslog over unix":    {logging("      driver: syslog\n      options:\n        syslog-address: unix:///run/docker.sock\n"), "change the logging driver"},
		"gelf":                {logging("      driver: gelf\n      options:\n        gelf-address: udp://127.0.0.1:12201\n"), "change the logging driver"},
		"fluentd":             {logging("      driver: fluentd\n"), "change the logging driver"},
		"splunk":              {logging("      driver: splunk\n"), "change the logging driver"},
		"awslogs":             {logging("      driver: awslogs\n"), "change the logging driver"},
		"gcplogs":             {logging("      driver: gcplogs\n"), "change the logging driver"},
		"journald":            {logging("      driver: journald\n"), "change the logging driver"},
		"plugin":              {logging("      driver: example/loki:latest\n"), "change the logging driver"},
		"different case":      {logging("      driver: JSON-FILE\n"), "change the logging driver"},
		"interpolated driver": {logging("      driver: ${UNSAFE}\n"), "change the logging driver"},
		"none with options":   {options("none", "max-size: 10m"), "remove these logging options"},
		"address option":      {options("json-file", "syslog-address: tcp://127.0.0.1:46379"), "remove this logging option"},
		"tag option":          {options("json-file", "tag: '{{.Name}}'"), "remove this logging option"},
		"size not a size":     {options("json-file", "max-size: lots"), "set logging max-size"},
		"size zero":           {options("json-file", "max-size: '0'"), "set logging max-size"},
		"size too large":      {options("json-file", "max-size: 2g"), "set logging max-size"},
		"files not a number":  {options("json-file", "max-size: 10m", "max-file: many"), "set logging max-file"},
		"files zero":          {options("json-file", "max-size: 10m", "max-file: 0"), "set logging max-file"},
		"no size":             {logging("      driver: json-file\n"), "add logging max-size"},
		"no driver or size":   {options("", "compress: 'false'"), "add logging max-size"},
		"files without size":  {options("json-file", "max-file: 3"), "add logging max-size"},
		"total too large":     {options("json-file", "max-size: 200m", "max-file: 6"), "reduce logging max-size or max-file"},
		"huge file count":     {options("json-file", "max-size: 1g", "max-file: 2147483647"), "reduce logging max-size or max-file"},
		"local default files": {options("local", "max-size: 300m"), "reduce logging max-size or max-file"},
		"local default size":  {options("local", "max-file: 60"), "reduce logging max-size or max-file"},
		"compress not a bool": {options("json-file", "max-size: 10m", "max-file: 3", "compress: maybe"), "set logging compress"},
		"compress one file":   {options("json-file", "max-size: 10m", "compress: 'true'"), "set logging max-file"},
		"local one file":      {options("local", "max-file: 1"), "set logging max-file"},
		"local compress one":  {options("local", "max-file: 1", "compress: 'true'"), "set logging max-file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), fixture(test.source), map[string]string{"UNSAFE": "syslog"})
			if err == nil {
				t.Fatal("unsupported logging accepted")
			}
			if d := ParseDiagnostic(err); !strings.Contains(d.Message, test.want) || d.Path != "compose.yaml" || d.Line < 4 {
				t.Fatalf("rejected for another reason or without a location: %+v", d)
			}
		})
	}
}

// Only a secret's placeholder is visible when a plan is reviewed, so a secret could carry
// a reserved name past the check to a host, which uses whatever the secret holds.
func TestLoadRejectsNamesFromSecrets(t *testing.T) {
	secret := map[string]string{"SECRET": "\x00ASTILBA_SECRET_11111111-1111-4111-8111-111111111111\x00"}
	for _, source := range []string{
		"services:\n  web:\n    image: busybox:1.37.0\n    hostname: \"${SECRET}\"\n",
		"services:\n  web:\n    image: busybox:1.37.0\n    networks:\n      default:\n        aliases: [\"web-${SECRET}\"]\n",
	} {
		_, err := Load(context.Background(), fixture(source), secret)
		if err == nil || !strings.Contains(ParseDiagnostic(err).Message, "use a plain value") {
			t.Fatalf("%s: %v", source, err)
		}
	}
}

// Saved files are re-read by recovery, restores, cleanup and backups, so what the new
// rules refuse must still load from storage and keep its meaning there.
func TestLoadStoredKeepsFilesSavedBeforeTheRules(t *testing.T) {
	const source = "services:\n  astilba-worker:\n    image: busybox:1.37.0\n    hostname: astilba-x\n    logging:\n      driver: syslog\n      options:\n        syslog-address: tcp://127.0.0.1:514\n    networks:\n      default:\n        aliases: [service-11111111-1111-4111-8111-111111111111]\n"
	if _, err := Load(context.Background(), fixture(source), nil); err == nil {
		t.Fatal("new input was not refused")
	}
	p, err := LoadStored(context.Background(), fixture(source), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := p.Model.Services["astilba-worker"]
	if service.Logging.Driver != "syslog" || service.Hostname != "astilba-x" {
		t.Fatalf("stored file changed meaning: %+v", service)
	}
	edited, err := EditImage(context.Background(), fixture(source), nil, "astilba-worker", "busybox:latest")
	if err != nil || !strings.Contains(edited.Files[0].Content, "busybox:latest") {
		t.Fatalf("a stored file could not take a new image: %v", err)
	}
}
func TestLocalIncludesAndElevatedPlanInputs(t *testing.T) {
	b := fixture("include: [parts/worker.yaml]\nservices:\n  web:\n    image: busybox:1.37.0\n    privileged: true\n    ports: ['127.0.0.1:18080:8080']\n")
	b.Files = append(b.Files, File{Path: "parts/worker.yaml", Content: "services:\n  worker:\n    image: busybox:1.37.0\n    env_file: worker.env\n"}, File{Path: "parts/worker.env", Content: "MODE=worker\n"})
	p, err := Load(context.Background(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Model.Services) != 2 || len(p.Permissions) != 2 || len(p.Ports) != 1 || *p.Model.Services["worker"].Environment["MODE"] != "worker" {
		t.Fatal("included source or permission requirements lost")
	}
	b.Files[1].Content = "services:\n  web:\n    image: busybox:latest\n"
	if _, err = Load(context.Background(), b, nil); err == nil {
		t.Fatal("conflicting included service silently merged")
	}
}

func TestBundledFilesResolveOnceAfterSourceMerge(t *testing.T) {
	b := fixture("services:\n  web:\n    image: busybox:1.37.0\n    environment: {MODE: explicit}\n")
	b.Entrypoints = append(b.Entrypoints, "override.yaml")
	b.Files = append(b.Files, File{Path: "override.yaml", Content: "services:\n  web:\n    env_file: vars.env\n    label_file: labels.env\n"}, File{Path: "vars.env", Content: "MODE=from-file\nLITERAL='cash$HOME'\nEXPANDED=${INPUT}\n"}, File{Path: "labels.env", Content: "example.label='cash$HOME'\n"})
	p, err := Load(context.Background(), b, map[string]string{"HOME": "expanded", "INPUT": "cash$HOME"})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Model.Services["web"]
	if *s.Environment["MODE"] != "explicit" || *s.Environment["LITERAL"] != "cash$HOME" || *s.Environment["EXPANDED"] != "cash$HOME" || s.Labels["example.label"] != "cash$HOME" {
		t.Fatalf("file merge/interpolation changed values: %+v", s)
	}
}

func TestRestoreVolumeRenamePreservesSource(t *testing.T) {
	source := "# recovery fixture\nservices:\n  web:\n    image: busybox:1.37.0\n    volumes:\n      - 'data:/data' # retained comment\nvolumes:\n  data: {} # storage\nx-note: untouched\n"
	b := fixture(source)
	next, err := RenameVolume(context.Background(), b, map[string]string{}, "data", "restored-fixture")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(strings.ReplaceAll(source, "'data:/data'", "'restored-fixture:/data'"), "  data: {}", "  restored-fixture: {}")
	if next.Files[0].Content != want {
		t.Fatal("volume cutover changed unrelated source bytes")
	}
	aliased := fixture("services:\n  web:\n    image: busybox:1.37.0\n    volumes: &mounts [data:/data]\n  other:\n    image: busybox:1.37.0\n    volumes: *mounts\nvolumes:\n  data: {}\n")
	if _, err = RenameVolume(context.Background(), aliased, map[string]string{}, "data", "restored-fixture"); err == nil {
		t.Fatal("ambiguous aliased mounts were silently rewritten")
	}
}
