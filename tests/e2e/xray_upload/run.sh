#!/usr/bin/env bash
# End-to-end check of the agent's config transaction against a real Xray,
# with real image uploads through the XHTTP tunnel it manages.
#
# The agent (built from this checkout) syncs from a mock controller, renders
# config/xray.xhttp.template.json, validates candidates with `xray run -test`
# and restarts a real Xray through restart_xray.sh. A second Xray acts as the
# client and uploads JPEG images over XHTTP (TLS, HTTP/2) in stream-up and
# packet-up mode. The template differs from production only in what a local
# run needs: it listens on 127.0.0.1 with TLS instead of the Caddy-fronted
# unix socket, and drops the geoip:private rule because the upload target is
# on loopback.
#
# Needs: go, xray (XRAY=/path or on PATH), python3 with Pillow, openssl, curl.
# Not run in CI. Usage: tests/e2e/xray_upload/run.sh
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
XRAY=${XRAY:-$(command -v xray || true)}
[ -x "$XRAY" ] || { echo "set XRAY to an xray binary" >&2; exit 2; }
WORK=${WORK:-$(mktemp -d)}
IMAGES=${IMAGES:-6}
PORT_XHTTP=18443 PORT_UPLOAD=18080 PORT_CTRL=18081 PORT_SOCKS=11080
U1=11111111-1111-4111-8111-111111111111
U2=22222222-2222-4222-8222-222222222222
BAD=not-a-uuid-and-much-longer-than-thirty-characters
OUT=$WORK/xray/config.json
FAILS=0
PIDS=()

cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  [ -f "$WORK/xray.pid" ] && kill "$(cat "$WORK/xray.pid")" 2>/dev/null || true
}
trap cleanup EXIT

log() { printf '%s\n' "$*" | tee -a "$WORK/results.txt"; }
check() { # check <description> <command...>
  local what=$1; shift
  if "$@"; then log "PASS  $what"; else log "FAIL  $what"; FAILS=$((FAILS + 1)); fi
}
wait_for() { # wait_for <seconds> <command...>
  local deadline=$((SECONDS + $1)); shift
  until "$@"; do [ $SECONDS -lt $deadline ] || return 1; sleep 0.2; done
}
port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
agent_log_has() { grep -q -- "$1" "$WORK/agent.log"; }
xray_pid() { cat "$WORK/xray.pid" 2>/dev/null || true; }
config_has() { grep -q -- "$1" "$OUT" 2>/dev/null; }
marker() { cat "$WORK/xray/.config.json.applied" 2>/dev/null | tr -d '\n'; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
# Predicates re-read state on every call, so wait_for can poll them.
marker_matches() { [ -f "$OUT" ] && [ "$(marker)" = "$(sha "$OUT")" ]; }
xray_restarted_since() { [ -n "$(xray_pid)" ] && [ "$(xray_pid)" != "$1" ]; }
xray_still() { [ "$(xray_pid)" = "$1" ]; }

set_state() { # set_state <revision> <tuning-json> <client-id>...
  local revision=$1 tuning=$2; shift 2
  python3 - "$WORK/state.json" "$revision" "$tuning" "$@" <<'PY'
import json, sys
path, revision, tuning, ids = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
doc = {"clients": [{"id": i, "email": f"user{n}@e2e.test"} for n, i in enumerate(ids)],
       "total": len(ids), "revision": revision}
if tuning != "null":
    doc["tuning"] = json.loads(tuning)
open(path + ".tmp", "w").write(json.dumps(doc))
PY
  mv "$WORK/state.json.tmp" "$WORK/state.json"
}

start_agent() {
  "$WORK/agent" -config "$WORK/agent.yaml" >>"$WORK/agent.log" 2>&1 &
  AGENT_PID=$!
  PIDS+=("$AGENT_PID")
}

upload_images() { # upload_images <label> <uuid> <mode>
  local label=$1 uuid=$2 mode=$3 ok=0 total=0 seconds=0 bytes=0
  python3 - "$WORK/client.json" "$uuid" "$mode" "$PORT_SOCKS" "$PORT_XHTTP" "$FINGERPRINT" <<'PY'
import json, sys
path, uuid, mode, socks, port, fingerprint = sys.argv[1:]
json.dump({
    "log": {"loglevel": "warning"},
    "inbounds": [{"listen": "127.0.0.1", "port": int(socks), "protocol": "socks", "settings": {"udp": False}}],
    "outbounds": [{
        "protocol": "vless",
        "settings": {"vnext": [{"address": "127.0.0.1", "port": int(port),
                                "users": [{"id": uuid, "encryption": "none"}]}]},
        "streamSettings": {
            "network": "xhttp",
            "security": "tls",
            "tlsSettings": {"serverName": "jp.test", "alpn": ["h2"], "pinnedPeerCertSha256": fingerprint},
            "xhttpSettings": {"path": "/split", "mode": mode},
        },
    }],
}, open(path, "w"))
PY
  "$XRAY" run -config "$WORK/client.json" >>"$WORK/client.log" 2>&1 &
  local client=$!
  wait_for 10 port_open "$PORT_SOCKS" || { kill "$client"; log "FAIL  $label: client did not start"; FAILS=$((FAILS + 1)); return; }
  for img in "$WORK"/images/*.jpg; do
    total=$((total + 1))
    local result
    if result=$(curl -sS --fail --max-time 120 --socks5-hostname "127.0.0.1:$PORT_SOCKS" \
        -H 'Content-Type: image/jpeg' -H 'Expect:' --data-binary "@$img" \
        -w '\n%{time_total}' "http://127.0.0.1:$PORT_UPLOAD/upload" 2>>"$WORK/curl.log"); then
      local body=${result%$'\n'*} took=${result##*$'\n'}
      if [ "$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["sha256"])' "$body")" = "$(sha "$img")" ]; then
        ok=$((ok + 1))
        seconds=$(python3 -c "print($seconds + $took)")
        bytes=$((bytes + $(stat -c %s "$img")))
      fi
    fi
  done
  kill "$client" 2>/dev/null || true
  wait "$client" 2>/dev/null || true
  local rss hwm
  rss=$(awk '/VmRSS/ {print $2}' "/proc/$(xray_pid)/status" 2>/dev/null || echo "?")
  hwm=$(awk '/VmHWM/ {print $2}' "/proc/$(xray_pid)/status" 2>/dev/null || echo "?")
  local rate
  rate=$(python3 -c "print(f'{$bytes / 1048576 / max($seconds, 1e-9):.1f}')")
  seconds=$(python3 -c "print(f'{$seconds:.2f}')")
  if [ "$ok" -eq "$total" ]; then
    log "PASS  $label: $ok/$total images intact, $((bytes / 1048576)) MiB in ${seconds}s (${rate} MiB/s), server RSS ${rss} kB peak ${hwm} kB"
  else
    log "FAIL  $label: $ok/$total images intact"
    FAILS=$((FAILS + 1))
  fi
}

: >"$WORK/results.txt"
log "work dir: $WORK"
log "xray: $("$XRAY" version | head -1)"
mkdir -p "$WORK/xray" "$WORK/templates" "$WORK/images"

# Agent binary from this checkout.
(cd "$ROOT" && go build -o "$WORK/agent" ./cmd/agent)

# Self-signed certificate for jp.test; the client pins it.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 1 \
  -subj /CN=jp.test -addext subjectAltName=DNS:jp.test \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" 2>/dev/null
FINGERPRINT=$(openssl x509 -in "$WORK/cert.pem" -noout -fingerprint -sha256 | cut -d= -f2)

# Production XHTTP template with only the local-run differences applied.
python3 - "$ROOT/config/xray.xhttp.template.json" "$WORK/templates/xray.xhttp.template.json" "$PORT_XHTTP" "$WORK" <<'PY'
import json, sys
src, dst, port, work = sys.argv[1:]
doc = json.load(open(src))
inbound = doc["inbounds"][0]
inbound["listen"], inbound["port"] = "127.0.0.1", int(port)
inbound["streamSettings"]["security"] = "tls"
inbound["streamSettings"]["tlsSettings"] = {
    "alpn": ["h2", "http/1.1"],
    "certificates": [{"certificateFile": f"{work}/cert.pem", "keyFile": f"{work}/key.pem"}],
}
doc.pop("routing", None)
json.dump(doc, open(dst, "w"), indent=2)
PY

cat >"$WORK/agent.yaml" <<YAML
mode: agent
log:
  level: info
agent:
  role: agent-proxy
  id: e2e.jp.test
  domain: jp.test
  controllerUrl: http://127.0.0.1:$PORT_CTRL
  apiToken: e2e-placeholder
  syncInterval: 2s
  statusInterval: 1m
  httpTimeout: 5s
xray:
  sync:
    enabled: true
    targets:
      - name: xhttp
        outputPath: $OUT
        templatePath: $WORK/templates/xray.xhttp.template.json
        validateCommand: ["$XRAY", "run", "-test", "-config", "{config}"]
        restartCommand: ["$HERE/restart_xray.sh", "$WORK", "$XRAY", "$PORT_XHTTP"]
        dynamicUsers:
          enabled: true
          executable: $XRAY
          server: 127.0.0.1:10086
YAML

python3 - "$WORK/images" "$IMAGES" <<'PY'
import os, sys
from PIL import Image
out, count = sys.argv[1], int(sys.argv[2])
for n in range(count):
    # Noise compresses poorly, so each JPEG is several MiB like a phone photo.
    Image.frombytes("RGB", (2400, 1800), os.urandom(2400 * 1800 * 3)).save(f"{out}/photo{n}.jpg", quality=90)
PY
log "images: $IMAGES JPEG, $(du -sm "$WORK/images" | cut -f1) MiB total"

set_state r1 null "$U1"
python3 "$HERE/mock_controller.py" "$WORK/state.json" "$PORT_CTRL" & PIDS+=($!)
python3 "$HERE/upload_server.py" "$PORT_UPLOAD" & PIDS+=($!)
wait_for 10 port_open "$PORT_CTRL"

log "-- 1. first sync: validate, commit, restart"
start_agent
check "xray started from the agent's config" wait_for 30 port_open "$PORT_XHTTP"
check "512 KiB buffer baseline rendered" config_has '"bufferSize": 512'
check "applied marker records the loaded config" wait_for 10 marker_matches
upload_images "stream-up uploads" "$U1" stream-up
upload_images "packet-up uploads" "$U1" packet-up

log "-- 2. client added: HandlerService, no restart"
pid=$(xray_pid)
set_state r2 null "$U1" "$U2"
check "new client written" wait_for 15 config_has "$U2"
check "marker follows the hot addition" wait_for 10 marker_matches
check "xray not restarted for an addition" xray_still "$pid"
upload_images "added client uploads" "$U2" packet-up

log "-- 3. tuning change: validate, commit, restart"
set_state r3 '{"xhttpScMaxEachPostBytes": 2000000, "connIdleSeconds": 300}' "$U1" "$U2"
check "tuning written" wait_for 15 config_has '"scMaxEachPostBytes": 2000000'
check "xray restarted for tuning" wait_for 15 xray_restarted_since "$pid"
check "marker records the restarted config" wait_for 10 marker_matches
upload_images "packet-up after tuning" "$U1" packet-up

log "-- 4. invalid candidate: rejected, live config kept"
pid=$(xray_pid) before=$(sha "$OUT")
set_state r4 '{"xhttpScMaxEachPostBytes": 2000000, "connIdleSeconds": 300}' "$U1" "$U2" "$BAD"
check "candidate rejected by xray -test" wait_for 15 agent_log_has "candidate config rejected"
check "live config unchanged" test "$(sha "$OUT")" = "$before"
check "xray not restarted" xray_still "$pid"
check "no candidate files left" test -z "$(ls -A "$WORK/xray" | grep candidate || true)"
upload_images "uploads while the bad candidate is rejected" "$U2" stream-up
set_state r5 '{"xhttpScMaxEachPostBytes": 2000000, "connIdleSeconds": 300}' "$U1" "$U2"

log "-- 5. restart fails: previous config restored, then retried"
applied=$(sha "$OUT")
touch "$WORK/fail-next-restart"
set_state r6 '{"xhttpScMaxEachPostBytes": 2000000, "connIdleSeconds": 600}' "$U1" "$U2"
check "failure reported with recovery" wait_for 15 agent_log_has "previous config restored and xray restarted"
check "xray back up" wait_for 15 port_open "$PORT_XHTTP"
check "change applied on the next sync" wait_for 15 config_has '"connIdle": 600'
check "marker matches the running config" wait_for 10 marker_matches
upload_images "uploads after recovery" "$U1" stream-up

log "-- 6. agent restart: running config adopted"
pid=$(xray_pid)
kill "$AGENT_PID"; wait "$AGENT_PID" 2>/dev/null || true
start_agent
check "agent adopted the running config" wait_for 15 agent_log_has "xray already runs the desired config"
check "xray not restarted by the agent restart" xray_still "$pid"

log "-- 7. first deploy, restart fails: new config removed"
kill "$AGENT_PID"; wait "$AGENT_PID" 2>/dev/null || true
kill "$(xray_pid)"; rm -f "$WORK/xray.pid" "$OUT" "$WORK/xray/.config.json.applied"
touch "$WORK/fail-always"
start_agent
check "failure reported without a previous config" wait_for 15 agent_log_has "no previous config, new config removed"
check "new config removed" test ! -e "$OUT"
check "no marker for an unproven config" test -z "$(marker)"
rm -f "$WORK/fail-always"
check "next sync deploys" wait_for 20 port_open "$PORT_XHTTP"
upload_images "uploads after the retried deploy" "$U1" packet-up

log "-- result: $FAILS failure(s); logs in $WORK"
exit $((FAILS > 0))
