# jp-xconnect.svc.plus edge-agent 上线方案（待审阅）

状态：**仅为方案，未执行**。取得明确的节点部署授权后才执行；授权前只允许第 1 步的只读核对。

范围：把 edge-agent 升级到包含“候选配置先校验、原子替换、重启失败回滚、多目标共享快照、
本地/控制面 tuning 分别校验”的已评审版本。本次**不**调整 `bufferSizeKB` 和上传分片限制，
它们按第 8 步单独压测后再决定。

约定：

- `$SHA`：评审通过并合入 `main` 的提交，部署前固定，全程不变。
- `$TS`：本次操作时间戳，例如 `20261015-0300`。
- 所有命令以 root 执行。**不要 `cat`、`grep` 输出或复制 agent 配置里的令牌**；涉及配置文件的
  命令只读取 `xray:` 段或用 `install` 原样拷贝。

## 1. 只读核对（实际值填入下表，与预期不符则停止）

```sh
uname -m                                   # 预期 aarch64（t4g）
systemctl list-units --type=service --all | grep -Ei 'xconnect|agent-svc-plus|agent-proxy|xray|caddy'
systemctl show -p FragmentPath,ExecStart,User,WorkingDirectory xconnect-edge-agent
systemctl show -p ExecStart,User,Environment xray xray-tcp
/usr/local/bin/xconnect-edge-agent -v
/usr/local/bin/xray version
sha256sum /usr/local/bin/xconnect-edge-agent /usr/local/bin/xray
# 只看 xray 段，不输出 agent 段（含令牌）
sed -n '/^xray:/,$p' /etc/agent/account-agent.yaml
ls -l /usr/local/etc/xray/ /usr/local/etc/xray/templates/ /usr/local/share/xray/
sha256sum /usr/local/etc/xray/*.json /usr/local/etc/xray/templates/*.json
# 校验命令在节点上可用（不带 XRAY_LOCATION_ASSET，Xray 会回落到 /usr/local/share/xray）
/usr/local/bin/xray run -test -config /usr/local/etc/xray/config.json
/usr/local/bin/xray run -test -config /usr/local/etc/xray/tcp-config.json
```

| 项目 | 预期（setup-proxy.sh 默认） | 实际 |
| --- | --- | --- |
| agent 服务名 | `xconnect-edge-agent`（旧名 `agent-svc-plus` / `agent-proxy`） | |
| ExecStart | `/usr/local/bin/xconnect-edge-agent -config /etc/agent/account-agent.yaml` | |
| target | `xhttp`、`tcp` | |
| outputPath | `/usr/local/etc/xray/config.json`、`/usr/local/etc/xray/tcp-config.json` | |
| templatePath | `/usr/local/etc/xray/templates/xray.{xhttp,tcp}.template.json` | |
| restartCommand | `systemctl restart xray.service` / `xray-tcp.service` | |
| validateCommand | 现为 `[]` | |
| 运行版本 | `-v` 输出 | |
| 输出文件权限 | `0644`（xray 以 `nobody` 运行，需可读） | |

两条 `xray run -test` 必须都输出 `Configuration OK`；任一失败先排查，不进入后续步骤。

## 2. 构建 arm64 候选

在构建机执行，不在节点上编译：

```sh
git fetch origin && git checkout --detach "$SHA"
go test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o dist/xconnect-edge-agent-linux-arm64 ./cmd/agent
sha256sum dist/xconnect-edge-agent-linux-arm64 | tee dist/SHA256SUMS
```

在构建机用与节点相同版本的 Xray 运行端到端检查，全部 PASS 才继续：

```sh
XRAY=/path/to/xray tests/e2e/xray_upload/run.sh
```

它用真实 Xray 验证候选校验、重启失败恢复、agent 重启采用、首次部署失败清理，并经 XHTTP（stream-up、packet-up）上传 JPEG 校验完整性。

不要使用仓库中 `deploy/ansible/roles/xconnect_edge_agent/files/` 下已提交的二进制，它不对应 `$SHA`。
把二进制和 `SHA256SUMS` 传到节点 `/root/rollout-$TS/`，在节点上复核哈希。

## 3. 备份

```sh
B=/var/backups/xconnect-edge-agent/$TS
install -d -m 700 "$B"
install -m 755 /usr/local/bin/xconnect-edge-agent "$B/xconnect-edge-agent"
install -m 600 /etc/agent/account-agent.yaml "$B/account-agent.yaml"
cp -a /usr/local/etc/xray/templates "$B/templates"
cp -a /usr/local/etc/xray/config.json /usr/local/etc/xray/tcp-config.json "$B/"
cp -a "$(systemctl show -p FragmentPath --value xconnect-edge-agent)" "$B/"
( cd "$B" && sha256sum xconnect-edge-agent *.json templates/*.json > SHA256SUMS )
```

## 4. 配置变更（审阅后执行）

1. 在两个 target 上加入候选校验命令。用补丁只改 `xray:` 段，变更前后用 `sed -n '/^xray:/,$p'` 对比：

   ```yaml
   validateCommand: ["/usr/local/bin/xray", "run", "-test", "-config", "{config}"]
   ```

2. 模板：用 `diff` 比较节点模板与 `$SHA` 的 `config/xray.*.template.json`。
   本次统一后的默认策略是 `loglevel: warning`，XHTTP 模板保留 JP 节点现行的 `bufferSize: 512` 基线。节点模板有现场修改时
   （证书路径、地址、出站、路由、TLS）保留现场值，只同步日志级别；不确定的差异先停下确认。
3. 不新增 `tuning`，保持现有运行参数。

## 5. 切换

建议在低峰时段执行。新版本首次启动时节点上还没有 `.config.json.applied` 记录，
会重启 xray 和 xray-tcp 各一次，现有连接会断开重连；之后 agent 再重启不会再重启 Xray。

```sh
install -m 755 /root/rollout-$TS/xconnect-edge-agent-linux-arm64 /usr/local/bin/xconnect-edge-agent
systemctl restart xconnect-edge-agent
```

## 6. 健康检查

```sh
systemctl is-active xconnect-edge-agent xray xray-tcp caddy
journalctl -u xconnect-edge-agent --since "-10 min" --no-pager | grep -Ei 'synchroniz|candidate|restor|fail|error'
ls -l /usr/local/etc/xray/.config.json.applied /usr/local/etc/xray/.tcp-config.json.applied
ls -a /usr/local/etc/xray/ | grep candidate || echo "no leftover candidates"
stat -c '%a %U %n' /usr/local/etc/xray/config.json /usr/local/etc/xray/tcp-config.json   # 权限与备份一致
ss -lxp | grep xray.sock
systemctl show -p ActiveEnterTimestamp xray xray-tcp
```

再重启一次 agent，确认 `ActiveEnterTimestamp` 不变（采用已应用配置，不重启 Xray）：

```sh
systemctl restart xconnect-edge-agent && sleep 15
systemctl show -p ActiveEnterTimestamp xray xray-tcp
```

控制面侧确认该节点状态上报 `healthy`，`syncRevision` 与控制面一致。

## 7. 客户端验证

- iOS 与 macOS 各一台设备连接 jp 节点，确认连接、断开、重连正常。
- 连续上传 5 张以上 5–10 MB 图片，记录耗时与失败次数，对比上线前基线。
- 同时观察节点：`journalctl -u xray -f`、`top -p "$(pgrep -d, xray)"`、`systemctl show -p MemoryCurrent xray`。
- 本次不修改 `xhttpScMaxEachPostBytes`；客户端与服务端都保持 1000000。

## 8. 回滚

任一检查失败或客户端上传明显变差时执行：

```sh
B=/var/backups/xconnect-edge-agent/$TS
systemctl stop xconnect-edge-agent
install -m 755 "$B/xconnect-edge-agent" /usr/local/bin/xconnect-edge-agent
install -m 600 "$B/account-agent.yaml" /etc/agent/account-agent.yaml
rm -rf /usr/local/etc/xray/templates && cp -a "$B/templates" /usr/local/etc/xray/templates
cp -a "$B/config.json" "$B/tcp-config.json" /usr/local/etc/xray/
rm -f /usr/local/etc/xray/.config.json.applied /usr/local/etc/xray/.tcp-config.json.applied
rm -f /usr/local/etc/xray/.*.candidate-*.json
/usr/local/bin/xray run -test -config /usr/local/etc/xray/config.json
/usr/local/bin/xray run -test -config /usr/local/etc/xray/tcp-config.json
systemctl restart xray xray-tcp
systemctl start xconnect-edge-agent
/usr/local/bin/xconnect-edge-agent -v
( cd "$B" && sha256sum -c SHA256SUMS )
```

然后重复第 6 步检查与第 7 步上传验证。

## 9. 后续：bufferSizeKB 压测（单独审批）

`bufferSizeKB` 是每个连接的缓冲容量，不是内存上限；调大可能提高上传吞吐，也会提高繁忙连接的
最坏内存。本次保持 JP 节点现行的 512 基线不变；之后以 512 为对照，按 4（arm64 上的 Xray 默认）、
128、1024 等档位通过本地 `xray.sync.tuning` 或控制面 tuning 下发，
每档在 5–10 个并发客户端下记录吞吐、xray RSS 与峰值、CPU、上传失败、重连次数，再决定采用值。
上传分片限制按 Accounts 的跨端发布顺序调整：提高限制先改服务端再发客户端，降低限制先改客户端并处理旧缓存。

## 10. 独立安全任务：服务令牌轮换

历史提交中的 deploy bundle 曾包含已填写的服务令牌。该项不等待本次上线，单独执行：
核验受影响令牌与使用方，经受控渠道生成并分发新令牌，确认所有使用方（各节点 agent、
xray-exporter 等）已切换并健康后再撤销旧令牌。令牌值不写入 PR、日志、命令行历史或本文档。
