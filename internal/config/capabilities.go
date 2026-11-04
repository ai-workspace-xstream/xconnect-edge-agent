package config

import (
	"bytes"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
)

// VirtualNodes declares the host envelope. Loading node children and enabling
// data planes remains gated on the identity and metering contract prerequisites.
type VirtualNodes struct {
	Enabled                 bool            `yaml:"enabled"`
	HostID                  string          `yaml:"hostId"`
	ConfigRoot              string          `yaml:"configRoot"`
	MaxConcurrentReconciles int             `yaml:"maxConcurrentReconciles"`
	Nodes                   []NodeReference `yaml:"nodes"`
}
type NodeReference struct {
	ConfigFile  string `yaml:"configFile"`
	RuntimeFile string `yaml:"runtimeFile"`
}

var ErrVirtualNodesNotReady = errors.New("virtual node runtime unavailable: node-bound Accounts identity and durable atomic metering prerequisites must be implemented and validated; no nodes loaded")

// Capabilities is suitable for installer preflight. Do not infer runtime
// support from configVersion alone: old binaries ignore unknown YAML fields.
func Capabilities() string {
	return `{"configVersions":[1,2],"strictV2":true,"checkConfig":true,"virtualNodeRuntime":false}`
}

func checkConfigEnvelope(payload []byte) (bool, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&doc); err != nil {
		return false, errors.New("invalid YAML configuration (values omitted)")
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false, errors.New("configuration must be a YAML mapping")
	}
	root := doc.Content[0]
	version := 0
	versioned := false
	virtual := false
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "configVersion":
			versioned = true
			if value.Tag != "!!int" || value.Decode(&version) != nil || version != 2 {
				return false, errors.New("configVersion must be integer 2 when specified")
			}
		case "virtualNodes":
			virtual = true
		}
	}
	if virtual && !versioned {
		return false, errors.New("virtualNodes requires configVersion: 2 and a capability-aware binary")
	}
	if !versioned {
		return false, nil
	}
	if err := checkV2Node(root); err != nil {
		return true, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return true, errors.New("v2 config must contain exactly one YAML document")
	}
	return true, nil
}
func checkV2Node(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return errors.New("v2 config forbids aliases and merge keys")
	}
	switch n.Tag {
	case "!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null", "!!float", "":
	default:
		return errors.New("v2 config contains an unsupported YAML tag")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Tag != "!!str" || seen[key.Value] {
				return fmt.Errorf("v2 config contains a duplicate or non-string key at line %d", key.Line)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkV2Node(child); err != nil {
			return err
		}
	}
	return nil
}
