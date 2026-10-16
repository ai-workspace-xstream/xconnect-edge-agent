package xrayconfig

import (
	"encoding/json"
	"sort"
)

// ServerTuning is the allowlisted set of runtime knobs that an operator (agent
// config) or the controller (users response) may adjust on a node. Every field
// is optional; an unset field leaves the template value untouched. Nothing
// outside this list is ever copied into the Xray configuration.
type ServerTuning struct {
	// LogLevel replaces log.loglevel: debug, info, warning, error or none.
	LogLevel string `json:"logLevel,omitempty" yaml:"logLevel"`
	// BufferSizeKB sets policy.levels.0.bufferSize: how much each connection
	// may buffer between inbound and outbound. Without it Xray uses 4 KB on
	// arm64 (for example AWS t4g) and 512 KB on amd64. It is a capacity, not
	// a memory limit: a larger value can raise throughput and also raises the
	// worst-case memory of busy connections, so choose it from a load test.
	BufferSizeKB *int `json:"bufferSizeKB,omitempty" yaml:"bufferSizeKB"`
	// The four timeouts below set policy.levels.0, in seconds.
	ConnIdleSeconds     *int `json:"connIdleSeconds,omitempty" yaml:"connIdleSeconds"`
	HandshakeSeconds    *int `json:"handshakeSeconds,omitempty" yaml:"handshakeSeconds"`
	UplinkOnlySeconds   *int `json:"uplinkOnlySeconds,omitempty" yaml:"uplinkOnlySeconds"`
	DownlinkOnlySeconds *int `json:"downlinkOnlySeconds,omitempty" yaml:"downlinkOnlySeconds"`
	// XHTTPMaxEachPostBytes sets scMaxEachPostBytes on XHTTP inbounds. It must
	// be at least the value clients send for packet-up uploads.
	XHTTPMaxEachPostBytes *int `json:"xhttpScMaxEachPostBytes,omitempty" yaml:"xhttpScMaxEachPostBytes"`
	// XHTTPMaxBufferedPosts sets scMaxBufferedPosts on XHTTP inbounds.
	XHTTPMaxBufferedPosts *int `json:"xhttpScMaxBufferedPosts,omitempty" yaml:"xhttpScMaxBufferedPosts"`
}

var allowedLogLevels = map[string]bool{
	"debug": true, "info": true, "warning": true, "error": true, "none": true,
}

type intBound struct {
	name     string
	value    func(*ServerTuning) **int
	min, max int
}

var tuningBounds = []intBound{
	{"bufferSizeKB", func(t *ServerTuning) **int { return &t.BufferSizeKB }, 0, 4096},
	{"connIdleSeconds", func(t *ServerTuning) **int { return &t.ConnIdleSeconds }, 10, 3600},
	{"handshakeSeconds", func(t *ServerTuning) **int { return &t.HandshakeSeconds }, 1, 60},
	{"uplinkOnlySeconds", func(t *ServerTuning) **int { return &t.UplinkOnlySeconds }, 0, 60},
	{"downlinkOnlySeconds", func(t *ServerTuning) **int { return &t.DownlinkOnlySeconds }, 0, 60},
	{"xhttpScMaxEachPostBytes", func(t *ServerTuning) **int { return &t.XHTTPMaxEachPostBytes }, 64 << 10, 16 << 20},
	{"xhttpScMaxBufferedPosts", func(t *ServerTuning) **int { return &t.XHTTPMaxBufferedPosts }, 1, 1000},
}

// Normalize returns a copy with every out-of-range or unknown value removed,
// along with the names of the fields it dropped. It returns nil when nothing
// usable remains.
func (t *ServerTuning) Normalize() (*ServerTuning, []string) {
	if t == nil {
		return nil, nil
	}
	out := *t
	var rejected []string
	if out.LogLevel != "" && !allowedLogLevels[out.LogLevel] {
		rejected = append(rejected, "logLevel")
		out.LogLevel = ""
	}
	for _, bound := range tuningBounds {
		field := bound.value(&out)
		if *field == nil {
			continue
		}
		if v := **field; v < bound.min || v > bound.max {
			rejected = append(rejected, bound.name)
			*field = nil
		} else {
			copied := v
			*field = &copied
		}
	}
	if out.isEmpty() {
		return nil, rejected
	}
	return &out, rejected
}

func (t *ServerTuning) isEmpty() bool {
	if t.LogLevel != "" {
		return false
	}
	for _, bound := range tuningBounds {
		if *bound.value(t) != nil {
			return false
		}
	}
	return true
}

// Overlay returns base with every field set in override replacing it.
func (t *ServerTuning) Overlay(override *ServerTuning) *ServerTuning {
	if t == nil && override == nil {
		return nil
	}
	var out ServerTuning
	if t != nil {
		out = *t
	}
	if override == nil {
		return &out
	}
	if override.LogLevel != "" {
		out.LogLevel = override.LogLevel
	}
	for _, bound := range tuningBounds {
		if value := *bound.value(override); value != nil {
			*bound.value(&out) = value
		}
	}
	return &out
}

// Key is a stable fingerprint used to detect tuning changes between syncs.
func (t *ServerTuning) Key() string {
	if t == nil {
		return ""
	}
	encoded, _ := json.Marshal(t)
	return string(encoded)
}

// apply writes the tuning into a rendered Xray configuration, keeping every
// key it does not manage.
func (t *ServerTuning) apply(root map[string]interface{}) {
	if t == nil {
		return
	}
	if t.LogLevel != "" {
		log := childMap(root, "log")
		log["loglevel"] = t.LogLevel
	}

	levelValues := map[string]*int{
		"bufferSize":   t.BufferSizeKB,
		"connIdle":     t.ConnIdleSeconds,
		"handshake":    t.HandshakeSeconds,
		"uplinkOnly":   t.UplinkOnlySeconds,
		"downlinkOnly": t.DownlinkOnlySeconds,
	}
	keys := make([]string, 0, len(levelValues))
	for key, value := range levelValues {
		if value != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		level := childMap(childMap(childMap(root, "policy"), "levels"), "0")
		for _, key := range keys {
			level[key] = *levelValues[key]
		}
	}

	if t.XHTTPMaxEachPostBytes == nil && t.XHTTPMaxBufferedPosts == nil {
		return
	}
	inbounds, _ := root["inbounds"].([]interface{})
	for _, value := range inbounds {
		inbound, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		stream, ok := inbound["streamSettings"].(map[string]interface{})
		if !ok || stream["network"] != "xhttp" {
			continue
		}
		settings := childMap(stream, "xhttpSettings")
		// Xray replaces every field except host, path and mode with "extra"
		// when it is present, so tuned fields must live where Xray reads them.
		if extra, ok := settings["extra"].(map[string]interface{}); ok {
			settings = extra
		}
		if t.XHTTPMaxEachPostBytes != nil {
			settings["scMaxEachPostBytes"] = *t.XHTTPMaxEachPostBytes
		}
		if t.XHTTPMaxBufferedPosts != nil {
			settings["scMaxBufferedPosts"] = *t.XHTTPMaxBufferedPosts
		}
	}
}

func childMap(parent map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := parent[key].(map[string]interface{}); ok {
		return existing
	}
	created := map[string]interface{}{}
	parent[key] = created
	return created
}
