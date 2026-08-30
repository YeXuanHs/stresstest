// stresstest - 多协议高吞吐压力测试工具
// 支持 TCP / UDP / HTTP 三种模式，可指定并发数、包大小、目标速率
//
// 用法示例：
//   go build -o stresstest main.go
//
//   TCP 模式:
//   ./stresstest -mode tcp -target 127.0.0.1:9000 -workers 64 -size 65536
//
//   UDP 模式:
//   ./stresstest -mode udp -target 127.0.0.1:9001 -workers 128 -size 1400
//
//   HTTP 模式 (POST 大 body):
//   ./stresstest -mode http -target http://127.0.0.1:8080/upload -workers 32 -size 65536
//
//   限速到 500MB/s (总量):
//   ./stresstest -mode tcp -target x.x.x.x:9000 -rate 500
//
//   限时 30 秒后自动停止:
//   ./stresstest -mode tcp -target x.x.x.x:9000 -duration 30s
//
//   Minecraft 模式 (模拟玩家登录并维持连接):
//   ./stresstest -mode mc -target 127.0.0.1:25565 -workers 200 -mc-action login
//
//   Minecraft 模式 (仅查服状态，测查询并发):
//   ./stresstest -mode mc -target 127.0.0.1:25565 -workers 500 -mc-action status
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	mode        = flag.String("mode", "tcp", "压测模式: tcp | udp | http | mc | flood | icmp")
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

		func() {
			defer conn.Close()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				lim.wait(len(payload))
				n, err := conn.Write(payload)
				if err != nil {
					atomic.AddInt64(&totalErrors, 1)
					return
				}
				atomic.AddInt64(&totalBytes, int64(n))
				atomic.AddInt64(&totalPackets, 1)
			}
		}()
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
		fmt.Printf(" Payload:   %d 字节\n", *payloadSize)
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

	if *mode == "icmp" {
		rfd, rerr := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, 1)
		if rerr != nil {
			fmt.Println("创建ICMP接收socket失败（需要root权限或CAP_NET_RAW）:", rerr)
			os.Exit(1)
		}
		go icmpReceiver(ctx, rfd)
		go icmpCleanup(ctx)
	}

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

	go statsReporter(ctx)

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
