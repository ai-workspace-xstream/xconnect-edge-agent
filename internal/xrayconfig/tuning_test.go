package xrayconfig

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

const tuningTemplate = `{
  "log": {"loglevel": "debug", "access": "none"},
  "policy": {"levels": {"0": {"statsUserUplink": true, "handshake": 4}}},
  "inbounds": [
    {"tag": "xhttp", "protocol": "vless", "settings": {"clients": []},
     "streamSettings": {"network": "xhttp", "xhttpSettings": {"mode": "auto", "path": "/split"}}},
    {"tag": "xhttp-extra", "protocol": "vless", "settings": {"clients": []},
     "streamSettings": {"network": "xhttp", "xhttpSettings": {"path": "/x", "extra": {"noSSEHeader": true}}}},
    {"tag": "tcp", "protocol": "vless", "settings": {"clients": []},
     "streamSettings": {"network": "tcp"}}
  ]
}`

func renderTuned(t *testing.T, tuning *ServerTuning) map[string]interface{} {
	t.Helper()
	generator := Generator{
		Definition: JSONDefinition{Raw: []byte(tuningTemplate)},
		Tuning:     tuning,
	}
	buf, err := generator.Render([]Client{{ID: "client-1", Email: "a@example.com"}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(buf, &root); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return root
}

func xhttpSettingsOf(root map[string]interface{}, index int) map[string]interface{} {
	inbound := root["inbounds"].([]interface{})[index].(map[string]interface{})
	stream := inbound["streamSettings"].(map[string]interface{})
	settings, _ := stream["xhttpSettings"].(map[string]interface{})
	return settings
}

func TestServerTuningNormalizeDropsOutOfRangeValues(t *testing.T) {
	tuning, rejected := (&ServerTuning{
		LogLevel:              "verbose",
		BufferSizeKB:          intPtr(512),
		ConnIdleSeconds:       intPtr(1),
		XHTTPMaxEachPostBytes: intPtr(2 << 20),
	}).Normalize()
	sort.Strings(rejected)
	if want := []string{"connIdleSeconds", "logLevel"}; !reflect.DeepEqual(rejected, want) {
		t.Fatalf("rejected = %v, want %v", rejected, want)
	}
	if tuning == nil || tuning.LogLevel != "" || tuning.ConnIdleSeconds != nil {
		t.Fatalf("invalid values survived: %+v", tuning)
	}
	if *tuning.BufferSizeKB != 512 || *tuning.XHTTPMaxEachPostBytes != 2<<20 {
		t.Fatalf("valid values were lost: %+v", tuning)
	}

	if empty, _ := (&ServerTuning{LogLevel: "loud"}).Normalize(); empty != nil {
		t.Fatalf("fully invalid tuning should normalize to nil, got %+v", empty)
	}
}

func TestServerTuningOverlayPrefersControllerFieldByField(t *testing.T) {
	local := &ServerTuning{LogLevel: "warning", BufferSizeKB: intPtr(256), ConnIdleSeconds: intPtr(300)}
	remote := &ServerTuning{BufferSizeKB: intPtr(1024)}
	merged := local.Overlay(remote)
	if merged.LogLevel != "warning" || *merged.BufferSizeKB != 1024 || *merged.ConnIdleSeconds != 300 {
		t.Fatalf("unexpected overlay: %+v", merged)
	}
	if *local.BufferSizeKB != 256 {
		t.Fatal("overlay mutated the local tuning")
	}
	if (*ServerTuning)(nil).Overlay(nil) != nil {
		t.Fatal("overlay of two nil tunings should be nil")
	}
}

func TestGeneratorAppliesTuningAndKeepsUnmanagedKeys(t *testing.T) {
	root := renderTuned(t, &ServerTuning{
		LogLevel:              "warning",
		BufferSizeKB:          intPtr(512),
		ConnIdleSeconds:       intPtr(300),
		XHTTPMaxEachPostBytes: intPtr(2 << 20),
		XHTTPMaxBufferedPosts: intPtr(60),
	})

	log := root["log"].(map[string]interface{})
	if log["loglevel"] != "warning" || log["access"] != "none" {
		t.Fatalf("log section: %v", log)
	}
	level := root["policy"].(map[string]interface{})["levels"].(map[string]interface{})["0"].(map[string]interface{})
	if level["bufferSize"] != float64(512) || level["connIdle"] != float64(300) {
		t.Fatalf("policy level not tuned: %v", level)
	}
	if level["statsUserUplink"] != true || level["handshake"] != float64(4) {
		t.Fatalf("unmanaged policy keys were lost: %v", level)
	}

	plain := xhttpSettingsOf(root, 0)
	if plain["scMaxEachPostBytes"] != float64(2<<20) || plain["scMaxBufferedPosts"] != float64(60) {
		t.Fatalf("xhttp inbound not tuned: %v", plain)
	}
	// Xray replaces the other fields with "extra" when it is present.
	withExtra := xhttpSettingsOf(root, 1)
	extra := withExtra["extra"].(map[string]interface{})
	if extra["scMaxEachPostBytes"] != float64(2<<20) || extra["noSSEHeader"] != true {
		t.Fatalf("tuning must be written inside extra: %v", withExtra)
	}
	if _, leaked := withExtra["scMaxEachPostBytes"]; leaked {
		t.Fatalf("tuning written where Xray ignores it: %v", withExtra)
	}
	if xhttpSettingsOf(root, 2) != nil {
		t.Fatal("non-xhttp inbound gained xhttp settings")
	}
}

func TestGeneratorWithoutTuningKeepsTemplate(t *testing.T) {
	root := renderTuned(t, nil)
	if root["log"].(map[string]interface{})["loglevel"] != "debug" {
		t.Fatal("untuned render changed the log level")
	}
	if _, ok := xhttpSettingsOf(root, 0)["scMaxEachPostBytes"]; ok {
		t.Fatal("untuned render added xhttp limits")
	}
}

type tuningClientSource struct {
	mutableClientSource
	tuning *ServerTuning
}

func (s *tuningClientSource) ServerTuning() *ServerTuning { return s.tuning }

func TestPeriodicSyncerRestartsXrayOnlyWhenTuningChanges(t *testing.T) {
	client := Client{ID: "client-1", Email: "a@example.com", Flow: DefaultFlow}
	source := &tuningClientSource{
		mutableClientSource: mutableClientSource{clients: []Client{client}},
		tuning:              &ServerTuning{BufferSizeKB: intPtr(256)},
	}
	output := t.TempDir() + "/config.json"
	restarts := 0
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Minute,
		Source:   source,
		Generator: Generator{
			Definition: DefaultDefinition(),
			OutputPath: output,
		},
		RestartCommand: []string{"restart-xray"},
		Runner: func(context.Context, []string) ([]byte, error) {
			restarts++
			return nil, nil
		},
		LocalTuning: &ServerTuning{LogLevel: "warning", BufferSizeKB: intPtr(128)},
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := syncer.sync(ctx); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	if restarts != 1 {
		t.Fatalf("unchanged tuning restarted Xray: restarts=%d", restarts)
	}

	source.tuning = &ServerTuning{BufferSizeKB: intPtr(1024)}
	if _, err := syncer.sync(ctx); err != nil {
		t.Fatalf("tuning sync: %v", err)
	}
	if restarts != 2 {
		t.Fatalf("tuning change did not restart Xray: restarts=%d", restarts)
	}
	written, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(written, &root); err != nil {
		t.Fatal(err)
	}
	level := root["policy"].(map[string]interface{})["levels"].(map[string]interface{})["0"].(map[string]interface{})
	if level["bufferSize"] != float64(1024) {
		t.Fatalf("controller tuning not written: %v", level)
	}
	if root["log"].(map[string]interface{})["loglevel"] != "warning" {
		t.Fatalf("local tuning lost when controller tuning changed: %v", root["log"])
	}
}

func TestPeriodicSyncerWritesTuningWithoutRestartCommand(t *testing.T) {
	source := &tuningClientSource{
		mutableClientSource: mutableClientSource{clients: []Client{{ID: "client-1", Email: "a@example.com"}}},
	}
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Minute,
		Source:   source,
		Generator: Generator{
			Definition: DefaultDefinition(),
			OutputPath: t.TempDir() + "/config.json",
		},
		Runner: func(context.Context, []string) ([]byte, error) {
			t.Fatal("no command should run")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	source.tuning = &ServerTuning{LogLevel: "error"}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("tuning without restart command must not fail: %v", err)
	}
}
