package xrayconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestGeneratorRenderUsesReadableJSON(t *testing.T) {
	generator := Generator{Definition: DefaultDefinition()}

	buf, err := generator.Render(nil)
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if !strings.Contains(string(buf), "\n  \"log\"") {
		t.Fatalf("expected indented JSON output, got %q", string(buf[:min(len(buf), 80)]))
	}
	if !strings.HasSuffix(string(buf), "\n") {
		t.Fatal("expected rendered config to end with a newline")
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
	if seen["HandlerService"] || !seen["StatsService"] {
		t.Fatalf("expected StatsService without HandlerService, got %#v", services)
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

func TestManagedTemplatesEnableUserTrafficCounters(t *testing.T) {
	for _, path := range []string{"", "../../config/xray.xhttp.template.json", "../../config/xray.tcp.template.json"} {
		t.Run(path, func(t *testing.T) {
			var definition Definition = DefaultDefinition()
			if path != "" {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				definition = JSONDefinition{Raw: raw}
			}
			raw, err := (Generator{Definition: definition, Domain: "test.example"}).Render([]Client{{ID: "550e8400-e29b-41d4-a716-446655440000", Email: "account-key"}})
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			level := cfg["policy"].(map[string]any)["levels"].(map[string]any)["0"].(map[string]any)
			for _, key := range []string{"statsUserUplink", "statsUserDownlink"} {
				if level[key] != true {
					t.Fatalf("%s must be enabled", key)
				}
			}
		})
	}
}

func decodeRenderedConfig(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	return cfg
}

func outboundMapByTag(t *testing.T, cfg map[string]any) map[string]map[string]any {
	t.Helper()
	rawList, ok := cfg["outbounds"].([]any)
	if !ok {
		t.Fatalf("expected outbounds slice, got %#v", cfg["outbounds"])
	}
	result := make(map[string]map[string]any, len(rawList))
	for _, item := range rawList {
		ob, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := ob["tag"].(string)
		result[tag] = ob
	}
	return result
}

func routingRulesList(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	routing, ok := cfg["routing"].(map[string]any)
	if !ok {
		t.Fatalf("expected routing map, got %#v", cfg["routing"])
	}
	if strategy, _ := routing["domainStrategy"].(string); strategy != "IPIfNonMatch" {
		t.Fatalf("domainStrategy = %q, want IPIfNonMatch", strategy)
	}
	rawRules, ok := routing["rules"].([]any)
	if !ok {
		t.Fatalf("expected routing rules slice, got %#v", routing["rules"])
	}
	rules := make([]map[string]any, 0, len(rawRules))
	for _, r := range rawRules {
		if m, ok := r.(map[string]any); ok {
			rules = append(rules, m)
		}
	}
	return rules
}

func findRuleByOutboundTag(rules []map[string]any, tag string) []map[string]any {
	var matched []map[string]any
	for _, rule := range rules {
		if got, _ := rule["outboundTag"].(string); got == tag {
			matched = append(matched, rule)
		}
	}
	return matched
}

func assertSniffingAndStatsPolicy(t *testing.T, cfg map[string]any, inboundIdx int) {
	t.Helper()
	inbounds, ok := cfg["inbounds"].([]any)
	if !ok || len(inbounds) <= inboundIdx {
		t.Fatalf("expected at least %d inbounds, got %#v", inboundIdx+1, cfg["inbounds"])
	}
	inbound, ok := inbounds[inboundIdx].(map[string]any)
	if !ok {
		t.Fatalf("expected inbound map at %d, got %#v", inboundIdx, inbounds[inboundIdx])
	}
	sniffing, ok := inbound["sniffing"].(map[string]any)
	if !ok {
		t.Fatalf("expected sniffing map on inbound %d, got %#v", inboundIdx, inbound["sniffing"])
	}
	if enabled, _ := sniffing["enabled"].(bool); !enabled {
		t.Fatalf("expected sniffing.enabled=true, got %#v", sniffing["enabled"])
	}
	wantDest := []any{"http", "tls", "quic"}
	if !reflect.DeepEqual(sniffing["destOverride"], wantDest) {
		t.Fatalf("sniffing.destOverride = %#v, want %#v", sniffing["destOverride"], wantDest)
	}

	policy, ok := cfg["policy"].(map[string]any)
	if !ok {
		t.Fatalf("expected policy map, got %#v", cfg["policy"])
	}
	system, ok := policy["system"].(map[string]any)
	if !ok {
		t.Fatalf("expected policy.system map, got %#v", policy["system"])
	}
	for _, key := range []string{"statsInboundUplink", "statsInboundDownlink", "statsOutboundUplink", "statsOutboundDownlink"} {
		if v, _ := system[key].(bool); !v {
			t.Fatalf("expected policy.system.%s=true, got %#v", key, system[key])
		}
	}
	levels, ok := policy["levels"].(map[string]any)
	if !ok {
		t.Fatalf("expected policy.levels map, got %#v", policy["levels"])
	}
	level0, ok := levels["0"].(map[string]any)
	if !ok {
		t.Fatalf("expected policy.levels[\"0\"] map, got %#v", levels["0"])
	}
	for _, key := range []string{"statsUserUplink", "statsUserDownlink"} {
		if v, _ := level0[key].(bool); !v {
			t.Fatalf("expected policy.levels[\"0\"].%s=true, got %#v", key, level0[key])
		}
	}
}

func TestApplyRoutingPolicyProEC2PoolAndControllerPrecedence(t *testing.T) {
	baseWithAPI := JSONDefinition{Raw: []byte(`{
		"api": {"tag": "api", "listen": "127.0.0.1:10086", "services": ["HandlerService", "StatsService"]},
		"inbounds": [{"tag": "xhttp-vless", "protocol": "vless", "settings": {"clients": [], "decryption": "none"}}],
		"routing": {
			"domainStrategy": "AsIs",
			"rules": [{"type": "field", "inboundTag": ["api"], "outboundTag": "api"}]
		}
	}`)}

	gen := Generator{
		Definition:   baseWithAPI,
		RoutingPool:  RoutingPoolMedia,
		PricingGroup: "free",
		RoutingPolicy: &RoutingPolicy{
			Revision:         "rp-42",
			PoolTag:          RoutingPoolProEC2,
			PublicResource:   true,
			SubPoolTags:      []string{"pool-pro-ai-dev", "pool-pro-unclassified"},
			MediaOnProAction: "block",
		},
	}

	rendered, err := gen.Render([]Client{{ID: "uuid-1", Email: "u1@example.com"}})
	if err != nil {
		t.Fatalf("render pro config: %v", err)
	}
	cfg := decodeRenderedConfig(t, rendered)
	assertSniffingAndStatsPolicy(t, cfg, 0)

	outbounds := outboundMapByTag(t, cfg)
	for tag, wantProto := range map[string]string{
		"blocked-private":       "blackhole",
		"direct-cn":             "freedom",
		"blocked-media-on-pro":  "blackhole",
		"pool-pro-ai-dev":       "freedom",
		"pool-pro-unclassified": "freedom",
	} {
		ob, exists := outbounds[tag]
		if !exists {
			t.Fatalf("missing outbound %q", tag)
		}
		if proto, _ := ob["protocol"].(string); proto != wantProto {
			t.Fatalf("outbound %q protocol = %q, want %q", tag, proto, wantProto)
		}
	}

	rules := routingRulesList(t, cfg)
	if len(rules) < 7 {
		t.Fatalf("expected at least 7 routing rules, got %d: %#v", len(rules), rules)
	}
	if tag, _ := rules[0]["outboundTag"].(string); tag != "api" {
		t.Fatalf("expected preserved api rule first, got %#v", rules[0])
	}
	if tag, _ := rules[1]["outboundTag"].(string); tag != "blocked-private" {
		t.Fatalf("expected geoip:private rule second, got %#v", rules[1])
	}
	cnRules := findRuleByOutboundTag(rules, "direct-cn")
	if len(cnRules) != 2 {
		t.Fatalf("expected 2 direct-cn rules (geosite:cn and geoip:cn), got %#v", cnRules)
	}
	mediaRules := findRuleByOutboundTag(rules, "blocked-media-on-pro")
	if len(mediaRules) != 1 {
		t.Fatalf("expected 1 blocked-media-on-pro rule, got %#v", mediaRules)
	}
	if gotDomains, _ := mediaRules[0]["domain"].([]any); len(gotDomains) != len(mediaDownloadDomains) {
		t.Fatalf("media domain count = %d, want %d", len(gotDomains), len(mediaDownloadDomains))
	}
	aiRules := findRuleByOutboundTag(rules, "pool-pro-ai-dev")
	if len(aiRules) != 1 {
		t.Fatalf("expected 1 pool-pro-ai-dev rule, got %#v", aiRules)
	}
	if gotDomains, _ := aiRules[0]["domain"].([]any); len(gotDomains) != len(aiDevOfficeDomains) {
		t.Fatalf("ai/dev domain count = %d, want %d", len(gotDomains), len(aiDevOfficeDomains))
	}
	fallbackRules := findRuleByOutboundTag(rules, "pool-pro-unclassified")
	if len(fallbackRules) != 1 {
		t.Fatalf("expected 1 pool-pro-unclassified fallback rule, got %#v", fallbackRules)
	}
}

func TestApplyRoutingPolicyMediaPoolUnrestricted(t *testing.T) {
	gen := Generator{
		Definition:   DefaultDefinition(),
		PricingGroup: "plus",
	}

	rendered, err := gen.Render([]Client{{ID: "uuid-1", Email: "u1@example.com"}})
	if err != nil {
		t.Fatalf("render media pool config: %v", err)
	}
	cfg := decodeRenderedConfig(t, rendered)
	assertSniffingAndStatsPolicy(t, cfg, 0)

	outbounds := outboundMapByTag(t, cfg)
	if _, exists := outbounds["blocked-media-on-pro"]; exists {
		t.Fatal("Media-Pool must not define blocked-media-on-pro blackhole")
	}
	for tag, wantProto := range map[string]string{
		"blocked-private":     "blackhole",
		"direct-cn":           "freedom",
		"pool-media-stream":   "freedom",
		"pool-pro-ai-dev":     "freedom",
		"pool-media-fallback": "freedom",
	} {
		ob, exists := outbounds[tag]
		if !exists {
			t.Fatalf("missing outbound %q", tag)
		}
		if proto, _ := ob["protocol"].(string); proto != wantProto {
			t.Fatalf("outbound %q protocol = %q, want %q", tag, proto, wantProto)
		}
	}

	rules := routingRulesList(t, cfg)
	if len(findRuleByOutboundTag(rules, "blocked-media-on-pro")) != 0 {
		t.Fatal("Media-Pool must not blackhole media traffic")
	}
	if len(findRuleByOutboundTag(rules, "pool-media-stream")) != 1 {
		t.Fatalf("expected pool-media-stream rule in %#v", rules)
	}
	if len(findRuleByOutboundTag(rules, "pool-pro-ai-dev")) != 1 {
		t.Fatalf("expected pool-pro-ai-dev rule in %#v", rules)
	}
	if len(findRuleByOutboundTag(rules, "pool-media-fallback")) != 1 {
		t.Fatalf("expected pool-media-fallback rule in %#v", rules)
	}
}

func TestApplyRoutingPolicyCustomDedicatedUnrestricted(t *testing.T) {
	gen := Generator{
		Definition:  DefaultDefinition(),
		RoutingPool: RoutingPoolCustomDedicated,
	}

	rendered, err := gen.Render([]Client{{ID: "uuid-1", Email: "u1@example.com"}})
	if err != nil {
		t.Fatalf("render custom dedicated config: %v", err)
	}
	cfg := decodeRenderedConfig(t, rendered)
	assertSniffingAndStatsPolicy(t, cfg, 0)

	outbounds := outboundMapByTag(t, cfg)
	for _, ob := range outbounds {
		tag, _ := ob["tag"].(string)
		proto, _ := ob["protocol"].(string)
		if proto == "blackhole" && tag != "blocked-private" {
			t.Fatalf("unexpected blackhole outbound %q on Custom-Dedicated", tag)
		}
	}
	for _, tag := range []string{"direct", "pool-media-stream", "pool-pro-ai-dev", "direct-cn"} {
		ob, exists := outbounds[tag]
		if !exists {
			t.Fatalf("missing outbound %q", tag)
		}
		if proto, _ := ob["protocol"].(string); proto != "freedom" {
			t.Fatalf("outbound %q protocol = %q, want freedom", tag, proto)
		}
	}

	rules := routingRulesList(t, cfg)
	if len(findRuleByOutboundTag(rules, "direct")) != 1 {
		t.Fatalf("expected direct fallback rule in %#v", rules)
	}
	if len(findRuleByOutboundTag(rules, "pool-media-stream")) != 1 {
		t.Fatalf("expected pool-media-stream freedom rule in %#v", rules)
	}
	if len(findRuleByOutboundTag(rules, "pool-pro-ai-dev")) != 1 {
		t.Fatalf("expected pool-pro-ai-dev freedom rule in %#v", rules)
	}
}

func TestApplyRoutingPolicyEnforcesSniffingAndStatsOnExternalTemplates(t *testing.T) {
	minimalExternal := JSONDefinition{Raw: []byte(`{
		"inbounds": [
			{
				"tag": "ext-vless",
				"protocol": "vless",
				"settings": {"clients": [], "decryption": "none"},
				"sniffing": {"enabled": false, "destOverride": ["http"]}
			}
		],
		"outbounds": [{"tag": "direct", "protocol": "freedom"}]
	}`)}

	rendered, err := (Generator{Definition: minimalExternal, RoutingPool: RoutingPoolMedia}).Render([]Client{{
		ID: "uuid-1", Email: "u1@example.com",
	}})
	if err != nil {
		t.Fatalf("render minimal external template: %v", err)
	}
	assertSniffingAndStatsPolicy(t, decodeRenderedConfig(t, rendered), 0)

	for _, rel := range []string{"../../config/xray.xhttp.template.json", "../../config/xray.tcp.template.json"} {
		payload, err := os.ReadFile(filepath.Clean(rel))
		if err != nil {
			t.Fatalf("read template %s: %v", rel, err)
		}
		out, err := (Generator{
			Definition:  JSONDefinition{Raw: payload},
			RoutingPool: RoutingPoolProEC2,
		}).Render([]Client{{ID: "uuid-1", Email: "u1@example.com"}})
		if err != nil {
			t.Fatalf("render %s: %v", rel, err)
		}
		cfg := decodeRenderedConfig(t, out)
		assertSniffingAndStatsPolicy(t, cfg, 0)
		if strings.Contains(rel, "tcp") {
			level0 := cfg["policy"].(map[string]any)["levels"].(map[string]any)["0"].(map[string]any)
			if level0["handshake"] != float64(2) || level0["connIdle"] != float64(120) {
				t.Fatalf("expected tcp template level 0 timeouts to be preserved, got %#v", level0)
			}
		}
	}
}
