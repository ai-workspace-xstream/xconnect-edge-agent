package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Managed snapshots are intentionally narrower than the legacy YAML. In
// particular, controller data must never choose executable commands, local
// paths, Xray targets, credentials, or process mode.
var managedFields = map[string]any{
	"log": map[string]any{"level": nil},
	"agent": map[string]any{
		"resourceClass": nil, "teamId": nil, "allowedEmails": nil,
		"region": nil, "pool": nil, "entryPoint": nil, "openToUsers": nil,
		"provider": nil, "product": nil, "lineCode": nil, "pricingGroup": nil,
		"statsEnabled": nil, "domain": nil, "httpTimeout": nil,
		"statusInterval": nil, "syncInterval": nil,
	},
	"xray": map[string]any{"sync": map[string]any{
		"enabled": nil, "interval": nil, "tuning": map[string]any{
			"logLevel": nil, "bufferSizeKB": nil, "connIdleSeconds": nil,
			"handshakeSeconds": nil, "uplinkOnlySeconds": nil,
			"downlinkOnlySeconds": nil, "xhttpScMaxEachPostBytes": nil,
			"xhttpScMaxBufferedPosts": nil,
		},
	}},
	"billing": map[string]any{
		"enabled": nil, "baseURL": nil, "httpTimeout": nil,
		"collectInterval": nil, "reconcileInterval": nil,
	},
}

// loadManagedOverlay loads one locally provisioned snapshot and rejects any
// field outside the explicit remote-management allowlist.
func loadManagedOverlay(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open managed config: %w", err)
	}
	defer f.Close()

	var document yaml.Node
	decoder := yaml.NewDecoder(f)
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode managed config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("managed config must contain exactly one document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("managed config must be a mapping")
	}
	if err := validateManagedNode(document.Content[0], managedFields, "managed config"); err != nil {
		return err
	}
	if err := document.Content[0].Decode(cfg); err != nil {
		return fmt.Errorf("apply managed config: %w", err)
	}
	return nil
}

func validateManagedNode(node *yaml.Node, allowlist map[string]any, path string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be a mapping", path)
	}
	seen := make(map[string]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || key.Value == "<<" {
			return fmt.Errorf("%s keys must be plain strings without YAML merge keys", path)
		}
		if seen[key.Value] {
			return fmt.Errorf("%s contains duplicate key %q", path, key.Value)
		}
		seen[key.Value] = true
		spec, ok := allowlist[key.Value]
		if !ok {
			return fmt.Errorf("%s contains unsupported field %q", path, key.Value)
		}
		if value.Kind == yaml.AliasNode || value.Anchor != "" {
			return fmt.Errorf("%s.%s cannot use YAML aliases or anchors", path, key.Value)
		}
		if nested, ok := spec.(map[string]any); ok {
			if err := validateManagedNode(value, nested, path+"."+key.Value); err != nil {
				return err
			}
		}
	}
	return nil
}
