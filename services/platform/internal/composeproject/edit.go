package composeproject

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

// EditImage changes one direct scalar token, leaving every other byte intact.
func EditImage(ctx context.Context, b Bundle, environment map[string]string, service, image string) (Bundle, error) {
	if _, err := reference.ParseNormalizedNamed(image); err != nil {
		return b, fail("", nil, "invalid image reference")
	}
	p, err := LoadStored(ctx, b, environment)
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
	check, err := LoadStored(ctx, result, environment)
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
	p, err := LoadStored(ctx, b, environment)
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
	after, err := LoadStored(ctx, result, environment)
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
		want, _ := stablejson.Marshal(service)
		got, _ := stablejson.Marshal(after.Model.Services[name])
		if !bytes.Equal(want, got) {
			return b, errors.New("volume edit changed unrelated service configuration")
		}
	}
	return result, nil
}
