package config

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// WithVNCCompatibility changes only whether the ordinary VNC listener is enabled.
func WithVNCCompatibility(original []byte, enabled bool) ([]byte, error) {
	before, err := parseSettings(bytes.NewReader(original))
	if err != nil {
		return nil, err
	}
	if before.VNC.Compatibility.Enabled == enabled {
		return original, nil
	}
	document, err := yamlSettings(original)
	if err != nil {
		return nil, err
	}
	vnc, err := yamlSettingsSection(document.Content[0], "vnc", before.VNC)
	if err != nil {
		return nil, err
	}
	compatibility, err := yamlSettingsSection(vnc, "compatibility", before.VNC.Compatibility)
	if err != nil {
		return nil, err
	}
	entry := yamlField(compatibility, "enabled")
	entry.Kind, entry.Tag, entry.Value, entry.Style = yaml.ScalarNode, "!!bool", strconv.FormatBool(enabled), 0
	entry.Alias, entry.Content = nil, nil
	updated, err := encodeYAMLSettings(document)
	if err != nil {
		return nil, err
	}
	after, err := parseSettings(bytes.NewReader(updated))
	if err != nil {
		return nil, err
	}
	before.VNC.Compatibility.Enabled = enabled
	if after != before {
		return nil, errors.New("vnc.compatibility.enabled shares a YAML anchor with another setting; edit the YAML directly")
	}
	if _, err := after.validateSettings(); err != nil {
		return nil, err
	}
	return updated, nil
}

func yamlSettings(original []byte) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(original, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("expected a YAML configuration mapping")
	}
	return &document, nil
}

func encodeYAMLSettings(document *yaml.Node) ([]byte, error) {
	var updated bytes.Buffer
	encoder := yaml.NewEncoder(&updated)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return updated.Bytes(), nil
}

func yamlSettingsSection(parent *yaml.Node, key string, current any) (*yaml.Node, error) {
	section := yamlField(parent, key)
	switch {
	case section.Kind == 0 && yamlEntry(parent, "<<") != nil:
		// An inherited section needs a direct override that retains its settings.
		if err := section.Encode(current); err != nil {
			return nil, err
		}
	case section.Kind == 0 || section.Tag == "!!null":
		section.Kind, section.Tag, section.Value, section.Style = yaml.MappingNode, "!!map", "", 0
	case section.Kind == yaml.AliasNode:
		alias := *section
		alias.HeadComment, alias.LineComment, alias.FootComment = "", "", ""
		section.Kind, section.Tag, section.Value, section.Alias = yaml.MappingNode, "!!map", "", nil
		section.Content = []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}, &alias}
	}
	if section.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a %s configuration mapping", key)
	}
	return section, nil
}

func yamlEntry(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func yamlField(mapping *yaml.Node, key string) *yaml.Node {
	if value := yamlEntry(mapping, key); value != nil {
		return value
	}
	value := new(yaml.Node)
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value
}
