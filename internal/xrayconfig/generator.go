package xrayconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const (
	// DefaultFlow is applied to VLESS clients when no explicit flow is
	// provided.
	DefaultFlow = "xtls-rprx-vision"

	// Canonical three-tier routing pool tags assigned by the controller or
	// mapped from agent.resourceClass.
	RoutingPoolMedia           = "Media-Pool"
	RoutingPoolProEC2          = "Pro-EC2-Pool"
	RoutingPoolCustomDedicated = "Custom-Dedicated"
)

// RoutingPolicy describes the controller-assigned routing pool and outbound
// enforcement rules for an edge node.
type RoutingPolicy struct {
	Revision         string   `json:"revision,omitempty"`
	PoolTag          string   `json:"poolTag,omitempty"`
	PublicResource   bool     `json:"publicResource,omitempty"`
	SubPoolTags      []string `json:"subPoolTags,omitempty"`
	MediaOnProAction string   `json:"mediaOnProAction,omitempty"`
}

// Client represents an entry under inbounds[0].settings.clients[] in the Xray
// config.
type Client struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Flow         string     `json:"flow,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	RoutingPool  string     `json:"routingPool,omitempty"`
	PricingGroup string     `json:"pricingGroup,omitempty"`
}

// Generator updates the Xray configuration file based on a template and a set of
// active clients.
type Generator struct {
	// Definition provides the base Xray configuration that will be mutated to
	// include the provided clients. When nil, DefaultDefinition is used.
	Definition Definition

	// OutputPath is the destination path for the generated configuration
	// (typically /usr/local/etc/xray/config.json).
	OutputPath string

	// FileMode controls the permissions for the generated file. When zero it
	// defaults to 0644.
	FileMode fs.FileMode

	// Domain is the hostname used to interpolate templates (e.g. for cert paths).
	Domain string

	// Tuning overrides allowlisted runtime knobs after the template is
	// rendered. Nil keeps the template unchanged.
	Tuning *ServerTuning

	// RoutingPool holds the node-level routing pool tag or fallback pool name.
	RoutingPool string

	// PricingGroup holds the node-level pricing tier used as fallback when no
	// explicit routing pool is configured.
	PricingGroup string

	// RoutingPolicy holds the controller-provided routing policy, taking
	// precedence over static node-level pool configuration.
	RoutingPolicy *RoutingPolicy
}

// Generate writes a new Xray configuration with the provided clients. The base
// template is loaded on every invocation to ensure updates remain additive and
// idempotent even when multiple callers trigger regeneration.
func (g Generator) Generate(clients []Client) error {
	if strings.TrimSpace(g.OutputPath) == "" {
		return errors.New("output path is required")
	}

	buf, err := g.Render(clients)
	if err != nil {
		return err
	}

	if err := atomicWriteFile(g.OutputPath, buf, g.fileMode()); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func (g Generator) fileMode() fs.FileMode {
	if g.FileMode == 0 {
		return 0o644
	}
	return g.FileMode
}

// Render returns the rendered configuration JSON without writing it to disk.
// The returned buffer always ends with a newline.
func (g Generator) Render(clients []Client) ([]byte, error) {
	definition := g.Definition
	if definition == nil {
		definition = DefaultDefinition()
	}

	// 1. Render text/template first
	baseMap, err := definition.Base()
	if err != nil {
		return nil, fmt.Errorf("load template base: %w", err)
	}

	// Re-marshal to bytes to apply template interpolation
	rawBase, err := json.Marshal(baseMap)
	if err != nil {
		return nil, fmt.Errorf("marshal base template: %w", err)
	}

	// Prepare data for template
	data := struct {
		Domain string
		UUID   string
	}{
		Domain: g.Domain,
	}
	if len(clients) > 0 {
		data.UUID = clients[0].ID
	}

	// Execute Template
	tmpl := template.New("xray").Funcs(template.FuncMap{
		"UUID": func() string { return data.UUID },
	})
	tmpl, err = tmpl.Parse(string(rawBase))
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	// Unmarshal back to map to perform structural updates (clients list)
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(buf.String()), &root); err != nil {
		return nil, fmt.Errorf("unmarshal rendered template: %w", err)
	}

	// 2. Structural updates (Inbounds & Outbounds clients/users)
	if err := updateClients(root, clients); err != nil {
		return nil, err
	}
	g.Tuning.apply(root)

	// 3. Enforce sniffing, stats policy, and three-tier pool routing
	applyRoutingPolicy(root, g, clients)

	finalBuf, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	finalBuf = append(finalBuf, '\n')
	return finalBuf, nil
}

func updateClients(root map[string]interface{}, clients []Client) error {
	for _, c := range clients {
		if strings.TrimSpace(c.ID) == "" {
			return errors.New("client id is required")
		}
		if strings.TrimSpace(c.Email) == "" {
			return fmt.Errorf("client %s email is required for xray stats", c.ID)
		}
	}

	// 1. Update Inbounds (Server side)
	if inboundsValue, ok := root["inbounds"]; ok {
		if inboundsSlice, ok := inboundsValue.([]interface{}); ok && len(inboundsSlice) > 0 {
			for i, inboundValue := range inboundsSlice {
				inbound, ok := inboundValue.(map[string]interface{})
				if !ok {
					continue
				}
				settings, ok := inbound["settings"].(map[string]interface{})
				if !ok {
					continue
				}
				if _, ok := settings["clients"]; !ok {
					continue
				}

				newClients := make([]interface{}, 0, len(clients))
				for _, c := range clients {
					entry := map[string]interface{}{"id": c.ID}
					if c.Email != "" {
						entry["email"] = c.Email
					}
					// Only xhttp inbounds omit flow; TCP/VLESS inbounds keep it.
					includeFlow := true
					if ss, ok := inbound["streamSettings"].(map[string]interface{}); ok {
						if net, _ := ss["network"].(string); net == "xhttp" {
							includeFlow = false
						}
					}
					if includeFlow {
						flow := c.Flow
						if flow == "" {
							flow = DefaultFlow
						}
						entry["flow"] = flow
					}
					newClients = append(newClients, entry)
				}
				settings["clients"] = newClients
				inbound["settings"] = settings
				inboundsSlice[i] = inbound
			}
			root["inbounds"] = inboundsSlice
		}
	}

	// 2. Update Outbounds (Client side) - Search for VLESS protocol
	if outboundsValue, ok := root["outbounds"]; ok {
		if outboundsSlice, ok := outboundsValue.([]interface{}); ok {
			updated := false
			for i, out := range outboundsSlice {
				outbound, ok := out.(map[string]interface{})
				if !ok {
					continue
				}
				proto, _ := outbound["protocol"].(string)
				if proto == "vless" {
					if settings, ok := outbound["settings"].(map[string]interface{}); ok {
						if vnext, ok := settings["vnext"].([]interface{}); ok {
							for j, vn := range vnext {
								vnextMap, ok := vn.(map[string]interface{})
								if !ok {
									continue
								}
								// Update users list in vnext
								// We replace the users list with our clients
								newUsers := make([]interface{}, 0, len(clients))
								for _, c := range clients {
									u := map[string]interface{}{
										"id":         c.ID,
										"encryption": "none",
									}
									// Add flow if not xhttp? Or always?
									// Check outbound streamSettings
									includeFlow := true
									if ss, ok := outbound["streamSettings"].(map[string]interface{}); ok {
										if net, _ := ss["network"].(string); net == "xhttp" {
											includeFlow = false
										}
									}
									if includeFlow {
										flow := c.Flow
										if flow == "" {
											flow = DefaultFlow
										}
										u["flow"] = flow
									}
									newUsers = append(newUsers, u)
								}
								vnextMap["users"] = newUsers
								vnext[j] = vnextMap
							}
							settings["vnext"] = vnext
							outbound["settings"] = settings
							outboundsSlice[i] = outbound
							updated = true
						}
					}
				}
			}
			if updated {
				root["outbounds"] = outboundsSlice
			}
		}
	}

	return nil
}

var (
	mediaDownloadDomains = []string{
		"geosite:youtube",
		"geosite:netflix",
		"geosite:disney",
		"geosite:tiktok",
		"geosite:twitch",
		"geosite:spotify",
		"geosite:bilibili",
		"domain:speedtest.net",
		"domain:fast.com",
		"domain:steamcontent.com",
		"domain:icloud-content.com",
		"domain:mega.nz",
	}

	aiDevOfficeDomains = []string{
		"geosite:openai",
		"geosite:anthropic",
		"domain:claude.ai",
		"domain:cursor.sh",
		"domain:cursor.com",
		"geosite:github",
		"geosite:google",
		"geosite:cloudflare",
	}
)

// MapResourceClassToRoutingPool maps a resource class, pricing tier, or pool
// tag into one of the canonical three-tier routing pool names. Unknown values
// return an empty string.
func MapResourceClassToRoutingPool(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "media-pool", "free", "shared", "plus":
		return RoutingPoolMedia
	case "pro-ec2-pool", "pro":
		return RoutingPoolProEC2
	case "custom-dedicated", "custom", "team":
		return RoutingPoolCustomDedicated
	default:
		return ""
	}
}

func applyRoutingPolicy(root map[string]interface{}, g Generator, clients []Client) {
	enforceInboundSniffing(root)
	enforceStatsPolicy(root)

	pool := effectiveRoutingPool(g, clients)
	tierOutbounds, tierRules := buildTierOutboundsAndRules(pool, g.RoutingPolicy)

	preservedOutbounds, hasNonProxyOutbound := extractPreservedOutbounds(root)
	outbounds := make([]interface{}, 0, len(preservedOutbounds)+len(tierOutbounds))
	outbounds = append(outbounds, preservedOutbounds...)
	outbounds = append(outbounds, tierOutbounds...)
	root["outbounds"] = outbounds

	routing, _ := root["routing"].(map[string]interface{})
	if routing == nil {
		routing = map[string]interface{}{}
	}
	apiRules := extractAPIRules(routing["rules"])

	rules := make([]interface{}, 0, len(apiRules)+len(tierRules))
	rules = append(rules, apiRules...)
	if !hasNonProxyOutbound {
		rules = append(rules, tierRules...)
	}

	routing["domainStrategy"] = "IPIfNonMatch"
	routing["rules"] = rules
	root["routing"] = routing
}

func enforceInboundSniffing(root map[string]interface{}) {
	inbounds, ok := root["inbounds"].([]interface{})
	if !ok {
		return
	}
	for i, raw := range inbounds {
		inbound, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := inbound["tag"].(string)
		proto, _ := inbound["protocol"].(string)
		if tag == "api" || proto == "dokodemo-door" {
			continue
		}
		sniffing, _ := inbound["sniffing"].(map[string]interface{})
		if sniffing == nil {
			sniffing = map[string]interface{}{}
		}
		sniffing["enabled"] = true
		sniffing["destOverride"] = []interface{}{"http", "tls", "quic"}
		inbound["sniffing"] = sniffing
		inbounds[i] = inbound
	}
	root["inbounds"] = inbounds
}

func enforceStatsPolicy(root map[string]interface{}) {
	if _, ok := root["stats"].(map[string]interface{}); !ok {
		root["stats"] = map[string]interface{}{}
	}

	policy, _ := root["policy"].(map[string]interface{})
	if policy == nil {
		policy = map[string]interface{}{}
	}

	system, _ := policy["system"].(map[string]interface{})
	if system == nil {
		system = map[string]interface{}{}
	}
	system["statsInboundUplink"] = true
	system["statsInboundDownlink"] = true
	system["statsOutboundUplink"] = true
	system["statsOutboundDownlink"] = true
	policy["system"] = system

	levels, _ := policy["levels"].(map[string]interface{})
	if levels == nil {
		levels = map[string]interface{}{}
	}
	level0, _ := levels["0"].(map[string]interface{})
	if level0 == nil {
		level0 = map[string]interface{}{}
	}
	level0["statsUserUplink"] = true
	level0["statsUserDownlink"] = true
	levels["0"] = level0
	policy["levels"] = levels

	root["policy"] = policy
}

func effectiveRoutingPool(g Generator, clients []Client) string {
	if g.RoutingPolicy != nil {
		if pool := MapResourceClassToRoutingPool(g.RoutingPolicy.PoolTag); pool != "" {
			return pool
		}
	}
	if pool := MapResourceClassToRoutingPool(g.RoutingPool); pool != "" {
		return pool
	}
	if pool := MapResourceClassToRoutingPool(g.PricingGroup); pool != "" {
		return pool
	}
	for _, client := range clients {
		if pool := MapResourceClassToRoutingPool(client.RoutingPool); pool != "" {
			return pool
		}
		if pool := MapResourceClassToRoutingPool(client.PricingGroup); pool != "" {
			return pool
		}
	}
	return ""
}

func buildTierOutboundsAndRules(pool string, policy *RoutingPolicy) ([]interface{}, []interface{}) {
	rules := []interface{}{
		ipRule([]string{"geoip:private"}, "blocked-private"),
		domainRule([]string{"geosite:cn"}, "direct-cn"),
		ipRule([]string{"geoip:cn"}, "direct-cn"),
	}

	switch pool {
	case RoutingPoolProEC2:
		mediaOutbound := "blocked-media-on-pro"
		if policy != nil && strings.EqualFold(strings.TrimSpace(policy.MediaOnProAction), "allow") {
			mediaOutbound = "pool-pro-unclassified"
		}
		outbounds := []interface{}{
			freedomOutbound("pool-pro-unclassified"),
			freedomOutbound("pool-pro-ai-dev"),
			freedomOutbound("direct-cn"),
			blackholeOutbound("blocked-private"),
			blackholeOutbound("blocked-media-on-pro"),
		}
		rules = append(rules,
			domainRule(mediaDownloadDomains, mediaOutbound),
			domainRule(aiDevOfficeDomains, "pool-pro-ai-dev"),
			fallbackRule("pool-pro-unclassified"),
		)
		return outbounds, rules

	case RoutingPoolMedia:
		outbounds := []interface{}{
			freedomOutbound("pool-media-fallback"),
			freedomOutbound("pool-media-stream"),
			freedomOutbound("pool-pro-ai-dev"),
			freedomOutbound("direct-cn"),
			blackholeOutbound("blocked-private"),
		}
		rules = append(rules,
			domainRule(mediaDownloadDomains, "pool-media-stream"),
			domainRule(aiDevOfficeDomains, "pool-pro-ai-dev"),
			fallbackRule("pool-media-fallback"),
		)
		return outbounds, rules

	case RoutingPoolCustomDedicated:
		outbounds := []interface{}{
			freedomOutbound("direct"),
			freedomOutbound("pool-media-stream"),
			freedomOutbound("pool-pro-ai-dev"),
			freedomOutbound("direct-cn"),
			blackholeOutbound("blocked-private"),
		}
		rules = append(rules,
			domainRule(mediaDownloadDomains, "pool-media-stream"),
			domainRule(aiDevOfficeDomains, "pool-pro-ai-dev"),
			fallbackRule("direct"),
		)
		return outbounds, rules

	default:
		outbounds := []interface{}{
			freedomOutbound("direct"),
			freedomOutbound("direct-cn"),
			blackholeOutbound("blocked-private"),
		}
		rules = append(rules, fallbackRule("direct"))
		return outbounds, rules
	}
}

func extractPreservedOutbounds(root map[string]interface{}) ([]interface{}, bool) {
	raw, ok := root["outbounds"].([]interface{})
	if !ok {
		return nil, false
	}
	preserved := make([]interface{}, 0)
	hasVlessOutbound := false
	for _, entry := range raw {
		ob, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		proto, _ := ob["protocol"].(string)
		if proto == "freedom" || proto == "blackhole" {
			continue
		}
		if proto == "vless" {
			hasVlessOutbound = true
		}
		preserved = append(preserved, ob)
	}
	return preserved, hasVlessOutbound
}

func extractAPIRules(rawRules interface{}) []interface{} {
	rulesSlice, ok := rawRules.([]interface{})
	if !ok {
		return nil
	}
	apiRules := make([]interface{}, 0)
	for _, raw := range rulesSlice {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if outboundTag, _ := rule["outboundTag"].(string); outboundTag == "api" {
			apiRules = append(apiRules, rule)
			continue
		}
		if inboundTags, ok := rule["inboundTag"].([]interface{}); ok {
			for _, tag := range inboundTags {
				if s, _ := tag.(string); s == "api" {
					apiRules = append(apiRules, rule)
					break
				}
			}
		}
	}
	return apiRules
}

func freedomOutbound(tag string) map[string]interface{} {
	return map[string]interface{}{
		"tag":      tag,
		"protocol": "freedom",
		"settings": map[string]interface{}{},
	}
}

func blackholeOutbound(tag string) map[string]interface{} {
	return map[string]interface{}{
		"tag":      tag,
		"protocol": "blackhole",
		"settings": map[string]interface{}{},
	}
}

func domainRule(domains []string, outboundTag string) map[string]interface{} {
	items := make([]interface{}, len(domains))
	for i, d := range domains {
		items[i] = d
	}
	return map[string]interface{}{
		"type":        "field",
		"domain":      items,
		"outboundTag": outboundTag,
	}
}

func ipRule(ips []string, outboundTag string) map[string]interface{} {
	items := make([]interface{}, len(ips))
	for i, ip := range ips {
		items[i] = ip
	}
	return map[string]interface{}{
		"type":        "field",
		"ip":          items,
		"outboundTag": outboundTag,
	}
}

func fallbackRule(outboundTag string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "field",
		"network":     "tcp,udp",
		"outboundTag": outboundTag,
	}
}

func atomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".xray-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}
