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
)

const (
	// DefaultFlow is applied to VLESS clients when no explicit flow is
	// provided.
	DefaultFlow = "xtls-rprx-vision"
)

// Client represents an entry under inbounds[0].settings.clients[] in the Xray
// config.
type Client struct {
	ID    string
	Email string
	Flow  string
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
