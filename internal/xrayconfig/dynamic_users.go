package xrayconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CLIUserAdder uses `xray api adu` to add users through HandlerService. The
// generated config is persisted separately by PeriodicSyncer; this temporary
// file is only the command payload understood by Xray's CLI.
type CLIUserAdder struct {
	executable string
	server     string
	runner     commandRunner
}

// RevokeUsers uses a capability-specific operation; an unpatched server fails
// explicitly rather than reporting a successful authentication-only removal.
func (a *CLIUserAdder) RevokeUsers(ctx context.Context, generator Generator, clients []Client) error {
	if len(clients) == 0 {
		return nil
	}
	payload, err := generator.Render(nil)
	if err != nil {
		return err
	}
	var root struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return err
	}
	tags := []string{}
	for _, inbound := range root.Inbounds {
		if inbound.Protocol == "vless" {
			if inbound.Tag == "" {
				return errors.New("VLESS inbound tag required for session revocation")
			}
			tags = append(tags, inbound.Tag)
		}
	}
	if len(tags) == 0 {
		return errors.New("no VLESS inbound found for session revocation")
	}
	for _, tag := range tags {
		cmd := []string{a.executable, "api", "rmus", "--server=" + a.server, "-tag=" + tag}
		for _, client := range clients {
			if strings.TrimSpace(client.Email) == "" || strings.HasPrefix(client.Email, "-") {
				return errors.New("invalid revocation identity")
			}
			cmd = append(cmd, client.Email)
		}
		output, err := a.runner(ctx, cmd)
		if err != nil {
			return fmt.Errorf("session revocation failed: %w", err)
		}
		want := fmt.Sprintf("Revoked %d user session set(s) in total.", len(clients))
		if !strings.Contains(string(output), want) {
			return errors.New("session revocation receipt missing")
		}
	}
	return nil
}

func NewCLIUserAdder(executable, server string) (*CLIUserAdder, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "xray"
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, errors.New("dynamic user API server is required")
	}
	return &CLIUserAdder{executable: executable, server: server, runner: defaultCommandRunner}, nil
}

func (a *CLIUserAdder) AddUsers(ctx context.Context, generator Generator, clients []Client) error {
	if len(clients) == 0 {
		return nil
	}
	payload, err := generator.Render(clients)
	if err != nil {
		return fmt.Errorf("render dynamic user payload: %w", err)
	}
	tmp, err := os.CreateTemp("", "agent-svc-plus-xray-users-*.json")
	if err != nil {
		return fmt.Errorf("create dynamic user payload: %w", err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure dynamic user payload: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write dynamic user payload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close dynamic user payload: %w", err)
	}

	runner := a.runner
	if runner == nil {
		runner = defaultCommandRunner
	}
	output, err := runner(ctx, []string{a.executable, "api", "adu", "--server=" + a.server, path})
	if err != nil {
		return fmt.Errorf("add xray users: %w: %s", err, strings.TrimSpace(string(output)))
	}
	want := "Added " + strconv.Itoa(len(clients)) + " user(s) in total."
	if !strings.Contains(string(output), want) {
		return fmt.Errorf("add xray users: expected %q, got %q", want, strings.TrimSpace(string(output)))
	}
	return nil
}
