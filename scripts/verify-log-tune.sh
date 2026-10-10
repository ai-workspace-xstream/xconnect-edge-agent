#!/usr/bin/env bash
# Read-only checks for Caddy, journald, disk and process memory after log tuning.
# Usage: sudo bash verify-log-tune.sh
# Optional: HOST=jp-xconnect.svc.plus JOURNAL_MAX=300M CADDY_RSS_WARN_MB=150
set -uo pipefail

HOST=${HOST:-jp-xconnect.svc.plus}
JOURNAL_MAX=${JOURNAL_MAX:-300M}
KEEP_DAYS=${KEEP_DAYS:-14}
CADDY_RSS_WARN_MB=${CADDY_RSS_WARN_MB:-150}

if [[ -t 1 ]]; then G=$'\e[32m'; Y=$'\e[33m'; R=$'\e[31m'; B=$'\e[1m'; N=$'\e[0m'; else G='' Y='' R='' B='' N=''; fi
PASS=0 WARN=0 FAIL=0
ok()   { echo "  ${G}PASS${N} $*"; PASS=$((PASS+1)); }
warn() { echo "  ${Y}WARN${N} $*"; WARN=$((WARN+1)); }
bad()  { echo "  ${R}FAIL${N} $*"; FAIL=$((FAIL+1)); }
sec()  { echo; echo "${B}[$1]${N}"; }

[[ $EUID -eq 0 ]] || { echo "请用 root 运行"; exit 2; }

sec "1. Caddy 日志配置"
cfg=$(curl -fsS --max-time 3 localhost:2019/config/logging 2>/dev/null || true)
src="admin API"
if [[ -z "$cfg" || "$cfg" == "null" ]]; then
  cfg=$(caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile 2>/dev/null || true)
  src="caddy adapt（文件，未必已加载）"
fi
if python3 -c 'import json,sys; d=json.load(sys.stdin); logs=d.get("logging",d).get("logs",{}); default=logs.get("default",{}); rp=[v for v in logs.values() if "http.handlers.reverse_proxy" in v.get("include",[]) and v.get("level")=="ERROR"]; raise SystemExit(0 if "http.handlers.reverse_proxy" in default.get("exclude",[]) and rp else 1)' <<<"$cfg" 2>/dev/null; then
  ok "reverse_proxy 从默认日志排除且专用级别为 ERROR（来源: $src）"
else
  bad "未找到 reverse_proxy 排除与 ERROR 级别配置（来源: $src）"
fi

sec "2. Caddy WARN 日志"
n5=$(journalctl -u caddy --since "5 min ago" --no-pager 2>/dev/null | grep -c 'aborting with incomplete' || true)
if [[ "${n5:-0}" -eq 0 ]]; then ok "最近 5 分钟 context canceled WARN: 0 条"
else bad "最近 5 分钟仍有 $n5 条 context canceled WARN"; fi
echo "  每分钟日志条数（最近 20 分钟）:"
journalctl -u caddy --since "20 min ago" -o short-iso --no-pager 2>/dev/null \
  | awk 'NF && $1 !~ /^--/ {print substr($1,1,16)}' | uniq -c | sed 's/^/    /' | tail -n 20

sec "3. 代理可用性"
for s in caddy xray; do
  if systemctl is-active -q "$s"; then ok "$s 运行中"; else bad "$s 未运行"; fi
done
socks=$(ss -x 2>/dev/null | grep -c 'xray.sock' || true)
if [[ "${socks:-0}" -gt 0 ]]; then ok "caddy→xray socket 连接: $socks"
else warn "caddy→xray socket 连接为 0（无人在用时正常，可用客户端连一下再测）"; fi
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 \
  --resolve "$HOST:443:127.0.0.1" "https://$HOST/" 2>/dev/null || true)
if [[ "$code" != "000" && -n "$code" ]]; then ok "https://$HOST/ 响应 HTTP $code（TLS 证书校验通过）"
else bad "https://$HOST/ 无响应或证书校验失败"; fi
errs=$(journalctl -u caddy -p err --since "10 min ago" -q --no-pager 2>/dev/null | wc -l)
if [[ "$errs" -eq 0 ]]; then ok "最近 10 分钟 caddy ERROR: 0"
else warn "最近 10 分钟 caddy ERROR: $errs 条，查看: journalctl -u caddy -p err --since '10 min ago'"; fi

sec "4. journald 限额"
jcfg=$(systemd-analyze cat-config systemd/journald.conf 2>/dev/null | grep -E '^(SystemMaxUse|MaxRetentionSec|Compress)=' || true)
if grep -Fxq "SystemMaxUse=$JOURNAL_MAX" <<<"$jcfg" && grep -Fxq "MaxRetentionSec=${KEEP_DAYS}day" <<<"$jcfg"; then
  ok "生效配置包含 SystemMaxUse=$JOURNAL_MAX、MaxRetentionSec=${KEEP_DAYS}day"
else bad "journald 生效配置不符合预期: ${jcfg:-（未找到）}"; fi
used=$(journalctl --disk-usage 2>/dev/null | grep -oE '[0-9.]+[KMGTP]?' | head -n1 || true)
limit=$(numfmt --from=iec "$JOURNAL_MAX" 2>/dev/null || echo 314572800)
bytes=$(numfmt --from=iec "${used:-0}" 2>/dev/null || echo 0)
if [[ "$bytes" -le $((limit * 11 / 10)) ]]; then ok "journal 占用 ${used:-0} ≤ $JOURNAL_MAX"
else bad "journal 占用 ${used:-未知} 超过 $JOURNAL_MAX"; fi

sec "5. 磁盘"
read -r size usedd avail pct <<<"$(df -h / | awk 'NR==2{print $2,$3,$4,$5}')"
p=${pct%%%}
if [[ "${p:-100}" -lt 85 ]]; then ok "/ 已用 $usedd / $size（$pct），可用 $avail"
else warn "/ 已用 ${pct:-未知}，可用 ${avail:-未知}"; fi

sec "6. 内存"
printf "    %-10s %8s %12s\n" COMMAND RSS_MB ELAPSED
ps -o comm=,rss=,etime= -C caddy,vector,xray,systemd-journal 2>/dev/null \
  | awk '{printf "    %-10s %8.1f %12s\n",$1,$2/1024,$3}'
crss=$(ps -o rss= -C caddy 2>/dev/null | awk '{s+=$1} END{print int(s/1024)}')
if [[ "${crss:-0}" -le "$CADDY_RSS_WARN_MB" ]]; then ok "caddy RSS ${crss:-0}MB ≤ ${CADDY_RSS_WARN_MB}MB"
else warn "caddy RSS ${crss}MB，旧进程仍在时可安排重启后观察"; fi
free -m | awk 'NR==2{printf "    内存 total %sM / used %sM / available %sM\n",$2,$3,$7}'

echo
echo "${B}结果: ${G}PASS $PASS${N}  ${Y}WARN $WARN${N}  ${R}FAIL $FAIL${N}"
[[ $FAIL -eq 0 ]]
