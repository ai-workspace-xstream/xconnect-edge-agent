package xrayconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratorRenderRequiresClientEmail(t *testing.T) {
	generator := Generator{
		Definition: DefaultDefinition(),
		Domain:     "node-a.svc.plus",
	}

	_, err := generator.Render([]Client{{
		ID:   "550e8400-e29b-41d4-a716-446655440000",
		Flow: DefaultFlow,
	}})
	if err == nil {
		t.Fatal("expected render to fail when client email is missing")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Fatalf("expected email validation error, got %v", err)
	}
}

func TestGeneratorRenderUsesEmailAsStatsKey(t *testing.T) {
	generator := Generator{
		Definition: DefaultDefinition(),
		Domain:     "node-a.svc.plus",
	}

	buf, err := generator.Render([]Client{{
		ID:    "550e8400-e29b-41d4-a716-446655440000",
		Email: "2cc7f0b2-69f5-4b02-beb5-df4dd62be7b1",
		Flow:  DefaultFlow,
	}})
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	rendered := string(buf)
	if !strings.Contains(rendered, `"email": "2cc7f0b2-69f5-4b02-beb5-df4dd62be7b1"`) {
		t.Fatalf("expected rendered config to include stats email key, got %s", rendered)
	}
}

func TestDefaultDefinitionEnablesStatsService(t *testing.T) {
	rendered, err := (Generator{}).Render(nil)
	if err != nil {
		t.Fatalf("render default definition: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(rendered, &config); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	api, ok := config["api"].(map[string]any)
	if !ok {
		t.Fatalf("expected api object, got %#v", config["api"])
	}
	services, ok := api["services"].([]any)
	if !ok {
		t.Fatalf("expected api services, got %#v", api["services"])
	}
	seen := map[string]bool{}
	for _, service := range services {
		if name, ok := service.(string); ok {
			seen[name] = true
		}
	}
	if !seen["HandlerService"] || !seen["StatsService"] {
		t.Fatalf("expected HandlerService and StatsService, got %#v", services)
	}
	if stats, ok := config["stats"].(map[string]any); !ok || stats == nil {
		t.Fatalf("expected top-level stats manager configuration, got %#v", config["stats"])
	}
}

func TestGeneratorRenderUpdatesClientsInAnyInbound(t *testing.T) {
	definition := JSONDefinition{Raw: []byte(`{
		"inbounds": [
			{
				"listen": "127.0.0.1",
				"port": 8080,
				"protocol": "dokodemo-door",
				"settings": {
					"address": "127.0.0.1"
				},
				"tag": "api"
			},
			{
				"listen": "/dev/shm/xray.sock,0666",
				"protocol": "vless",
				"settings": {
					"clients": [
						{
							"id": "{{ UUID }}"
						}
					],
					"decryption": "none"
				},
				"streamSettings": {
					"network": "xhttp"
				}
			}
		]
	}`)}

	generator := Generator{
		Definition: definition,
		Domain:     "node-a.svc.plus",
	}

	buf, err := generator.Render([]Client{
		{ID: "client-1", Email: "a@example.com"},
		{ID: "client-2", Email: "b@example.com"},
	})
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	var rendered struct {
		Inbounds []struct {
			Settings map[string]any `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(buf, &rendered); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	if len(rendered.Inbounds) != 2 {
		t.Fatalf("expected 2 inbounds, got %d", len(rendered.Inbounds))
	}
	clients, ok := rendered.Inbounds[1].Settings["clients"].([]any)
	if !ok {
		t.Fatalf("expected inbound 1 clients array, got %#v", rendered.Inbounds[1].Settings["clients"])
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients in second inbound, got %d", len(clients))
	}
}
