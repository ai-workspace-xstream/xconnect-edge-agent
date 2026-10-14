package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type processStatus struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	Error   string `json:"error,omitempty"`
}

type statusPayload struct {
	OK        bool                     `json:"ok"`
	Time      string                   `json:"time"`
	Processes map[string]processStatus `json:"processes"`
}

func main() {
	listenAddr := flag.String("listen", envOrDefault("HEALTH_LISTEN_ADDR", ":8080"), "listen address")
	flag.Parse()

	xrayPIDFile := envOrDefault("XRAY_PID_FILE", "/var/run/xconnect-edge-agent/xray.pid")
	xrayTCPPIDFile := envOrDefault("XRAY_TCP_PID_FILE", "/var/run/xconnect-edge-agent/xray-tcp.pid")
	agentPIDFile := envOrDefault("AGENT_PID_FILE", "/var/run/xconnect-edge-agent/agent.pid")
	xraySock := envOrDefault("XRAY_UNIX_SOCKET", "/dev/shm/xray.sock")

	// Reverse proxy to xray XHTTP Unix socket (replaces Caddy reverse_proxy)
	xrayProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "xray-unix"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", xraySock)
			},
		},
	}

	mux := http.NewServeMux()

	// Health (always OK)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"service": "agent-runtime-healthz",
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Readiness (all processes)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		processes := getProcesses(xrayPIDFile, xrayTCPPIDFile, agentPIDFile)
		ready := (processes["xray"].Running || processes["xray-tcp"].Running) && processes["agent"].Running
		code := http.StatusOK
		if !ready {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, statusPayload{
			OK:        ready,
			Time:      time.Now().UTC().Format(time.RFC3339),
			Processes: processes,
		})
	})

	// XHTTP proxy: /split/* → xray Unix socket (replaces Caddy reverse_proxy)
	mux.HandleFunc("/split/", func(w http.ResponseWriter, r *http.Request) {
		xrayProxy.ServeHTTP(w, r)
	})

	// Default: return service info or proxy to xray
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Non-root paths: proxy to xray (XHTTP mode=auto may use various paths)
		if r.URL.Path != "/" {
			xrayProxy.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "xconnect-edge-agent-runtime",
			"node":    envOrDefault("AGENT_ID", "unknown"),
		})
	})

	log.Printf("health+proxy server listening on %s (xray socket: %s)", *listenAddr, xraySock)
	if err := http.ListenAndServe(*listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func getProcesses(xrayPID, xrayTCPPID, agentPID string) map[string]processStatus {
	return map[string]processStatus{
		"xray":     readProcessStatus(xrayPID),
		"xray-tcp": readProcessStatus(xrayTCPPID),
		"agent":    readProcessStatus(agentPID),
	}
}

func envOrDefault(name, fallback string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v
}

func readProcessStatus(pidFile string) processStatus {
	content, err := os.ReadFile(pidFile)
	if err != nil {
		return processStatus{Running: false, Error: "pid file not found"}
	}
	pidStr := strings.TrimSpace(string(content))
	if pidStr == "" {
		return processStatus{Running: false, Error: "pid file empty"}
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return processStatus{Running: false, Error: "pid is not a number"}
	}

	if err := syscall.Kill(pid, 0); err != nil {
		return processStatus{Running: false, PID: pid, Error: err.Error()}
	}
	return processStatus{Running: true, PID: pid}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
