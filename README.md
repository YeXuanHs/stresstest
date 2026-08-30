# stresstest 多协议压力测试工具

支持 TCP / UDP / HTTP / Minecraft / Flood / ICMP 的高并发压测工具，支持分布式多机压测。

## 功能特性

- 多协议支持：TCP / UDP / HTTP / Minecraft / Flood / ICMP
- 分布式架构：主控 + 被控模式，支持多机同时发压
- CLI 交互菜单：实时监控、任务下发、停止控制
- 实时统计：500ms 刷新间隔，查看吞吐、包速率、错误数
- 密码保护：Agent 连接需密码验证

## 架构说明

| 角色 | 说明 |
|------|------|
| **主控 (master)** | 管理被控，下发任务，CLI 菜单操作 |
| **被控 (agent)** | 连接主控，执行发包任务 |
| **主控+被控 (both)** | 管理被控，自己也参与发包 |

## 快速开始

```bash
# 下载（从 GitHub Releases）
# 或自行编译
go build -o stresstest .

# 首次运行（自动进入主控+被控模式）
./stresstest

# 或指定模式
./stresstest master    # 仅主控
./stresstest agent     # 仅被控
./stresstest both      # 主控+被控
```

## CLI 菜单

手动运行 `./stresstest master` 或 `./stresstest both` 进入交互式菜单：

```
╔══════════════════════════════════════════╗
║         压力测试工具 - 控制面板           ║
╠══════════════════════════════════════════╣
║  [1] 查看 Agent 列表                     ║
║  [2] 实时监控（单台）                     ║
║  [3] 实时监控（总量）                     ║
║  [4] 下发任务                            ║
║  [5] 停止任务                            ║
║  [0] 退出                                ║
╚══════════════════════════════════════════╝
```

### 实时监控

选择一台 Agent 后，每 500ms 刷新显示：

```
=== 实时监控: 甲骨文G口1 ===
吞吐: 1.23 MB/s (0.001 GB/s)
包速率: 950 pkt/s
累计错误: 0
运行时间: 5m30s

按 Enter 返回菜单...
```

### systemd 模式

以服务方式运行时（`./stresstest run --port 8443`），自动检测无终端，仅运行 WebSocket 服务器，不启动 CLI 菜单。

## 单机模式参数

```bash
# TCP 压测
./stresstest -mode tcp -target 192.168.1.100:9000 -workers 64 -size 65536

# UDP 压测
./stresstest -mode udp -target 192.168.1.100:9001 -workers 128 -size 1400

# HTTP 压测
./stresstest -mode http -target http://192.168.1.100:8080/upload -workers 32 -duration 60s

# Minecraft - 200个玩家登录挂机
./stresstest -mode mc -mc-action login -target 10.0.0.5:25565 -workers 200

# Flood TCP - 打满pps
./stresstest -mode flood -target 10.0.0.5:9000 -workers 128 -flood-size 64

# Flood UDP - sendmmsg批量发送
./stresstest -mode flood -flood-proto udp -target 10.0.0.5:9000 -workers 128 -flood-batch 64

# ICMP ping flood
./stresstest -mode icmp -target 10.0.0.5 -workers 64
```

### 通用参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-mode` | tcp / udp / http / mc / flood / icmp | tcp |
| `-target` | 目标地址 | 必填 |
| `-workers` | 并发数 | CPU核数×4 |
| `-size` | payload大小（字节） | 65536 |
| `-rate` | 限速 MB/s，0不限速 | 0 |
| `-duration` | 运行时长，0为持续 | 0 |
| `-stats` | 统计间隔 | 1s |

### Flood 模式参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-flood-size` | payload大小（字节） | 64 |
| `-flood-proto` | tcp / udp / http | tcp |
| `-flood-nodelay` | 关闭Nagle | true |
| `-flood-churn` | 每次重新建连 | false |
| `-flood-batch` | UDP sendmmsg批量数 | 64 |

### ICMP 模式参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-icmp-size` | ICMP payload大小 | 56 |
| `-icmp-rate-pps` | 限速 pps，0不限速 | 0 |
| `-icmp-timeout` | Reply超时 | 2s |

### Minecraft 模式参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-mc-action` | login / status | login |
| `-mc-protocol` | 协议版本号 | 763 |
| `-mc-username-prefix` | 用户名前缀 | stress |
| `-mc-keepalive` | 是否保活 | true |
| `-mc-reconnect` | 断线重连 | true |

## 分布式压测

1. 在主控机器运行 `./stresstest both`
2. 在被控机器配置 `.env` 并运行 `./stresstest agent`
3. 主控 CLI 菜单中选择「下发任务」，选择 Agent 和参数

### .env 配置（被控）

```env
MODE=agent
MASTER_HOST=主控IP
MASTER_PORT=8443
MASTER_TOKEN=密码
AGENT_NAME=自定义名称
```

## Minecraft 模式限制

1. 必须关闭正版验证：`online-mode=false`
2. KeepAlive 包ID是版本相关的，如连接频繁掉线请调整 `-mc-keepalive-serverbound-id`

## 关于高吞吐

单机能否达到 2GB/s 取决于：

1. **网卡带宽**：需要万兆（10G）及以上网卡
2. **CPU核数**：workers 建议设为 CPU 核数的 2~8 倍
3. **分布式**：单机不够可多台机器同时发压
4. **协议选择**：UDP 比 TCP 更容易跑高吞吐
5. **payload 大小**：UDP 建议 1200~1400 字节

## 注意事项

- 请仅对自己拥有或已获得明确授权的目标进行压测
- UDP 模式下发送成功不代表对方收到，需在目标端统计

## 编译

```bash
# Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o stresstest .

# Windows
GOOS=windows GOARCH=amd64 go build -o stresstest.exe .

# macOS
GOOS=darwin GOARCH=arm64 go build -o stresstest .
```
