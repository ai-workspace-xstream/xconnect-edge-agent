//go:build xrayintegration

package xrayconfig

import (
	"context"
	wirebinary "encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExtendedXrayXHTTPSessionIsolation(t *testing.T) {
	binary := os.Getenv("XRAY_SESSION_TEST_BINARY")
	if binary == "" {
		t.Skip("XRAY_SESSION_TEST_BINARY required")
	}
	for _, mode := range []string{"packet-up", "stream-up", "tcp-mux"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := func(name string, config map[string]interface{}, port int) {
				t.Helper()
				config["log"] = map[string]interface{}{"loglevel": "debug"}
				payload, _ := json.Marshal(config)
				path := filepath.Join(t.TempDir(), name+".json")
				if err := os.WriteFile(path, payload, 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, binary, "run", "-c", path)
				cmd.Stderr = os.Stderr
				cmd.Stdout = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				waitTCP(t, net.JoinHostPort("127.0.0.1", itoa(port)))
			}
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			targetPort := target.Addr().(*net.TCPAddr).Port
			go func() {
				for {
					c, err := target.Accept()
					if err != nil {
						return
					}
					go func(c net.Conn) { defer c.Close(); _, _ = c.Write([]byte("ready")); _, _ = io.Copy(c, c) }(c)
				}
			}()
			a := Client{ID: "00000000-0000-4000-8000-000000000001", Email: "a@example.test"}
			b := Client{ID: "00000000-0000-4000-8000-000000000002", Email: "b@example.test"}
			serverPort, apiPort := freeTCPPort(t), freeTCPPort(t)
			server := map[string]interface{}{"api": map[string]interface{}{"listen": "127.0.0.1:" + itoa(apiPort), "services": []string{"HandlerService"}}, "inbounds": []interface{}{map[string]interface{}{"tag": "test-vless", "listen": "127.0.0.1", "port": serverPort, "protocol": "vless", "settings": map[string]interface{}{"decryption": "none", "clients": []interface{}{map[string]interface{}{"id": a.ID, "email": a.Email}, map[string]interface{}{"id": b.ID, "email": b.Email}}}, "streamSettings": map[string]interface{}{"network": "xhttp", "xhttpSettings": map[string]interface{}{"path": "/session-test", "mode": "auto"}}}}, "outbounds": []interface{}{map[string]interface{}{"protocol": "freedom"}}}
			server["api"].(map[string]interface{})["tag"] = "api"
			if mode == "tcp-mux" {
				server["inbounds"].([]interface{})[0].(map[string]interface{})["streamSettings"] = map[string]interface{}{"network": "tcp"}
			}
			start("server", server, apiPort)
			client := func(u Client) int {
				port := freeTCPPort(t)
				config := map[string]interface{}{"inbounds": []interface{}{map[string]interface{}{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]interface{}{"auth": "noauth"}}}, "outbounds": []interface{}{map[string]interface{}{"protocol": "vless", "settings": map[string]interface{}{"vnext": []interface{}{map[string]interface{}{"address": "127.0.0.1", "port": serverPort, "users": []interface{}{map[string]interface{}{"id": u.ID, "encryption": "none"}}}}}, "streamSettings": map[string]interface{}{"network": "xhttp", "xhttpSettings": map[string]interface{}{"path": "/session-test", "mode": mode}}, "mux": map[string]interface{}{"enabled": true, "concurrency": 4}}}}
				out := config["outbounds"].([]interface{})[0].(map[string]interface{})
				out["mux"].(map[string]interface{})["enabled"] = mode == "tcp-mux"
				if mode == "tcp-mux" {
					out["streamSettings"] = map[string]interface{}{"network": "tcp"}
				}
				start(u.Email, config, port)
				return port
			}
			aPort, bPort := client(a), client(b)
			open := func(port int) net.Conn {
				t.Helper()
				c, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Write([]byte{5, 1, 0})
				resp := make([]byte, 2)
				if _, err := io.ReadFull(c, resp); err != nil {
					t.Fatal(err)
				}
				req := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 0}
				wirebinary.BigEndian.PutUint16(req[8:], uint16(targetPort))
				_, _ = c.Write(req)
				reply := make([]byte, 10)
				if _, err := io.ReadFull(c, reply); err != nil || reply[1] != 0 {
					t.Fatalf("socks connect: %v %v", reply, err)
				}
				greet := make([]byte, 5)
				if _, err := io.ReadFull(c, greet); err != nil || string(greet) != "ready" {
					t.Fatalf("tunnel greeting: %q %v", greet, err)
				}
				_ = c.SetDeadline(time.Time{})
				return c
			}
			a1, a2, b1 := open(aPort), open(aPort), open(bPort)
			defer a1.Close()
			defer a2.Close()
			defer b1.Close()
			cli, _ := NewCLIUserAdder(binary, "127.0.0.1:"+itoa(apiPort))
			serverPayload, _ := json.Marshal(server)
			if err := cli.RevokeUsers(ctx, Generator{Definition: JSONDefinition{Raw: serverPayload}}, []Client{a}); err != nil {
				t.Fatal(err)
			}
			for _, c := range []net.Conn{a1, a2} {
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, err := c.Read(make([]byte, 1))
				if err == nil {
					t.Fatal("revoked mux stream remains open")
				}
				if e, ok := err.(net.Error); ok && e.Timeout() {
					t.Fatal("revoked mux stream stalled instead of closing")
				}
			}
			_ = b1.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = b1.Write([]byte("alive"))
			data := make([]byte, 5)
			if _, err := io.ReadFull(b1, data); err != nil || string(data) != "alive" {
				t.Fatalf("other XHTTP user interrupted: %q %v", data, err)
			}
		})
	}
}
