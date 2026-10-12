// stresstest - 多协议高吞吐压力测试工具
// 支持 TCP / UDP / HTTP / MC / Flood / ICMP 模式，可指定并发数、包大小、目标速率
//
// 用法示例：
//   go build -o stresstest .
//
//   安装并启动服务（首次运行）:
//   ./stresstest
//
//   主控模式 (管理被控，不参与发包):
//   ./stresstest master
//
//   被控模式 (连接主控，执行发包):
//   ./stresstest agent
//
//   主控+被控模式 (管理被控，自己也参与发包):
//   ./stresstest both
//
//   卸载服务:
//   ./stresstest uninstall
//
//   查看服务状态:
//   ./stresstest status
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	mode        = flag.String("mode", "tcp", "压测模式: tcp | udp | http | mc | mcpe | flood | icmp")
	target      = flag.String("target", "", "目标地址。tcp/udp/mc 形如 host:port，http 形如 http://host:port/path")
	workers     = flag.Int("workers", runtime.NumCPU()*4, "并发worker数（每个worker独立连接/goroutine）")
	payloadSize = flag.Int("size", 65536, "每次发送的payload大小（字节，仅tcp/udp/http模式用）")
	rateMBs     = flag.Float64("rate", 0, "全局限速，单位 MB/s，0 表示不限速（尽力打满，仅tcp/udp/http模式用）")
	duration    = flag.Duration("duration", 0, "运行时长，0 表示一直运行直到 Ctrl+C")
	statsEvery  = flag.Duration("stats", time.Second, "统计输出间隔")
	httpMethod  = flag.String("http-method", "POST", "HTTP 模式使用的方法")
	udpConnMode = flag.Bool("udp-connected", true, "UDP 是否使用已连接socket（性能更好）")
	httpTLSSkipVerify = flag.Bool("http-tls-skip-verify", false, "HTTP模式请求https目标时是否跳过证书校验（测试自签名证书时用）")

	// ---- mc 模式专用参数 ----
	mcAction          = flag.String("mc-action", "login", "mc模式子动作: login（模拟玩家登录并挂机）| status（仅查服状态，测查询并发）")
	mcProtocol        = flag.Int("mc-protocol", 763, "Minecraft协议版本号，参考 https://wiki.vg/Protocol_version_numbers（763≈1.20.1）")
	mcServerAddress   = flag.String("mc-server-address", "", "握手包中的服务器地址字段，默认取target的host部分（一般无需改动，除非服务端按此字段做虚拟主机路由）")
	mcUsernamePrefix  = flag.String("mc-username-prefix", "stress", "模拟玩家用户名前缀，实际用户名为 前缀+worker序号")
	mcKeepAlive       = flag.Bool("mc-keepalive", true, "登录成功后是否持续响应KeepAlive包以维持连接（模拟真实在线玩家）")
	mcReconnect       = flag.Bool("mc-reconnect", true, "断线/登录失败后是否自动重连重试")
	mcKeepAliveServerboundID = flag.Int("mc-keepalive-serverbound-id", 0x12, "Serverbound KeepAlive包的ID，随协议版本变化，需要按目标版本核对（参考 wiki.vg/Protocol）")
)

var (
	totalBytes   int64
	totalPackets int64
	totalErrors  int64
)

func randomPayload(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// ---------- 限速器：令牌桶，按全局字节数控制 ----------
type limiter struct {
	enabled    bool
	bytesPerSec int64
	mu          sync.Mutex
	tokens      int64
	last        time.Time
}

func newLimiter(mbPerSec float64) *limiter {
	if mbPerSec <= 0 {
		return &limiter{enabled: false}
	}
	return &limiter{
		enabled:     true,
		bytesPerSec: int64(mbPerSec * 1024 * 1024),
		tokens:      int64(mbPerSec * 1024 * 1024),
		last:        time.Now(),
	}
}

func (l *limiter) wait(n int) {
	if !l.enabled {
		return
	}
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.last).Seconds()
		l.last = now
		l.tokens += int64(elapsed * float64(l.bytesPerSec))
		if l.tokens > l.bytesPerSec {
			l.tokens = l.bytesPerSec
		}
		if l.tokens >= int64(n) {
			l.tokens -= int64(n)
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
}

// ---------- 单实例锁：避免重复启动多个进程 ----------
// lockInstance 使用 flock 独占锁定 lockPath。
// 若已有进程持有锁，返回 false；成功持有则返回 true。
func lockInstance(lockPath string) bool {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		// 无权创建锁文件时不阻断运行
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	// 记录 PID，便于排查
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	// 故意不 Close：文件描述符保持打开，进程退出时锁自动释放
	return true
}

// ---------- TCP worker ----------
func tcpWorker(ctx context.Context, wg *sync.WaitGroup, id int, lim *limiter) {
	defer wg.Done()
	payload := randomPayload(*payloadSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := net.DialTimeout("tcp", *target, 3*time.Second)
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// 加大发送缓冲区，减少系统调用次数
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetWriteBuffer(4 << 20)
			tc.SetNoDelay(false) // 允许 Nagle 合并小包，提升吞吐
		}

		// 持续发送，出错时立即关闭连接
		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			default:
			}
			lim.wait(len(payload))
			n, err := conn.Write(payload)
			if err != nil {
				conn.Close() // 立即关闭废弃连接
				atomic.AddInt64(&totalErrors, 1)
				break // 跳出内层循环，重新建立连接
			}
			atomic.AddInt64(&totalBytes, int64(n))
			atomic.AddInt64(&totalPackets, 1)
		}
	}
}

// ---------- UDP worker ----------
func udpWorker(ctx context.Context, wg *sync.WaitGroup, id int, lim *limiter) {
	defer wg.Done()
	payload := randomPayload(*payloadSize)

	raddr, err := net.ResolveUDPAddr("udp", *target)
	if err != nil {
		log.Fatalf("解析UDP目标地址失败: %v", err)
	}

	var conn *net.UDPConn
	if *udpConnMode {
		conn, err = net.DialUDP("udp", nil, raddr)
	} else {
		conn, err = net.ListenUDP("udp", nil)
	}
	if err != nil {
		log.Printf("worker %d 创建UDP socket失败: %v", id, err)
		atomic.AddInt64(&totalErrors, 1)
		return
	}
	defer conn.Close()

	// 加大发送缓冲区
	if rc, err := conn.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 8<<20)
		})
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		lim.wait(len(payload))
		var n int
		var err error
		if *udpConnMode {
			n, err = conn.Write(payload)
		} else {
			n, err = conn.WriteToUDP(payload, raddr)
		}
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			continue
		}
		atomic.AddInt64(&totalBytes, int64(n))
		atomic.AddInt64(&totalPackets, 1)
	}
}

// ---------- HTTP worker ----------
func httpWorker(ctx context.Context, wg *sync.WaitGroup, id int, lim *limiter) {
	defer wg.Done()
	payload := randomPayload(*payloadSize)

	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: *httpTLSSkipVerify},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		lim.wait(len(payload))
		req, err := http.NewRequestWithContext(ctx, *httpMethod, *target, bytes.NewReader(payload))
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			continue
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := client.Do(req)
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		atomic.AddInt64(&totalBytes, int64(len(payload)))
		atomic.AddInt64(&totalPackets, 1)
	}
}

// ---------- 统计输出 ----------
func statsReporter(ctx context.Context) {
	ticker := time.NewTicker(*statsEvery)
	defer ticker.Stop()
	var lastBytes, lastPackets int64
	lastTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			e := atomic.LoadInt64(&totalErrors)

			if *mode == "icmp" {
				sent := atomic.LoadInt64(&icmpSent)
				recv := atomic.LoadInt64(&icmpReceived)
				loss := 0.0
				if sent > 0 {
					loss = 100 * float64(sent-recv) / float64(sent)
				}
				avgUs := int64(0)
				if recv > 0 {
					avgUs = atomic.LoadInt64(&icmpRTTSumUs) / recv
				}
				minUs := atomic.LoadInt64(&icmpRTTMinUs)
				maxUs := atomic.LoadInt64(&icmpRTTMaxUs)
				fmt.Printf("[%s] 已发送: %d | 已收到回复: %d | 丢包率: %.2f%% | RTT avg/min/max: %.2f/%.2f/%.2f ms\n",
					now.Format("15:04:05"), sent, recv, loss,
					float64(avgUs)/1000, float64(minUs)/1000, float64(maxUs)/1000)
				continue
			}

			if *mode == "mcpe" {
				active := atomic.LoadInt64(&mcpeActiveConns)
				okLogin := atomic.LoadInt64(&mcpeLoginOK)
				failLogin := atomic.LoadInt64(&mcpeLoginFailed)
				ka := atomic.LoadInt64(&mcpeKeepAlives)
				statusOK := atomic.LoadInt64(&mcpeStatusOK)
				hs := atomic.LoadInt64(&mcpeHandshakes)
				encSess := atomic.LoadInt64(&mcpeEncryptedSess)
				if *mcpeAction == "status" {
					fmt.Printf("[%s] 基岩版查询成功: %d | RakNet握手: %d | 加密会话: %d | 累计错误: %d\n",
						now.Format("15:04:05"), statusOK, hs, encSess, e)
				} else {
					fmt.Printf("[%s] 基岩版挂机连接: %d | 登录成功: %d | 登录失败: %d | KeepAlive响应: %d | 加密会话: %d | 错误: %d\n",
						now.Format("15:04:05"), active, okLogin, failLogin, ka, encSess, e)
				}
				continue
			}

			if *mode == "mc" {
				active := atomic.LoadInt64(&mcActiveConns)
				okLogin := atomic.LoadInt64(&mcLoginOK)
				failLogin := atomic.LoadInt64(&mcLoginFailed)
				ka := atomic.LoadInt64(&mcKeepAlives)
				statusOK := atomic.LoadInt64(&mcStatusOK)
				if *mcAction == "status" {
					fmt.Printf("[%s] 状态查询成功: %d | 累计错误: %d\n",
						now.Format("15:04:05"), statusOK, e)
				} else {
					fmt.Printf("[%s] 当前挂机连接数: %d | 累计登录成功: %d | 累计登录失败: %d | KeepAlive响应: %d | 累计错误: %d\n",
						now.Format("15:04:05"), active, okLogin, failLogin, ka, e)
				}
				continue
			}

			b := atomic.LoadInt64(&totalBytes)
			p := atomic.LoadInt64(&totalPackets)
			elapsed := now.Sub(lastTime).Seconds()
			dBytes := b - lastBytes
			dPackets := p - lastPackets
			mbps := float64(dBytes) / elapsed / 1024 / 1024
			gbps := mbps / 1024
			pps := float64(dPackets) / elapsed

			fmt.Printf("[%s] 吞吐: %.2f MB/s (%.3f GB/s) | 包速率: %.0f pkt/s | 累计错误: %d\n",
				now.Format("15:04:05"), mbps, gbps, pps, e)

			lastBytes, lastPackets, lastTime = b, p, now
		}
	}
}

const asciiBanner = `
 ____  _____ ____  _____ ____ ____ _____ _____ ____ _____
/ ___||_   _|  _ \| ____/ ___/ ___|_   _| ____/ ___|_   _|
\___ \  | | | |_) |  _| \___ \___ \ | | |  _| \___ \ | |
 ___) | | | |  _ <| |___ ___) |__) || | | |___ ___) || |
|____/  |_| |_| \_\_____|____/____/ |_| |_____|____/ |_|
`

func main() {
	// 单实例保护：已有进程在跑时，直接退出而不是重复启动
	// 命令行压测模式(-mode)与 service 安装/卸载不参与加锁
	args := os.Args[1:]
	isDaemonLike := len(args) == 0
	if len(args) > 0 {
		switch args[0] {
		case "master", "agent", "both", "run", "uninstall", "status":
			isDaemonLike = true
		default:
			for _, a := range args {
				if a == "-mode" || strings.HasPrefix(a, "-mode=") {
					isDaemonLike = false
					break
				}
			}
		}
	}
	if isDaemonLike {
		lockPath := filepath.Join(filepath.Dir(getEnvFilePath()), ".stresstest.lock")
		if !lockInstance(lockPath) {
			fmt.Fprintln(os.Stderr, "已有 stresstest 实例在运行，本次启动已取消")
			os.Exit(0)
		}
	}

	// 检查子命令
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "master":
			RunMaster()
			return
		case "agent":
			// 设置 agent 标志
			*agentMode = true
			os.Args = append(os.Args[:1], os.Args[2:]...)
			RunAgent()
			return
		case "both":
			RunBoth()
			return
		case "run":
			// systemd 服务运行模式
			runService()
			return
		case "uninstall":
			uninstallService()
			return
		case "status":
			showServiceStatus()
			return
		}
	}

	// 检查是否有 -mode 参数（命令行模式）
	hasModeFlag := false
	for _, arg := range os.Args[1:] {
		if arg == "-mode" || strings.HasPrefix(arg, "-mode=") {
			hasModeFlag = true
			break
		}
	}

	if hasModeFlag {
		// 命令行模式
		runCommandLine()
		return
	}

	// 没有参数时，安装并启动服务
	installAndStartService()
}

// runCommandLine 命令行模式
func runCommandLine() {
	flag.Usage = func() {
		fmt.Print(asciiBanner)
		fmt.Println("stresstest - 多协议压力测试工具\n")
		fmt.Fprintf(flag.CommandLine.Output(), "用法: %s -mode <tcp|udp|http|mc|flood|icmp> -target <目标> [参数]\n\n参数:\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if *target == "" {
		fmt.Println("必须通过 -target 指定目标地址")
		flag.Usage()
		os.Exit(1)
	}

	fmt.Println("======================================")
	fmt.Printf(" 模式:      %s\n", *mode)
	fmt.Printf(" 目标:      %s\n", *target)
	fmt.Printf(" 并发数:    %d\n", *workers)
	if *mode == "mc" {
		fmt.Printf(" MC动作:    %s\n", *mcAction)
		fmt.Printf(" 协议版本:  %d\n", *mcProtocol)
		if *mcAction == "login" {
			fmt.Printf(" 挂机保活:  %v\n", *mcKeepAlive)
		}
	} else if *mode == "flood" {
		fmt.Printf(" 协议:      %s\n", *floodProto)
		fmt.Printf(" Payload:   %d 字节\n", *floodSize)
		if *floodProto == "tcp" {
			fmt.Printf(" NoDelay:   %v\n", *floodNoDelay)
		}
		fmt.Printf(" 连接方式:  %s\n", map[bool]string{true: "每次重新握手/建socket(churn)", false: "长连接/长期socket持续写"}[*floodChurn])
		fmt.Printf(" 限速:      不限速（flood模式恒为全力压测）\n")
	} else {
		fmt.Printf(" 包大小:    %d 字节\n", *payloadSize)
	}
	if *mode != "flood" && *mode != "mc" {
		if *rateMBs > 0 {
			fmt.Printf(" 限速:      %.2f MB/s\n", *rateMBs)
		} else {
			fmt.Printf(" 限速:      不限速（尽力打满）\n")
		}
	}
	if *duration > 0 {
		fmt.Printf(" 运行时长:  %s\n", *duration)
	} else {
		fmt.Printf(" 运行时长:  一直运行，Ctrl+C 停止\n")
	}
	fmt.Println("======================================")
	fmt.Println("⚠️  请只对你自己拥有或已获得授权的目标进行压测。")
	fmt.Println()

	runtime.GOMAXPROCS(runtime.NumCPU())

	ctx, cancel := context.WithCancel(context.Background())
	if *duration > 0 {
		ctx, cancel = context.WithTimeout(ctx, *duration)
	}
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n收到停止信号，正在退出...")
		cancel()
	}()

	lim := newLimiter(*rateMBs)

	// ICMP 模式需要预先启动 receiver 和 cleanup
	if *mode == "icmp" {
		rfd, rerr := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, 1)
		if rerr != nil {
			fmt.Println("创建ICMP接收socket失败（需要root权限或CAP_NET_RAW）:", rerr)
			os.Exit(1)
		}
		go icmpReceiver(ctx, rfd)
		go icmpCleanup(ctx)
	}

	go statsReporter(ctx)

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		switch *mode {
		case "tcp":
			go tcpWorker(ctx, &wg, i, lim)
		case "udp":
			go udpWorker(ctx, &wg, i, lim)
		case "http":
			go httpWorker(ctx, &wg, i, lim)
		case "mc":
			go mcWorker(ctx, &wg, i)
		case "mcpe":
			go mcpeWorker(ctx, &wg, i)
		case "flood":
			if *floodProto == "udp" {
				if !*floodChurn && *floodBatch > 1 {
					go udpFloodBatchWorker(ctx, &wg, i, *floodBatch)
				} else {
					go udpFloodWorker(ctx, &wg, i)
				}
			} else if *floodProto == "http" {
				go httpFloodWorker(ctx, &wg, i)
			} else {
				go floodWorker(ctx, &wg, i)
			}
		case "icmp":
			go icmpWorker(ctx, &wg, i)
		default:
			log.Fatalf("未知模式: %s（支持 tcp/udp/http/mc/flood/icmp）", *mode)
		}
	}

	wg.Wait()

	if *mode == "icmp" {
		sent := atomic.LoadInt64(&icmpSent)
		recv := atomic.LoadInt64(&icmpReceived)
		loss := 0.0
		if sent > 0 {
			loss = 100 * float64(sent-recv) / float64(sent)
		}
		fmt.Printf("\n压测结束。已发送: %d，已收到回复: %d，丢包率: %.2f%%\n", sent, recv, loss)
		return
	}

	if *mode == "mc" {
		if *mcAction == "status" {
			fmt.Printf("\n压测结束。状态查询成功: %d，总错误: %d\n",
				atomic.LoadInt64(&mcStatusOK), atomic.LoadInt64(&totalErrors))
		} else {
			fmt.Printf("\n压测结束。登录成功: %d，登录失败: %d，KeepAlive响应次数: %d，总错误: %d\n",
				atomic.LoadInt64(&mcLoginOK), atomic.LoadInt64(&mcLoginFailed),
				atomic.LoadInt64(&mcKeepAlives), atomic.LoadInt64(&totalErrors))
		}
		return
	}

	totalGB := float64(atomic.LoadInt64(&totalBytes)) / 1024 / 1024 / 1024
	fmt.Printf("\n压测结束。总发送: %.2f GB，总包数: %d，总错误: %d\n",
		totalGB, atomic.LoadInt64(&totalPackets), atomic.LoadInt64(&totalErrors))
}

const serviceName = "stresstest"
const serviceFile = "/etc/systemd/system/" + serviceName + ".service"

// getServiceTemplate 生成 systemd service 文件内容
// 包含日志限速，避免高频日志把磁盘写满。
func getServiceTemplate(binPath, port string) string {
	return fmt.Sprintf(`[Unit]
Description=StressTest - 多协议压力测试工具
After=network.target

[Service]
Type=simple
ExecStart=%s run --port %s
Restart=on-failure
RestartSec=10

# 日志限速：30 秒内最多 200 条，超出则丢弃，防止刷屏写满磁盘
LogRateLimitIntervalSec=30s
LogRateLimitBurst=200
StandardOutput=journal
StandardError=journal
SyslogIdentifier=stresstest

# 文件描述符上限
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`, binPath, port)
}

// journaldDropIn 生成 journald 配置片段，限制日志总占用磁盘
func journaldDropIn() string {
	return `[Journal]
# 日志最大占用 50M
SystemMaxUse=50M
# 最多保留 7 天
MaxRetentionSec=7day
`
}

// installAndStartService 安装并启动服务
func installAndStartService() {
	fmt.Println(asciiBanner)

	// 检查是否已安装
	if _, err := os.Stat(serviceFile); err == nil {
		fmt.Println("服务已安装，正在显示状态...")
		showServiceStatus()
		return
	}

	// 获取可执行文件路径
	binPath, err := os.Executable()
	if err != nil {
		log.Fatalf("获取可执行文件路径失败: %v", err)
	}

	// 询问端口
	fmt.Print("请输入面板端口 (默认 8443): ")
	var port string
	fmt.Scanln(&port)
	port = strings.TrimSpace(port)
	if port == "" {
		port = "8443"
	}

	// 创建 .env 文件（只写端口，模式由 Web UI 选择）
	envConfig := fmt.Sprintf("PORT=%s\n", port)
	if err := os.WriteFile(".env", []byte(envConfig), 0644); err != nil {
		log.Fatalf("创建 .env 文件失败: %v", err)
	}

	// 创建 systemd service 文件
	serviceDir := filepath.Dir(serviceFile)
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		log.Fatalf("创建服务目录失败: %v", err)
	}
	serviceContent := getServiceTemplate(binPath, port)
	if err := os.WriteFile(serviceFile, []byte(serviceContent), 0644); err != nil {
		log.Fatalf("创建服务文件失败: %v", err)
	}

	// 配置 journald 日志上限（静默，失败不影响主流程）
	journaldDir := "/etc/systemd/journald.conf.d"
	if err := os.MkdirAll(journaldDir, 0755); err == nil {
		if err := os.WriteFile(journaldDir+"/stresstest.conf", []byte(journaldDropIn()), 0644); err == nil {
			c := exec.Command("systemctl", "restart", "systemd-journald")
			c.Stdout = nil
			c.Stderr = nil
			c.Run()
		}
	}

	// 立即清理历史超限日志，回收磁盘
	c2 := exec.Command("journalctl", "--vacuum-size=50M")
	c2.Stdout = nil
	c2.Stderr = nil
	c2.Run()

	// 重载 systemd（静默）
	client := exec.Command("systemctl", "daemon-reload")
	client.Stdout = nil
	client.Stderr = nil
	client.Run()

	// 启用开机自启（静默）
	client = exec.Command("systemctl", "enable", serviceName)
	client.Stdout = nil
	client.Stderr = nil
	client.Run()

	// 启动服务（静默）
	client = exec.Command("systemctl", "start", serviceName)
	client.Stdout = nil
	client.Stderr = nil
	client.Run()

	// 等待服务启动
	time.Sleep(2 * time.Second)

	// 显示状态
	localIP := getLocalIP()
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│  服务安装成功!                               │")
	fmt.Println("│                                             │")
	fmt.Printf("│  面板地址: http://%-26s│\n", localIP+":"+port)
	fmt.Println("│                                             │")
	fmt.Println("│  首次访问面板请选择运行模式                    │")
	fmt.Println("│                                             │")
	fmt.Println("│  常用命令:                                   │")
	fmt.Printf("│    查看状态: systemctl status %-13s│\n", serviceName)
	fmt.Printf("│    查看日志: journalctl -u %-16s│\n", serviceName)
	fmt.Printf("│    停止服务: systemctl stop %-15s│\n", serviceName)
	fmt.Printf("│    启动服务: systemctl start %-14s│\n", serviceName)
	fmt.Printf("│    卸载服务: %s uninstall %-13s│\n", binPath, "")
	fmt.Println("└─────────────────────────────────────────────┘")
	fmt.Println()
}

// showServiceStatus 显示服务状态
func showServiceStatus() {
	fmt.Println()
	executeSystemCmd("systemctl", "status", serviceName, "--no-pager")
	fmt.Println()
}

// uninstallService 卸载服务
func uninstallService() {
	fmt.Println("正在卸载服务...")

	// 停止服务
	executeSystemCmd("systemctl", "stop", serviceName)

	// 禁用开机自启
	executeSystemCmd("systemctl", "disable", serviceName)

	// 删除服务文件
	if err := os.Remove(serviceFile); err != nil && !os.IsNotExist(err) {
		log.Printf("删除服务文件失败: %v", err)
	}

	// 删除日志限制配置与锁文件
	os.Remove("/etc/systemd/journald.conf.d/stresstest.conf")
	if lockPath := filepath.Join(filepath.Dir(getEnvFilePath()), ".stresstest.lock"); lockPath != "" {
		os.Remove(lockPath)
	}
	executeSystemCmd("systemctl", "restart", "systemd-journald")

	// 重载 systemd
	executeSystemCmd("systemctl", "daemon-reload")

	fmt.Println("服务已卸载")
}

// executeSystemCmd 执行系统命令
func executeSystemCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

// runService 服务运行入口
// getLocalIP 获取本机真实 IP（优先公网IP）
func getLocalIP() string {
	// 先尝试获取公网 IP（带超时）
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://ipinfo.io/json")
	if err == nil {
		defer resp.Body.Close()
		var result struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.IP != "" {
			return result.IP
		}
	}
	
	// 降级获取内网 IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func runService() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 确保有端口
	if server.config.Port == "" {
		server.config.Port = "8443"
	}

	addr := "0.0.0.0:" + server.config.Port

	// 如果 MODE 为空（首次安装），默认 both 模式
	if server.config.Mode == "" {
		log.Printf("首次运行，默认以主控+被控模式启动")
		server.config.Mode = "both"
		server.SaveEnv()
	}

	switch server.config.Mode {
	case "master":
		log.Printf("主控模式")
	case "agent":
		log.Printf("被控模式")
	case "both":
		log.Printf("主控+被控模式")
		// 注册本地 Agent
		hostname, _ := os.Hostname()
		localName := hostname + " (本机)"
		if server.config.AgentName != "" {
			localName = server.config.AgentName
		}
		localAgent := &AgentInfo{
			ID:       "local",
			Name:     localName,
			Selected: true,
			Order:    0,
			LastSeen: time.Now(),
			CPU:      runtime.NumCPU(),
		}
		server.agents["local"] = localAgent

		// 定期更新本地 Agent 状态
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				server.agentsMu.Lock()
				if agent, ok := server.agents["local"]; ok {
					agent.LastSeen = time.Now()
					agent.CPU = runtime.NumCPU()
				}
				server.agentsMu.Unlock()
			}
		}()
	default:
		log.Printf("未知模式 %s，使用主控+被控模式", server.config.Mode)
		server.config.Mode = "both"
		server.SaveEnv()
	}

	// 启动 WebSocket 服务器（仅 master 和 both 模式需要）
	if server.config.Mode != "agent" {
		server.startWSServer(addr)
	}

	if server.config.Mode == "agent" {
		// Agent 模式不需要 CLI 菜单，只需连接主控并等待
		log.Printf("被控运行中，等待主控下发任务...")
		if server.config.AgentMasterHost != "" && server.config.AgentMasterPort != "" {
			connectToMaster(server)
		} else {
			log.Fatal("未配置主控地址，请在 .env 中设置 MASTER_HOST 和 MASTER_PORT")
		}
	} else {
		// Master 或 Both 模式
		// 检测是否有终端，有则启动 CLI 菜单，否则只运行 WS 服务器（systemd 模式）
		if isTerminal() {
			server.cliMenu()
		} else {
			log.Printf("无终端模式，仅运行 WebSocket 服务器")
			// 阻塞等待
			select {}
		}
	}
}

// checkAndRunAgent 在 main 里调用：如果是 agent 模式就走这里
func checkAndRunAgent() bool {
	if !*agentMode {
		return false
	}
	// 忽略普通信号，让 agent 自己处理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nAgent 收到退出信号")
		os.Exit(0)
	}()
	runAgent()
	return true
}

// isTerminal 检测 stdin 是否是终端
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
