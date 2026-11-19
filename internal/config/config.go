package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

type Config struct {
	Mode    string  `yaml:"mode"`
	Log     Log     `yaml:"log"`
	Agent   Agent   `yaml:"agent"`
	Xray    Xray    `yaml:"xray"`
	Billing Billing `yaml:"billing"`

	// Flat top-level bootstrap fields for minimal 5-field config:
	ID            string `yaml:"id"`
	NodeID        string `yaml:"nodeId"`
	ControllerURL string `yaml:"controllerUrl"`
	APIToken      string `yaml:"apiToken"`
	ResourceClass string `yaml:"resourceClass"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Agent struct {
	CustomAllowFree bool `yaml:"customAllowFree"`
	// ResourceClass selects the user sync scope: shared, free, plus, pro, custom, or team.
	AllowedEmails        []string       `yaml:"allowedEmails"`
	ResourceClass        string         `yaml:"resourceClass"`
	TeamID               string         `yaml:"teamId"`
	OverlayStatusPath    string         `yaml:"overlayStatusPath"`
	GatewayTelemetryPath string         `yaml:"gatewayTelemetryPath"`
	Gateway              ControlGateway `yaml:"gateway"`
	Role                 string         `yaml:"role"`
	ID                   string         `yaml:"id"`
	NodeID               string         `yaml:"nodeId"`
	NetworkID            string         `yaml:"networkId"`
	Region               string         `yaml:"region"`
	Pool                 string         `yaml:"pool"`
	EntryPoint           string         `yaml:"entryPoint"`
	OpenToUsers          *bool          `yaml:"openToUsers"`
	Provider             string         `yaml:"provider"`
	Product              string         `yaml:"product"`
	LineCode             string         `yaml:"lineCode"`
	PricingGroup         string         `yaml:"pricingGroup"`
	StatsEnabled         bool           `yaml:"statsEnabled"`
	ControllerURL        string         `yaml:"controllerUrl"`
	APIToken             string         `yaml:"apiToken"`
	Domain               string         `yaml:"domain"`
	HTTPTimeout          time.Duration  `yaml:"httpTimeout"`
	StatusInterval       time.Duration  `yaml:"statusInterval"`
	SyncInterval         time.Duration  `yaml:"syncInterval"`
	TLS                  TLS            `yaml:"tls"`
}

type ControlGateway struct {
	Enabled      bool     `yaml:"enabled"`
	StateDir     string   `yaml:"stateDir"`
	ApplyCommand []string `yaml:"applyCommand"`
}

const (
	RoleGateway    = "gateway"
	RoleOne        = "one"
	RoleAgentProxy = "agent-proxy"
)

func (a Agent) EffectiveRole() string {
	if a.Role == "" {
		return RoleAgentProxy
	}
	return a.Role
}

// ValidateUserScope validates the explicit resource class independently of Pool.
// Empty scope retains compatibility with existing controller deployments.
func (a Agent) ValidateUserScope() error {
	if len(a.AllowedEmails) > 0 && !agentproto.PublicResourceClass(a.ResourceClass) && a.ResourceClass != "custom" {
		return fmt.Errorf("allowedEmails requires an explicit public class or custom")
	}
	if _, err := agentproto.NormalizeNodeEmails(a.AllowedEmails); err != nil {
		return err
	}
	if a.CustomAllowFree {
		switch a.ResourceClass {
		case "shared", "free", "plus", "pro", "custom", "team":
		default:
			return fmt.Errorf("agent.customAllowFree requires an explicit resourceClass")
		}
	}
	for _, email := range a.AllowedEmails {
		parsed, err := mail.ParseAddress(strings.TrimSpace(email))
		if err != nil || parsed.Address != strings.TrimSpace(email) {
			return fmt.Errorf("agent.allowedEmails contains an invalid email")
		}
	}
	switch a.ResourceClass {
	case "", "shared", "free", "plus", "pro", "custom":
		if a.TeamID != "" {
			return fmt.Errorf("agent.teamId requires resourceClass team")
		}
	case "team":
		if len(a.TeamID) > 128 || strings.ContainsAny(a.TeamID, " \t\r\n") {
			return fmt.Errorf("agent.teamId is an optional suggested team ID and must contain no whitespace")
		}
	default:
		return fmt.Errorf("agent.resourceClass must be shared, free, plus, pro, custom, or team")
	}
	return nil
}

func (a Agent) ValidateRole() error {
	switch a.EffectiveRole() {
	case RoleGateway, RoleOne, RoleAgentProxy:
		return nil
	default:
		return fmt.Errorf("agent.role must be one of %q, %q, or %q", RoleGateway, RoleOne, RoleAgentProxy)
	}
}

type TLS struct {
	InsecureSkipVerify bool `yaml:"insecureSkipVerify"`
}

type Xray struct {
	Sync XraySync `yaml:"sync"`
}

type Billing struct {
	Enabled           bool          `yaml:"enabled"`
	BaseURL           string        `yaml:"baseURL"`
	HTTPTimeout       time.Duration `yaml:"httpTimeout"`
	CollectInterval   time.Duration `yaml:"collectInterval"`
	ReconcileInterval time.Duration `yaml:"reconcileInterval"`
}

type XraySync struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
	// Targets allows defining multiple Xray configuration files to sync.
	Targets []SyncTarget `yaml:"targets"`
	// Tuning adjusts allowlisted Xray runtime knobs on every target. Tuning
	// from the controller overrides it field by field.
	Tuning *xrayconfig.ServerTuning `yaml:"tuning"`

	// Legacy fields for backward compatibility or simple single-target config
	OutputPath      string   `yaml:"outputPath"`
	TemplatePath    string   `yaml:"templatePath"`
	ValidateCommand []string `yaml:"validateCommand"`
	RestartCommand  []string `yaml:"restartCommand"`
}

type SyncTarget struct {
	Name            string   `yaml:"name"`
	OutputPath      string   `yaml:"outputPath"`
	TemplatePath    string   `yaml:"templatePath"`
	ValidateCommand []string `yaml:"validateCommand"`
	RestartCommand  []string `yaml:"restartCommand"`
}

func Load(path string) (*Config, error) {
	return load(path, false)
}

// LoadManaged loads the bootstrap configuration and its provisioned snapshot.
func LoadManaged(path string) (*Config, error) {
	return load(path, true)
}

// LoadAll loads the base configuration and any profile fragments in a sibling
// conf.d directory. When conf.d contains *.yaml or *.yml files, each file is
// decoded on top of a copy of the base configuration and finalized as an
// independent runtime profile.
func LoadAll(path string, managed bool) ([]*Config, error) {
	confDir := filepath.Join(filepath.Dir(path), "conf.d")
	entries, err := os.ReadDir(confDir)
	if err != nil {
		if os.IsNotExist(err) {
			cfg, err := load(path, managed)
			if err != nil {
				return nil, err
			}
			return []*Config{cfg}, nil
		}
		return nil, fmt.Errorf("read conf.d directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yaml" || ext == ".yml" {
			files = append(files, filepath.Join(confDir, entry.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		cfg, err := load(path, managed)
		if err != nil {
			return nil, err
		}
		return []*Config{cfg}, nil
	}

	var baseCfg Config
	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, &baseCfg); err != nil {
			return nil, fmt.Errorf("decode base config %s: %w", path, err)
		}
		if managed {
			overlayPath := filepath.Join(strings.TrimSuffix(path, filepath.Ext(path))+".d", "managed.yaml")
			if err := loadManagedOverlay(overlayPath, &baseCfg); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read base config %s: %w", path, err)
	}

	configs := make([]*Config, 0, len(files))
	seenIDs := make(map[string]struct{}, len(files))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read conf.d file %s: %w", file, err)
		}
		cfg := baseCfg
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode conf.d file %s: %w", file, err)
		}
		finalized, err := finalizeConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("finalize conf.d file %s: %w", file, err)
		}
		if finalized.Mode != "" && finalized.Mode != "agent" {
			return nil, fmt.Errorf("invalid run mode in %s: expected agent, got %s", file, finalized.Mode)
		}
		if id := strings.TrimSpace(finalized.Agent.ID); id != "" {
			seenIDs[id] = struct{}{}
		}
		configs = append(configs, finalized)
	}

	if baseID := strings.TrimSpace(baseCfg.Agent.ID); baseID != "" {
		if _, exists := seenIDs[baseID]; !exists {
			finalizedBase, err := finalizeConfig(baseCfg)
			if err != nil {
				return nil, err
			}
			configs = append([]*Config{finalizedBase}, configs...)
		}
	}
	return configs, nil
}

func load(path string, managed bool) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	cfg, err := decodeConfig(f)
	if err != nil {
		return nil, err
	}
	if managed {
		overlayPath := filepath.Join(strings.TrimSuffix(path, filepath.Ext(path))+".d", "managed.yaml")
		if err := loadManagedOverlay(overlayPath, &cfg); err != nil {
			return nil, err
		}
	}

	fragments, err := filepath.Glob(filepath.Join(filepath.Dir(path), "conf.d", "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob conf.d fragments: %w", err)
	}
	sort.Strings(fragments)

	for _, fragPath := range fragments {
		frag, ok, err := loadFragment(fragPath)
		if err != nil {
			return nil, fmt.Errorf("load config fragment %s: %w", fragPath, err)
		}
		if !ok {
			continue
		}
		if !shouldMergeFragment(cfg.Agent.EffectiveRole(), frag.Agent.Role) {
			continue
		}
		mergeConfig(&cfg, frag)
	}

	return finalizeConfig(cfg)
}

func LoadReader(r io.Reader) (*Config, error) {
	cfg, err := decodeConfig(r)
	if err != nil {
		return nil, err
	}
	return finalizeConfig(cfg)
}

func decodeConfig(r io.Reader) (Config, error) {
	var cfg Config
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

func loadFragment(path string) (Config, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, false, err
	}
	defer f.Close()

	var frag Config
	if err := yaml.NewDecoder(f).Decode(&frag); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	return frag, true, nil
}

func shouldMergeFragment(baseRole, fragRole string) bool {
	base := strings.ToLower(strings.TrimSpace(baseRole))
	frag := strings.ToLower(strings.TrimSpace(fragRole))
	if base == RoleAgentProxy && (frag == RoleGateway || frag == "xconnect-gateway") {
		return false
	}
	if frag != "" && frag != base {
		return false
	}
	return true
}

func mergeConfig(dst *Config, src Config) {
	setIfNonEmpty(&dst.Mode, src.Mode)
	setIfNonEmpty(&dst.Log.Level, src.Log.Level)
	setIfNonEmpty(&dst.ID, src.ID)
	setIfNonEmpty(&dst.NodeID, src.NodeID)
	setIfNonEmpty(&dst.ControllerURL, src.ControllerURL)
	setIfNonEmpty(&dst.APIToken, src.APIToken)
	setIfNonEmpty(&dst.ResourceClass, src.ResourceClass)

	if src.Agent.CustomAllowFree {
		dst.Agent.CustomAllowFree = true
	}
	if len(src.Agent.AllowedEmails) > 0 {
		dst.Agent.AllowedEmails = append([]string(nil), src.Agent.AllowedEmails...)
	}
	setIfNonEmpty(&dst.Agent.TeamID, src.Agent.TeamID)
	setIfNonEmpty(&dst.Agent.OverlayStatusPath, src.Agent.OverlayStatusPath)
	setIfNonEmpty(&dst.Agent.GatewayTelemetryPath, src.Agent.GatewayTelemetryPath)
	if src.Agent.Gateway.Enabled {
		dst.Agent.Gateway.Enabled = true
	}
	setIfNonEmpty(&dst.Agent.Gateway.StateDir, src.Agent.Gateway.StateDir)
	if len(src.Agent.Gateway.ApplyCommand) > 0 {
		dst.Agent.Gateway.ApplyCommand = append([]string(nil), src.Agent.Gateway.ApplyCommand...)
	}
	setIfNonEmpty(&dst.Agent.Role, src.Agent.Role)
	setIfNonEmpty(&dst.Agent.ID, src.Agent.ID)
	setIfNonEmpty(&dst.Agent.NodeID, src.Agent.NodeID)
	setIfNonEmpty(&dst.Agent.NetworkID, src.Agent.NetworkID)
	setIfNonEmpty(&dst.Agent.Region, src.Agent.Region)
	setIfNonEmpty(&dst.Agent.Pool, src.Agent.Pool)
	setIfNonEmpty(&dst.Agent.ResourceClass, src.Agent.ResourceClass)
	setIfNonEmpty(&dst.Agent.EntryPoint, src.Agent.EntryPoint)
	if src.Agent.OpenToUsers != nil {
		v := *src.Agent.OpenToUsers
		dst.Agent.OpenToUsers = &v
	}
	setIfNonEmpty(&dst.Agent.Provider, src.Agent.Provider)
	setIfNonEmpty(&dst.Agent.Product, src.Agent.Product)
	setIfNonEmpty(&dst.Agent.LineCode, src.Agent.LineCode)
	setIfNonEmpty(&dst.Agent.PricingGroup, src.Agent.PricingGroup)
	if src.Agent.StatsEnabled {
		dst.Agent.StatsEnabled = true
	}
	setIfNonEmpty(&dst.Agent.ControllerURL, src.Agent.ControllerURL)
	setIfNonEmpty(&dst.Agent.APIToken, src.Agent.APIToken)
	setIfNonEmpty(&dst.Agent.Domain, src.Agent.Domain)
	if src.Agent.HTTPTimeout > 0 {
		dst.Agent.HTTPTimeout = src.Agent.HTTPTimeout
	}
	if src.Agent.StatusInterval > 0 {
		dst.Agent.StatusInterval = src.Agent.StatusInterval
	}
	if src.Agent.SyncInterval > 0 {
		dst.Agent.SyncInterval = src.Agent.SyncInterval
	}
	if src.Agent.TLS.InsecureSkipVerify {
		dst.Agent.TLS.InsecureSkipVerify = true
	}

	if src.Xray.Sync.Enabled {
		dst.Xray.Sync.Enabled = true
	}
	if src.Xray.Sync.Interval > 0 {
		dst.Xray.Sync.Interval = src.Xray.Sync.Interval
	}
	if len(src.Xray.Sync.Targets) > 0 {
		dst.Xray.Sync.Targets = append([]SyncTarget(nil), src.Xray.Sync.Targets...)
	}
	if src.Xray.Sync.Tuning != nil {
		dst.Xray.Sync.Tuning = src.Xray.Sync.Tuning
	}
	setIfNonEmpty(&dst.Xray.Sync.OutputPath, src.Xray.Sync.OutputPath)
	setIfNonEmpty(&dst.Xray.Sync.TemplatePath, src.Xray.Sync.TemplatePath)
	if len(src.Xray.Sync.ValidateCommand) > 0 {
		dst.Xray.Sync.ValidateCommand = append([]string(nil), src.Xray.Sync.ValidateCommand...)
	}
	if len(src.Xray.Sync.RestartCommand) > 0 {
		dst.Xray.Sync.RestartCommand = append([]string(nil), src.Xray.Sync.RestartCommand...)
	}

	if src.Billing.Enabled {
		dst.Billing.Enabled = true
	}
	setIfNonEmpty(&dst.Billing.BaseURL, src.Billing.BaseURL)
	if src.Billing.HTTPTimeout > 0 {
		dst.Billing.HTTPTimeout = src.Billing.HTTPTimeout
	}
	if src.Billing.CollectInterval > 0 {
		dst.Billing.CollectInterval = src.Billing.CollectInterval
	}
	if src.Billing.ReconcileInterval > 0 {
		dst.Billing.ReconcileInterval = src.Billing.ReconcileInterval
	}
}

func setIfNonEmpty(dst *string, val string) {
	if strings.TrimSpace(val) != "" {
		*dst = val
	}
}

func finalizeConfig(cfg Config) (*Config, error) {
	if cfg.Mode == "" {
		cfg.Mode = "agent"
	}
	if cfg.Agent.ID == "" && cfg.ID != "" {
		cfg.Agent.ID = strings.TrimSpace(cfg.ID)
	}
	if cfg.Agent.NodeID == "" && cfg.NodeID != "" {
		cfg.Agent.NodeID = strings.TrimSpace(cfg.NodeID)
	}
	if cfg.Agent.ControllerURL == "" && cfg.ControllerURL != "" {
		cfg.Agent.ControllerURL = strings.TrimSpace(cfg.ControllerURL)
	}
	if cfg.Agent.APIToken == "" && cfg.APIToken != "" {
		cfg.Agent.APIToken = strings.TrimSpace(cfg.APIToken)
	}
	if cfg.Agent.ResourceClass == "" && cfg.ResourceClass != "" {
		cfg.Agent.ResourceClass = strings.TrimSpace(cfg.ResourceClass)
	}
	if cfg.Agent.NodeID == "" && cfg.Agent.ID != "" {
		cfg.Agent.NodeID = cfg.Agent.ID
	}

	if authURL := os.Getenv("AuthUrl"); authURL != "" {
		cfg.Agent.ControllerURL = authURL
	}
	if token := os.Getenv("INTERNAL_SERVICE_TOKEN"); token != "" {
		cfg.Agent.APIToken = token
	}
	if domain := os.Getenv("DOMAIN"); domain != "" {
		cfg.Agent.Domain = domain
	}
	if billingURL := os.Getenv("BILLING_SERVICE_BASE_URL"); billingURL != "" {
		cfg.Billing.BaseURL = billingURL
	}
	if role := os.Getenv("XCONNECT_ROLE"); role != "" {
		cfg.Agent.Role = role
	}
	if value, ok := os.LookupEnv("AGENT_RESOURCE_CLASS"); ok {
		cfg.Agent.ResourceClass = value
	}
	if value, ok := os.LookupEnv("AGENT_TEAM_ID"); ok {
		cfg.Agent.TeamID = value
	}
	if value, ok := os.LookupEnv("AGENT_CUSTOM_EMAILS"); ok {
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "[") {
			if err := json.Unmarshal([]byte(value), &cfg.Agent.AllowedEmails); err != nil {
				return nil, fmt.Errorf("AGENT_CUSTOM_EMAILS must be an email or a JSON array of emails")
			}
		} else {
			cfg.Agent.AllowedEmails = []string{value}
		}
		if len(cfg.Agent.AllowedEmails) == 0 {
			return nil, fmt.Errorf("AGENT_CUSTOM_EMAILS must not be empty")
		}
	}
	if cfg.Agent.ResourceClass == "team" && len(cfg.Agent.AllowedEmails) > 0 {
		return nil, fmt.Errorf("team nodes do not support allowedEmails")
	}
	normalized, err := agentproto.NormalizeNodeEmails(cfg.Agent.AllowedEmails)
	if err != nil {
		return nil, err
	}
	cfg.Agent.AllowedEmails = normalized
	if cfg.Agent.ResourceClass == "custom" && len(cfg.Agent.AllowedEmails) == 0 {
		return nil, fmt.Errorf("custom agent requires a non-empty allowedEmails list")
	}
	if err := cfg.Agent.ValidateUserScope(); err != nil {
		return nil, err
	}
	if err := cfg.Agent.ValidateRole(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
