package xrayconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped templates are what nodes actually run: config/ is copied by the
// install scripts and the Docker images, the Ansible role renders its own
// copies, and the built-in template is used when no templatePath is set.
const repoRoot = "../.."

func loadTemplate(t *testing.T, path string) Definition {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.HasSuffix(path, ".j2") {
		// Ansible only wraps the agent's own template markers in raw blocks.
		text := strings.NewReplacer("{% raw %}", "", "{% endraw %}", "").Replace(string(raw))
		if rest := strings.ReplaceAll(text, "{{ UUID }}", ""); strings.Contains(rest, "{{") || strings.Contains(rest, "{%") {
			t.Fatalf("%s uses Ansible variables; parity cannot be checked", path)
		}
		raw = []byte(text)
	}
	return JSONDefinition{Raw: raw}
}

func renderTemplate(t *testing.T, definition Definition) map[string]interface{} {
	t.Helper()
	generator := Generator{Definition: definition, Domain: "node.example.com"}
	buf, err := generator.Render([]Client{alice, bob})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(buf, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestShippedTemplatesShareOneDefaultPolicy(t *testing.T) {
	families := map[string][]string{
		"xhttp": {
			"config/xray.xhttp.template.json",
			"deploy/ansible/roles/xconnect_edge_agent/templates/xray.xhttp.template.json.j2",
			"",
		},
		"tcp": {
			"config/xray.tcp.template.json",
			"deploy/ansible/roles/xconnect_edge_agent/templates/xray.tcp.template.json.j2",
		},
	}
	for family, paths := range families {
		var reference []byte
		for _, path := range paths {
			definition := DefaultDefinition()
			name := "built-in template"
			if path != "" {
				definition = loadTemplate(t, path)
				name = path
			}
			root := renderTemplate(t, definition)
			if level := root["log"].(map[string]interface{})["loglevel"]; level != "warning" {
				t.Errorf("%s logs at %v; production templates log at warning", name, level)
			}
			// bufferSize is connection buffer capacity, decided per node
			// through tuning after a load test, never by a template.
			if policy, ok := root["policy"].(map[string]interface{}); ok {
				levels, _ := policy["levels"].(map[string]interface{})
				level, _ := levels["0"].(map[string]interface{})
				if _, set := level["bufferSize"]; set {
					t.Errorf("%s sets policy bufferSize", name)
				}
			}
			encoded, _ := json.Marshal(root)
			if reference == nil {
				reference = encoded
				continue
			}
			if string(encoded) != string(reference) {
				t.Errorf("%s template %s renders differently from %s:\n%s\n%s", family, name, paths[0], encoded, reference)
			}
		}
	}
}

func TestAnsibleAgentConfigValidatesCandidates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "deploy/ansible/roles/xconnect_edge_agent/templates/agent-config.yaml.j2"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	want := `validateCommand: ["{{ xray_executable }}", "run", "-test", "-config", "` + ConfigPathPlaceholder + `"]`
	if got := strings.Count(text, want); got != strings.Count(text, "- name: ") || got == 0 {
		t.Fatalf("every Ansible target must validate its candidate, found %d of %d", got, strings.Count(text, "- name: "))
	}
	if !strings.Contains(text, "tuning: {{ xray_sync_tuning | to_json }}") {
		t.Fatal("Ansible cannot set local tuning")
	}
}
