// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

type SourceFile struct {
	Path string
	Data []byte
}

// Runtime values are validation inputs, never the source written back to Git.
type Bundle struct {
	Files       []SourceFile
	Environment map[string]string
	Secrets     []string
}

type FileEditorError struct {
	Path   string
	Line   int
	Reason string
}

func (e *FileEditorError) Error() string {
	return fmt.Sprintf("%s:%d: use the file editor: %s", e.Path, e.Line, e.Reason)
}

func validPath(path string) bool {
	return fs.ValidPath(path) && path != "." && !strings.ContainsAny(path, "\\\x00\r\n:")
}

func rejectSecrets(data []byte, secrets []string) error {
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			return errors.New("source contains a runtime secret")
		}
	}
	return nil
}

func (b Bundle) validate() error {
	if len(b.Files) == 0 || len(b.Files) > 16 {
		return errors.New("provide 1–16 explicit source files")
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range b.Files {
		if !validPath(f.Path) || seen[f.Path] {
			return errors.New("invalid or repeated source path")
		}
		seen[f.Path] = true
		if err := rejectSecrets(f.Data, b.Secrets); err != nil {
			return err
		}
		total += len(f.Data)
		if len(f.Data) > maxFileBytes || total > 4*maxFileBytes || !utf8.Valid(f.Data) {
			return errors.New("source exceeds the UTF-8 fixture limit")
		}
	}
	return nil
}

func parseSource(f SourceFile) (*yaml.Node, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(f.Data))
	if err := decoder.Decode(&doc); err != nil {
		return nil, &FileEditorError{f.Path, 1, "invalid YAML"}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, &FileEditorError{f.Path, 1, "multiple YAML documents are unsupported"}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &FileEditorError{f.Path, 1, "expected a Compose mapping"}
	}
	var check func(*yaml.Node) error
	check = func(n *yaml.Node) error {
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || keys[k.Value] {
					return &FileEditorError{f.Path, k.Line, "complex or repeated mapping key"}
				}
				keys[k.Value] = true
			}
		}
		for _, child := range n.Content {
			if err := check(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(&doc); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func directMapping(n *yaml.Node) bool {
	return n.Kind == yaml.MappingNode && n.Anchor == "" && n.Style&yaml.FlowStyle == 0 && mappingValue(n, "<<") == nil
}

func validateCompose(ctx context.Context, b Bundle) (*types.Project, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	details := types.ConfigDetails{WorkingDir: "/fixture", Environment: types.Mapping(b.Environment)}
	for _, f := range b.Files {
		root, err := parseSource(f)
		if err != nil {
			return nil, err
		}
		if mappingValue(root, "include") != nil {
			return nil, &FileEditorError{f.Path, root.Line, "includes need explicit source ownership"}
		}
		// Resolve YAML aliases in memory before checking keys. An include hidden
		// behind a merge key must not reach the Compose resource loader.
		var model map[string]any
		if root.Decode(&model) != nil {
			return nil, &FileEditorError{f.Path, root.Line, "unsupported YAML mapping"}
		}
		if _, ok := model["include"]; ok {
			return nil, &FileEditorError{f.Path, root.Line, "includes need explicit source ownership"}
		}
		services, _ := model["services"].(map[string]any)
		for _, raw := range services {
			service, _ := raw.(map[string]any)
			for _, key := range []string{"extends", "env_file", "label_file"} {
				if _, ok := service[key]; ok {
					return nil, &FileEditorError{f.Path, root.Line, "external-file resolution is outside this fixture"}
				}
			}
		}
		for _, section := range []string{"secrets", "configs"} {
			items, _ := model[section].(map[string]any)
			for _, raw := range items {
				item, _ := raw.(map[string]any)
				if _, ok := item["file"]; ok {
					return nil, &FileEditorError{f.Path, root.Line, "external-file resolution is outside this fixture"}
				}
			}
		}
		details.ConfigFiles = append(details.ConfigFiles, types.ConfigFile{Filename: f.Path, Content: f.Data})
	}
	project, err := loader.LoadWithContext(ctx, details, func(o *loader.Options) {
		o.SetProjectName("plat7", true)
		o.ResolvePaths = false
		o.SkipInclude = true
		o.SkipExtends = true
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
	})
	if err != nil {
		return nil, errors.New("Compose validation failed")
	}
	return project, nil
}

func scalarSpan(data []byte, n *yaml.Node) (int, int, error) {
	lines := bytes.Split(data, []byte{'\n'})
	if n.Line < 1 || n.Line > len(lines) {
		return 0, 0, errors.New("invalid source location")
	}
	line := lines[n.Line-1]
	column := 0
	for count := 1; count < n.Column; count++ {
		if column >= len(line) {
			return 0, 0, errors.New("invalid source column")
		}
		_, size := utf8.DecodeRune(line[column:])
		column += size
	}
	start := 0
	for _, l := range lines[:n.Line-1] {
		start += len(l) + 1
	}
	start += column
	if column >= len(line) {
		return 0, 0, errors.New("missing scalar")
	}
	end := column
	switch n.Style {
	case yaml.DoubleQuotedStyle:
		if line[end] != '"' {
			return 0, 0, errors.New("scalar has a tag or anchor")
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
			return 0, 0, errors.New("scalar has a tag or anchor")
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
	return 0, 0, errors.New("scalar is not an editable single-line token")
}

func EditImage(ctx context.Context, b Bundle, service, image string) (Bundle, error) {
	if _, err := reference.ParseNormalizedNamed(image); err != nil {
		return Bundle{}, errors.New("invalid container image reference")
	}
	if _, err := validateCompose(ctx, b); err != nil {
		return Bundle{}, err
	}
	owner := -1
	var target *yaml.Node
	for i, f := range b.Files {
		root, err := parseSource(f)
		if err != nil {
			return Bundle{}, err
		}
		services := mappingValue(root, "services")
		if services == nil {
			continue
		}
		if !directMapping(root) || !directMapping(services) {
			return Bundle{}, &FileEditorError{f.Path, services.Line, "indirect services mapping"}
		}
		s := mappingValue(services, service)
		if s == nil {
			continue
		}
		if !directMapping(s) {
			return Bundle{}, &FileEditorError{f.Path, s.Line, "indirect service mapping"}
		}
		if n := mappingValue(s, "image"); n != nil {
			owner = i
			target = n
		}
	}
	if owner < 0 {
		return Bundle{}, &FileEditorError{"", 1, "no explicit image value"}
	}
	f := b.Files[owner]
	if target.Kind != yaml.ScalarNode || target.Tag != "!!str" || target.Anchor != "" || strings.Contains(target.Value, "$") {
		return Bundle{}, &FileEditorError{f.Path, target.Line, "image is aliased, anchored or interpolated"}
	}
	start, end, err := scalarSpan(f.Data, target)
	if err != nil {
		return Bundle{}, &FileEditorError{f.Path, target.Line, err.Error()}
	}
	replacement := image
	if target.Style == yaml.DoubleQuotedStyle {
		replacement = strconv.Quote(image)
	}
	if target.Style == yaml.SingleQuotedStyle {
		replacement = "'" + strings.ReplaceAll(image, "'", "''") + "'"
	}
	result := b
	result.Files = append([]SourceFile(nil), b.Files...)
	result.Files[owner].Data = append(append(append([]byte{}, f.Data[:start]...), replacement...), f.Data[end:]...)
	project, err := validateCompose(ctx, result)
	if err != nil {
		return Bundle{}, err
	}
	if project.Services[service].Image != image {
		return Bundle{}, &FileEditorError{f.Path, target.Line, "edit does not own the effective image"}
	}
	return result, nil
}
