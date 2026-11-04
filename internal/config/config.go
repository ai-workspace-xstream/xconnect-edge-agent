package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

type Config struct {
	ConfigVersion int          `yaml:"configVersion"`
	VirtualNodes  VirtualNodes `yaml:"virtualNodes"`
	Mode          string       `yaml:"mode"`
	Log           Log          `yaml:"log"`
	Agent         Agent        `yaml:"agent"`
	Xray          Xray         `yaml:"xray"`
	Billing       Billing      `yaml:"billing"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Agent struct {
	CustomAllowFree bool `yaml:"customAllowFree"`
	// ResourceClass selects the user sync scope: shared, free, plus, pro, or team.
	AllowedEmails     []string      `yaml:"allowedEmails"`
	ResourceClass     string        `yaml:"resourceClass"`
	TeamID            string        `yaml:"teamId"`
	OverlayStatusPath string        `yaml:"overlayStatusPath"`
	Role              string        `yaml:"role"`
	ID                string        `yaml:"id"`
	NodeID            string        `yaml:"nodeId"`
	NetworkID         string        `yaml:"networkId"`
	Region            string        `yaml:"region"`
	Pool              string        `yaml:"pool"`
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
	Name            string          `yaml:"name"`
	OutputPath      string          `yaml:"outputPath"`
	TemplatePath    string          `yaml:"templatePath"`
	ValidateCommand []string        `yaml:"validateCommand"`
	RestartCommand  []string        `yaml:"restartCommand"`
	DynamicUsers    DynamicUserSync `yaml:"dynamicUsers"`
}

// DynamicUserSync configures Xray HandlerService updates for runtime user changes.
type DynamicUserSync struct {
	TerminateSessions bool   `yaml:"terminateSessions"`
	Enabled           bool   `yaml:"enabled"`
	Server            string `yaml:"server"`
	Executable        string `yaml:"executable"`
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
	payload, err := io.ReadAll(io.LimitReader(r, (4<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(payload) > 4<<20 {
		return nil, fmt.Errorf("config exceeds 4 MiB limit")
	}
	var cfg Config
	versioned, err := checkConfigEnvelope(payload)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(versioned)
	if err := decoder.Decode(&cfg); err != nil {
		if versioned {
			return nil, fmt.Errorf("decode v2 config: invalid field or type (values omitted)")
		}
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if cfg.VirtualNodes.Enabled {
		for _, key := range []string{"AuthUrl", "INTERNAL_SERVICE_TOKEN", "DOMAIN", "XCONNECT_ROLE", "AGENT_RESOURCE_CLASS", "AGENT_TEAM_ID", "AGENT_CUSTOM_EMAILS"} {
			if _, exists := os.LookupEnv(key); exists {
				return nil, fmt.Errorf("virtualNodes conflicts with environment variable %s", key)
			}
		}
		if len(cfg.VirtualNodes.Nodes) == 0 {
			return nil, fmt.Errorf("virtualNodes.nodes must not be empty")
		}
		return nil, ErrVirtualNodesNotReady
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
