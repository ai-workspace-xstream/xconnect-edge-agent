package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode    string  `yaml:"mode"`
	Log     Log     `yaml:"log"`
	Agent   Agent   `yaml:"agent"`
	Xray    Xray    `yaml:"xray"`
	Billing Billing `yaml:"billing"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Agent struct {
	OverlayStatusPath string        `yaml:"overlayStatusPath"`
	Role              string        `yaml:"role"`
	ID                string        `yaml:"id"`
	NodeID            string        `yaml:"nodeId"`
	NetworkID         string        `yaml:"networkId"`
	Region            string        `yaml:"region"`
	Pool              string        `yaml:"pool"`
	ResourceClass     string        `yaml:"resourceClass"`
	EntryPoint        string        `yaml:"entryPoint"`
	OpenToUsers       *bool         `yaml:"openToUsers"`
	Provider          string        `yaml:"provider"`
	Product           string        `yaml:"product"`
	LineCode          string        `yaml:"lineCode"`
	PricingGroup      string        `yaml:"pricingGroup"`
	StatsEnabled      bool          `yaml:"statsEnabled"`
	ControllerURL     string        `yaml:"controllerUrl"`
	APIToken          string        `yaml:"apiToken"`
	Domain            string        `yaml:"domain"`
	HTTPTimeout       time.Duration `yaml:"httpTimeout"`
	StatusInterval    time.Duration `yaml:"statusInterval"`
	SyncInterval      time.Duration `yaml:"syncInterval"`
	TLS               TLS           `yaml:"tls"`
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

	// Legacy fields for backward compatibility or simple single-target config
	OutputPath      string   `yaml:"outputPath"`
	TemplatePath    string   `yaml:"templatePath"`
	ValidateCommand []string `yaml:"validateCommand"`
	RestartCommand  []string `yaml:"restartCommand"`
}

type SyncTarget struct {
	Name            string          `yaml:"name"`
	OutputPath      string          `yaml:"outputPath"`
	TemplatePath    string          `yaml:"templatePath"`
	ValidateCommand []string        `yaml:"validateCommand"`
	RestartCommand  []string        `yaml:"restartCommand"`
	DynamicUsers    DynamicUserSync `yaml:"dynamicUsers"`
}

// DynamicUserSync configures Xray HandlerService updates for runtime user changes.
type DynamicUserSync struct {
	Enabled    bool   `yaml:"enabled"`
	Server     string `yaml:"server"`
	Executable string `yaml:"executable"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	cfg, err := decodeConfig(f)
	if err != nil {
		return nil, err
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

	if err := applyEnvAndValidate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadReader(r io.Reader) (*Config, error) {
	cfg, err := decodeConfig(r)
	if err != nil {
		return nil, err
	}
	if err := applyEnvAndValidate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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

	setIfNonEmpty(&dst.Agent.OverlayStatusPath, src.Agent.OverlayStatusPath)
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

func applyEnvAndValidate(cfg *Config) error {
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
	if err := cfg.Agent.ValidateRole(); err != nil {
		return err
	}

	return nil
}
