//go:build xrayintegration

package xrayconfig

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExtendedXrayRevokesOnlySelectedUserAndHotRestores(t *testing.T) {
	binary := os.Getenv("XRAY_SESSION_TEST_BINARY")
	if binary == "" {
		t.Skip("XRAY_SESSION_TEST_BINARY must select the maintained core")
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetPort := target.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = c.Write([]byte("ready")); _, _ = io.Copy(c, c) }(conn)
		}
	}()
	a := Client{ID: "00000000-0000-4000-8000-000000000001", Email: "a@example.test"}
	b := Client{ID: "00000000-0000-4000-8000-000000000002", Email: "b@example.test"}
	inboundPort, apiPort := freeTCPPort(t), freeTCPPort(t)
	config := map[string]interface{}{
		"api":       map[string]interface{}{"tag": "api", "listen": net.JoinHostPort("127.0.0.1", itoa(apiPort)), "services": []string{"HandlerService"}},
		"inbounds":  []interface{}{map[string]interface{}{"tag": "test-vless", "listen": "127.0.0.1", "port": inboundPort, "protocol": "vless", "settings": map[string]interface{}{"decryption": "none", "clients": []interface{}{map[string]interface{}{"id": a.ID, "email": a.Email}, map[string]interface{}{"id": b.ID, "email": b.Email}}}}},
		"outbounds": []interface{}{map[string]interface{}{"protocol": "freedom"}},
	}
	payload, _ := json.Marshal(config)
	path := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process := exec.CommandContext(ctx, binary, "run", "-c", path)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = process.Wait() }()
	address, api := net.JoinHostPort("127.0.0.1", itoa(inboundPort)), net.JoinHostPort("127.0.0.1", itoa(apiPort))
	waitTCP(t, api)
	connect := func(id string) net.Conn {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Write(vlessRequest(id, targetPort))
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 7)
		if _, err := io.ReadFull(conn, header); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if string(header[2:]) != "ready" {
			t.Fatalf("bad greeting: %v", header)
		}
		_ = conn.SetDeadline(time.Time{})
		return conn
	}
	echo := func(conn net.Conn) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte("alive")); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 5)
		if _, err := io.ReadFull(conn, data); err != nil || string(data) != "alive" {
			t.Fatalf("unrelated session interrupted: %q %v", data, err)
		}
		_ = conn.SetDeadline(time.Time{})
	}
	a1, a2, b1 := connect(a.ID), connect(a.ID), connect(b.ID)
	defer a1.Close()
	defer a2.Close()
	defer b1.Close()
	g := Generator{Definition: JSONDefinition{Raw: payload}}
	cli, err := NewCLIUserAdder(binary, api)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.RevokeUsers(context.Background(), g, []Client{a}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []net.Conn{a1, a2} {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err := conn.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("revoked session stayed open")
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("revocation merely stalled the session")
		}
	}
	echo(b1)
	if err := cli.RevokeUsers(context.Background(), g, []Client{a}); err != nil {
		t.Fatalf("repeated revocation failed: %v", err)
	}
	rejected, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rejected.Close()
	_ = rejected.SetDeadline(time.Now().Add(time.Second))
	_, _ = rejected.Write(vlessRequest(a.ID, targetPort))
	if _, err := io.ReadFull(rejected, make([]byte, 2)); err == nil {
		t.Fatal("revoked UUID authenticated")
	}
	a.Flow = "" // This fixture has a plain TCP inbound without Vision.
	restored, _ := g.Render([]Client{a})
	var doc map[string]interface{}
	_ = json.Unmarshal(restored, &doc)
	inbound := doc["inbounds"].([]interface{})[0].(map[string]interface{})
	entry := inbound["settings"].(map[string]interface{})["clients"].([]interface{})[0].(map[string]interface{})
	delete(entry, "flow")
	restorePayload, _ := json.Marshal(doc)
	restorePath := filepath.Join(t.TempDir(), "restore.json")
	_ = os.WriteFile(restorePath, restorePayload, 0600)
	output, err := exec.Command(binary, "api", "adu", "--server="+api, restorePath).CombinedOutput()
	if err != nil {
		t.Fatalf("hot restore failed: %v %s", err, output)
	}
	recovered := connect(a.ID)
	defer recovered.Close()
	echo(recovered)
	echo(b1)
	t.Logf("revoked two sessions, denied new authentication, preserved other user, restored access; same server PID=%d", process.Process.Pid)
}
