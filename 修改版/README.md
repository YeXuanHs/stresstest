# stresstest 多协议压力测试工具

单文件 Go 程序，支持 TCP / UDP / HTTP 三种模式的高并发压测，可限速、限时、实时统计吞吐量。

## 编译

```bash
go build -o stresstest main.go
```

## 用法

```bash
./stresstest -mode <tcp|udp|http> -target <目标> [其他参数]
```

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-mode` | tcp / udp / http | tcp |
| `-target` | 目标地址，tcp/udp 为 `host:port`，http 为完整 URL | 必填 |
| `-workers` | 并发数（每个worker一个连接/goroutine） | CPU核数×4 |
| `-size` | 每次发送的payload大小（字节） | 65536 |
| `-rate` | 全局限速，单位 MB/s，0为不限速 | 0（不限速） |
| `-duration` | 运行时长，如 `30s`、`5m`，0为一直运行 | 0 |
| `-stats` | 统计输出间隔 | 1s |
| `-http-method` | HTTP模式请求方法 | POST |
| `-http-tls-skip-verify` | HTTP模式请求https目标时是否跳过证书校验（测试自签名证书时用） | false |
| `-udp-connected` | UDP是否用已连接socket（性能更优） | true |
| `-mc-action` | mc模式子动作：login（模拟玩家登录并挂机）\| status（仅查服状态） | login |
| `-mc-protocol` | Minecraft协议版本号，参考 wiki.vg/Protocol_version_numbers | 763（≈1.20.1） |
| `-mc-server-address` | 握手包里的服务器地址字段，一般不用改 | target的host部分 |
| `-mc-username-prefix` | 模拟玩家用户名前缀，实际用户名=前缀+worker序号 | stress |
| `-mc-keepalive` | 登录后是否持续响应KeepAlive维持连接 | true |
| `-mc-reconnect` | 断线/失败后是否自动重连 | true |
| `-mc-keepalive-serverbound-id` | Serverbound KeepAlive包ID，随协议版本变化 | 0x12（18） |
| `-flood-size` | flood模式payload大小（字节），小包提高pps | 64 |
| `-flood-proto` | flood模式协议: tcp \| udp | tcp |
| `-flood-nodelay` | flood模式(tcp)是否关闭Nagle算法（TCP_NODELAY） | true |
| `-flood-churn` | flood模式是否每次重新建连（tcp:重新握手；udp:重新建socket；http:不用keep-alive每次新连接） | false |
| `-flood-http-path` | HTTP flood请求的路径 | / |

## 示例

**TCP，64并发，64KB包，一直跑到 Ctrl+C：**
```bash
./stresstest -mode tcp -target 10.0.0.5:9000 -workers 64 -size 65536
```

**UDP，128并发，1400字节包（避免IP分片）：**
```bash
./stresstest -mode udp -target 10.0.0.5:9001 -workers 128 -size 1400
```

**HTTP，32并发，跑60秒：**
```bash
./stresstest -mode http -target http://10.0.0.5:8080/upload -workers 32 -duration 60s
```

**限速到 500MB/s：**
```bash
./stresstest -mode tcp -target 10.0.0.5:9000 -rate 500
```

**Minecraft 模式 - 模拟200个玩家登录并挂机（测试服务器承载多少在线人数）：**
```bash
./stresstest -mode mc -mc-action login -target 10.0.0.5:25565 -workers 200
```

**Minecraft 模式 - 只测查服状态接口的并发能力（不占用玩家位）：**
```bash
./stresstest -mode mc -mc-action status -target 10.0.0.5:25565 -workers 500
```

## Minecraft 模式重要限制

1. **必须关闭正版验证**：目标服务器 `server.properties` 里需要 `online-mode=false`（离线/破解模式），否则会在加密握手阶段失败（本工具不支持正版加密验证，故意如此，因为伪造正版登录本身就不合法）。
2. **必须关闭数据包压缩**：把 `network-compression-threshold` 设为 `-1`。压测结束后记得改回来，否则真实玩家连接也会跳过压缩，增加带宽消耗。
3. **KeepAlive 包ID是版本相关的**：不同MC版本这个ID不一样，代码里用"payload恰好是8字节就当作KeepAlive回应"这种启发式判断，绝大多数版本都能生效；如果发现连接总是几十秒后掉线，去 wiki.vg/Protocol 查一下你目标版本准确的 serverbound Keep Alive 包ID，通过 `-mc-keepalive-serverbound-id` 传进去。
4. 这个模式只是模拟"连接+挂机"来测服务器能扛多少并发在线连接和登录压力，不会真正执行任何游戏内操作（移动、聊天、破坏方块等），所以测不出方块更新、实体AI这类游戏逻辑层面的压力。

## Flood 极限模式

**Flood TCP长连接模式（打满pps，测网络栈处理速率上限）：**
```bash
./stresstest -mode flood -target 10.0.0.5:9000 -workers 128 -flood-size 64
```

**Flood TCP churn模式（每次都重新三次握手，测连接建立/销毁速率上限）：**
```bash
./stresstest -mode flood -target 10.0.0.5:9000 -workers 200 -flood-churn
```

**Flood UDP长期socket模式（打满UDP pps）：**
```bash
./stresstest -mode flood -flood-proto udp -target 10.0.0.5:9001 -workers 128 -flood-size 64
```

**Flood UDP churn模式（每次重新建socket再发一个包，测新五元组/conntrack处理能力）：**
```bash
./stresstest -mode flood -flood-proto udp -flood-churn -target 10.0.0.5:9001 -workers 128
```

说明：
- flood 模式走的是**真实TCP三次握手 / 真实UDP发包**，不涉及任何原始socket伪造包或伪造源IP，本质上和 tcp/udp 模式一样是真实流量，只是去掉了限速、默认用小包（TCP还关闭了Nagle合并），目的是把 pps（包速率）或连接/五元组建立速率打到极限，而不是打满带宽。
- **长连接/长期socket模式**：默认64字节小包、不限速地连续写，测的是内核网络栈单位时间能处理多少个包（很多设备/服务端真正瓶颈是pps不是带宽）。
- **churn模式**：TCP下每次都完整走一遍握手→写一次→关闭，测服务端 `accept()` 循环和连接生命周期能扛住多高频率的新建连接（对应调优 `somaxconn`、`tcp_max_syn_backlog`）；UDP下每次都新建一个socket（相当于用新的本地源端口发包），测的是服务端/中间设备处理"新五元组"流量的能力（比如conntrack表压力、NAT表项创建速率）。
- 因为是真实握手/真实源IP，这个模式没法伪装来源、也没法对着没有权限的目标"打了不被发现"，请只用于自己的基础设施。

## 关于达到 2GB/s

单机吞吐能否达到 2GB/s（约 16Gbps）取决于：

1. **网卡带宽**：普通千兆网卡上限约 125MB/s，需要万兆（10G）及以上网卡才可能达到 2GB/s。
2. **CPU核数**：`-workers` 建议设为 CPU 核数的 2~8 倍，程序会自动用满所有核（`GOMAXPROCS`）。
3. **单机 vs 多机**：如果单机网卡/CPU达不到，可以在多台机器上同时跑本工具打同一个目标，实现分布式叠加流量（这就是主从架构 stresstool 项目所解决的问题——用一个 master 协调多个 agent 一起发压）。
4. **UDP 通常比 TCP 更容易跑高吞吐**（没有握手和拥塞控制开销），但要注意目标端和中间网络设备是否会因为大量UDP包而丢包或限速。
5. **payload 大小**：UDP建议 1200~1400 字节（避免IP分片被丢弃）；TCP/HTTP 用大包（32KB~256KB）减少系统调用次数、提升吞吐效率。

## 注意事项

- 请仅对自己拥有或已获得明确授权的目标进行压测，未经授权对他人服务器发起流量攻击可能触犯法律。
- UDP 模式下没有反馈机制，发送成功不代表对方真的收到了，需要在目标端另外统计接收情况才能验证真实吞吐。
