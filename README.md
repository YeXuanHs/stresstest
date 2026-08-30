# stresstest 多协议压力测试工具

支持 TCP / UDP / HTTP / Minecraft / Flood / ICMP 的高并发压测工具，提供 Web 管理界面，支持分布式多机压测。

## 功能特性

- 多协议支持：TCP / UDP / HTTP / Minecraft / Flood / ICMP
- Web 管理界面：可视化操作，无需命令行
- 分布式架构：主控 + 被控模式，支持多机同时发压
- 实时统计：200ms 刷新间隔，实时查看吞吐、包速率、错误数
- 密码保护：主控面板和 Agent 连接均需密码验证

## 架构说明

| 角色 | 说明 |
|------|------|
| **主控 (master)** | 管理被控，下发任务，不参与发包 |
| **被控 (agent)** | 连接主控，执行发包任务 |
| **主控+被控 (both)** | 管理被控，自己也参与发包 |

## 编译

### 前端构建

```bash
cd web
npm install
npm run build
cd ..
```

### Go 构建

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o stresstest .

# Windows
go build -o stresstest.exe .
```

构建后只有一个可执行文件，前端已嵌入其中。

## 使用方式

### 1. 主控模式

```bash
./stresstest master
```

首次运行会询问面板端口，然后启动 Web 服务：

```
┌─────────────────────────────────────────────┐
│  主控已启动                                   │
│                                             │
│  面板地址: http://0.0.0.0:8443               │
│  被控连接地址: 0.0.0.0:8443                  │
└─────────────────────────────────────────────┘
```

浏览器访问面板，首次使用会引导设置密码。

### 2. 被控模式

```bash
./stresstest agent
```

首次运行会询问面板端口，然后启动 Web 服务。浏览器访问面板，填写主控 IP、端口和密码进行连接。

### 3. 主控+被控模式

```bash
./stresstest both
```

同时作为主控和被控，本机也参与发包。

### 4. 单机模式（无需主控）

```bash
# TCP 压测
./stresstest -mode tcp -target 192.168.1.100:9000 -workers 64 -size 65536

# UDP 压测
./stresstest -mode udp -target 192.168.1.100:9001 -workers 128 -size 1400

# HTTP 压测
./stresstest -mode http -target http://192.168.1.100:8080/upload -workers 32 -duration 60s
```

## Web 管理界面

### 主控面板功能

- 在线 Agent 列表（支持全选/反选）
- 任务配置（模式、目标、并发数、包大小、时长）
- 实时统计（总吞吐、包速率、错误数）
- 修改 Agent 名称
- 修改密码
- 修改端口（自动重启）

### 被控面板功能

- 查看连接状态
- 修改主控连接配置（IP、端口、密码）

## 单机模式参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-mode` | tcp / udp / http / mc / flood / icmp | tcp |
| `-target` | 目标地址 | 必填 |
| `-workers` | 并发数 | CPU核数×4 |
| `-size` | payload大小（字节） | 65536 |
| `-rate` | 限速 MB/s，0不限速 | 0 |
| `-duration` | 运行时长，0为持续 | 0 |
| `-stats` | 统计间隔 | 1s |

### Minecraft 模式参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-mc-action` | login / status | login |
| `-mc-protocol` | 协议版本号 | 763 |
| `-mc-username-prefix` | 用户名前缀 | stress |
| `-mc-keepalive` | 是否保活 | true |

### Flood 模式参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-flood-size` | payload大小（字节） | 64 |
| `-flood-proto` | tcp / udp / http | tcp |
| `-flood-nodelay` | 关闭Nagle | true |
| `-flood-churn` | 每次重新建连 | false |

## 压测示例

### 单机压测

```bash
# TCP，64并发，64KB包
./stresstest -mode tcp -target 10.0.0.5:9000 -workers 64 -size 65536

# UDP，128并发，1400字节包
./stresstest -mode udp -target 10.0.0.5:9001 -workers 128 -size 1400

# HTTP，32并发，60秒
./stresstest -mode http -target http://10.0.0.5:8080/upload -workers 32 -duration 60s

# Minecraft - 200个玩家登录挂机
./stresstest -mode mc -mc-action login -target 10.0.0.5:25565 -workers 200

# Flood TCP - 打满pps
./stresstest -mode flood -target 10.0.0.5:9000 -workers 128 -flood-size 64
```

### 分布式压测

1. 在主控机器运行 `./stresstest both`
2. 在被控机器运行 `./stresstest agent`
3. 浏览器访问主控面板，配置任务，选择 Agent，点击开始

## Minecraft 模式限制

1. 必须关闭正版验证：`online-mode=false`
2. 必须关闭数据包压缩：`network-compression-threshold=-1`
3. KeepAlive 包ID是版本相关的，如连接频繁掉线请调整 `-mc-keepalive-serverbound-id`

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
