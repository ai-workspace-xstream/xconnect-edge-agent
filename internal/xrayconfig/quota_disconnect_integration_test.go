//go:build xrayintegration

package xrayconfig

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestXrayRemoveUserLeavesEstablishedVLESSSessionOpenButRestartClosesIt(t *testing.T) {
	xray, err := exec.LookPath("xray")
	if err != nil {
		t.Skip("xray binary is required; run with xrayintegration build tag")
	}

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetPort := target.Addr().(*net.TCPAddr).Port

	inboundPort := freeTCPPort(t)
	apiPort := freeTCPPort(t)
	const userID = "00000000-0000-4000-8000-000000000001"
	const userEmail = "quota-user@example.test"
	config := map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "warning"},
		"api": map[string]interface{}{"tag": "api", "listen": net.JoinHostPort("127.0.0.1", itoa(apiPort)), "services": []string{"HandlerService"}},
		"inbounds": []interface{}{map[string]interface{}{
			"tag": "test-vless", "listen": "127.0.0.1", "port": inboundPort, "protocol": "vless",
			"settings":       map[string]interface{}{"clients": []interface{}{map[string]interface{}{"id": userID, "email": userEmail}}, "decryption": "none"},
			"streamSettings": map[string]interface{}{"network": "tcp"},
		}},
		"outbounds": []interface{}{map[string]interface{}{"tag": "direct", "protocol": "freedom", "settings": map[string]interface{}{}}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	process := exec.Command(xray, "run", "-c", configPath)
	var processLogs bytes.Buffer
	process.Stdout, process.Stderr = &processLogs, &processLogs
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- process.Wait() }()
	defer func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			<-processDone
		}
	}()
	apiAddress := net.JoinHostPort("127.0.0.1", itoa(apiPort))
	inboundAddress := net.JoinHostPort("127.0.0.1", itoa(inboundPort))
	waitTCP(t, apiAddress)

	targetConnections := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := target.Accept()
		if acceptErr == nil {
			_, _ = conn.Write([]byte("ready")) // Flush Xray's VLESS response header.
			targetConnections <- conn
		}
	}()
	clientConn := openVLESSSession(t, inboundAddress, userID, targetPort, &processLogs)
	defer clientConn.Close()
	ready := make([]byte, len("ready"))
	if _, err := io.ReadFull(clientConn, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("read target greeting: %q, %v", ready, err)
	}
	var targetConn net.Conn
	select {
	case targetConn = <-targetConnections:
	case <-time.After(2 * time.Second):
		t.Fatal("Xray did not connect to the test target")
	}
	defer targetConn.Close()

	remove := exec.Command(xray, "api", "rmu", "--server="+apiAddress, "-tag=test-vless", userEmail)
	output, err := remove.CombinedOutput()
	if err != nil {
		t.Fatalf("xray api rmu: %v: %s", err, output)
	}
	if !bytes.Contains(output, []byte("Removed 1 user(s) in total.")) {
		t.Fatalf("unexpected rmu output: %s", output)
	}

	// A marker sent through the already-authenticated VLESS stream proves rmu did
	// not disconnect the established session.
	marker := []byte("session-survived-rmu")
	if _, err := clientConn.Write(marker); err != nil {
		t.Fatalf("write through established session after rmu: %v", err)
	}
	_ = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	received := make([]byte, len(marker))
	if _, err := io.ReadFull(targetConn, received); err != nil {
		t.Fatalf("established session did not relay data after rmu: %v", err)
	}
	if !bytes.Equal(received, marker) {
		t.Fatalf("unexpected relayed marker: %q", received)
	}

	// New authentication with the removed identity must fail.
	newConn, err := net.DialTimeout("tcp", inboundAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer newConn.Close()
	if _, err := newConn.Write(vlessRequest(userID, targetPort)); err != nil {
		t.Fatalf("write new VLESS request: %v", err)
	}
	_ = newConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	response := make([]byte, 2)
	if _, err := io.ReadFull(newConn, response); err == nil {
		t.Fatal("removed identity authenticated a new VLESS session")
	}

	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-processDone
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := clientConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("established VLESS session remained open after Xray process termination")
	}
}

func openVLESSSession(t *testing.T, address, userID string, targetPort int, processLogs *bytes.Buffer) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(vlessRequest(userID, targetPort)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		conn.Close()
		t.Fatalf("VLESS authentication failed: %v; xray output: %s", err, processLogs.String())
	}
	if response[0] != 0 || response[1] != 0 {
		conn.Close()
		t.Fatalf("unexpected VLESS response header: %v", response)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

func vlessRequest(userID string, targetPort int) []byte {
	uuid := make([]byte, 0, 16)
	parts := []int{8, 4, 4, 4, 12}
	index := 0
	for _, part := range parts {
		for n := 0; n < part; n += 2 {
			var value byte
			for k := 0; k < 2; k++ {
				value <<= 4
				c := userID[index]
				index++
				if c >= '0' && c <= '9' {
					value |= c - '0'
				} else if c >= 'a' && c <= 'f' {
					value |= c - 'a' + 10
				}
			}
			uuid = append(uuid, value)
		}
		if index < len(userID) && userID[index] == '-' {
			index++
		}
	}
	request := []byte{0}
	request = append(request, uuid...)
	request = append(request, 0, 1)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(targetPort))
	request = append(request, port[:]...)
	request = append(request, 1, 127, 0, 0, 1)
	return request
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func waitTCP(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Xray API did not listen on %s", address)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
