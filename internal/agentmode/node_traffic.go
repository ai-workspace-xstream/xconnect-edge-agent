package agentmode

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
)

func sampleNodeTraffic(iface string) (*agentproto.NodeTrafficSample, error) {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	boot := strings.TrimSpace(string(bootID))
	if boot == "" {
		return nil, fmt.Errorf("empty Linux boot ID")
	}

	counters, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	rx, tx, err := parseNodeTraffic(string(counters), iface)
	if err != nil {
		return nil, err
	}
	return &agentproto.NodeTrafficSample{
		Interface: iface, BootID: boot, RxBytes: rx, TxBytes: tx, SampledAt: time.Now().UTC(),
	}, nil
}

func parseNodeTraffic(counters, iface string) (uint64, uint64, error) {
	for _, line := range strings.Split(counters, "\n") {
		name, values, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != iface {
			continue
		}
		fields := strings.Fields(values)
		if len(fields) != 16 {
			return 0, 0, fmt.Errorf("invalid Linux interface counter row")
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse receive bytes: %w", err)
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse transmit bytes: %w", err)
		}
		return rx, tx, nil
	}
	return 0, 0, fmt.Errorf("interface not found in /proc/net/dev")
}
