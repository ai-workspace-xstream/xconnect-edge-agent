#!/usr/bin/env bash
# restartCommand for the e2e run: stands in for `systemctl restart xray`.
# fail-next-restart makes one restart fail after stopping Xray (the unit is
# down, as with a failed systemctl restart); fail-always keeps failing.
set -euo pipefail
work=$1 xray=$2 port=$3
pidfile=$work/xray.pid
if [ -f "$pidfile" ]; then
  pid=$(cat "$pidfile")
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  rm -f "$pidfile"
fi
if [ -f "$work/fail-always" ] || [ -f "$work/fail-next-restart" ]; then
  rm -f "$work/fail-next-restart"
  echo "simulated unit failure" >&2
  exit 1
fi
"$xray" run -config "$work/xray/config.json" >>"$work/xray.log" 2>&1 &
pid=$!
echo "$pid" >"$pidfile"
for _ in $(seq 100); do
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then exit 0; fi
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
echo "xray did not start" >&2
exit 1
