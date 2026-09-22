package config

import (
	"fmt"
	"io"
	"os"
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
	Role           string        `yaml:"role"`
	ID             string        `yaml:"id"`
	NodeID         string        `yaml:"nodeId"`
	Region         string        `yaml:"region"`
	Pool           string        `yaml:"pool"`
	Provider       string        `yaml:"provider"`
	Product        string        `yaml:"product"`
	LineCode       string        `yaml:"lineCode"`
	PricingGroup   string        `yaml:"pricingGroup"`
	StatsEnabled   bool          `yaml:"statsEnabled"`
	ControllerURL  string        `yaml:"controllerUrl"`
	APIToken       string        `yaml:"apiToken"`
	Domain         string        `yaml:"domain"`
	HTTPTimeout    time.Duration `yaml:"httpTimeout"`
	StatusInterval time.Duration `yaml:"statusInterval"`
	SyncInterval   time.Duration `yaml:"syncInterval"`
	TLS            TLS           `yaml:"tls"`
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

// DynamicUserSync configures Xray HandlerService updates for client additions.
// Withdrawing a paused user's node-local credential still restarts Xray so
// established sessions are terminated immediately; the account is never deleted.
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

	return LoadReader(f)
}

func LoadReader(r io.Reader) (*Config, error) {
	var cfg Config
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
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
	if err := cfg.Agent.ValidateRole(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
