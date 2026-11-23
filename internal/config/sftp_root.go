package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// WithSFTPRoot returns an updated YAML document after validating it at its
// original location. Other settings and the configured SFTP mode are preserved.
func WithSFTPRoot(name string, original []byte, root string) ([]byte, error) {
	if strings.TrimSpace(root) == "" || strings.ContainsAny(root, "\x00\r\n") {
		return nil, errors.New("sftp.root must be a directory path or ~")
	}
	name, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	before, err := decodeSettings(bytes.NewReader(original))
	if err != nil {
		return nil, err
	}
	if before.SFTP.Mode == "disabled" {
		return nil, errors.New("sftp.mode is disabled")
	}

	document, err := yamlSettings(original)
	if err != nil {
		return nil, err
	}
	sftp, err := yamlSettingsSection(document.Content[0], "sftp", before.SFTP)
	if err != nil {
		return nil, err
	}
	entry := yamlField(sftp, "root")
	entry.Kind, entry.Tag, entry.Value, entry.Style = yaml.ScalarNode, "!!str", root, yaml.SingleQuotedStyle
	entry.Alias, entry.Content = nil, nil

	updated, err := encodeYAMLSettings(document)
	if err != nil {
		return nil, err
	}
	after, err := decodeSettings(bytes.NewReader(updated))
	if err != nil {
		return nil, err
	}
	before.SFTP.Root = root
	if after != before {
		return nil, errors.New("sftp.root shares a YAML anchor with another setting; edit the YAML directly")
	}
	resolved, err := after.resolvePaths(name)
	if err != nil {
		return nil, err
	}
	directory, err := os.OpenRoot(resolved.SFTP.Root)
	if err != nil {
		return nil, fmt.Errorf("open sftp.root: %w", err)
	}
	if err := directory.Close(); err != nil {
		return nil, err
	}
	return updated, nil
}
