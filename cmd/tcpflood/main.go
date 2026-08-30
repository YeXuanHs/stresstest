package main

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

var (
	totalBytes   int64
	totalPackets int64
	totalErrors  int64
)

func tcpWorker(ctx context.Context, wg *sync.WaitGroup, target string, payload []byte, dialer proxy.Dialer) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := dialer.Dial("tcp", target)
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetWriteBuffer(4 << 20)
			tc.SetNoDelay(false)
		}

		func() {
			defer conn.Close()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
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

func stats(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastBytes, lastPackets int64
	lastTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			e := atomic.LoadInt64(&totalErrors)
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

func readLine(reader *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func main() {
	fmt.Println("======================================")
	fmt.Println(" TCP 压测工具")
	fmt.Println("======================================")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// 目标地址
	target := readLine(reader, "请输入目标地址 (host:port): ")
	if target == "" {
		fmt.Println("目标地址不能为空")
		os.Exit(1)
	}

	// 并发数
	workersStr := readLine(reader, "请输入并发数 (默认 9999): ")
	workers := 9999
	if workersStr != "" {
		fmt.Sscanf(workersStr, "%d", &workers)
	}

	// 包大小
	sizeStr := readLine(reader, "请输入包大小 (字节，默认 1450): ")
	size := 1450
	if sizeStr != "" {
		fmt.Sscanf(sizeStr, "%d", &size)
	}

	// SOCKS5 代理
	useProxy := readLine(reader, "是否使用 SOCKS5 代理? (y/n，默认 n): ")

	var dialer proxy.Dialer = proxy.Direct

	if strings.ToLower(useProxy) == "y" {
		proxyAddr := readLine(reader, "请输入代理地址 (ip:port): ")
		if proxyAddr == "" {
			fmt.Println("代理地址不能为空")
			os.Exit(1)
		}

		proxyUser := readLine(reader, "请输入代理用户名 (无用户名直接回车): ")
		proxyPass := readLine(reader, "请输入代理密码 (无密码直接回车): ")

		var auth *proxy.Auth
		if proxyUser != "" || proxyPass != "" {
			auth = &proxy.Auth{
				User:     proxyUser,
				Password: proxyPass,
			}
		}

		var err error
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, auth, proxy.Direct)
		if err != nil {
			fmt.Printf("创建代理失败: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println()
	fmt.Println("======================================")
	fmt.Printf(" 目标: %s\n", target)
	fmt.Printf(" 并发: %d\n", workers)
	fmt.Printf(" 包大小: %d 字节\n", size)
	if strings.ToLower(useProxy) == "y" {
		fmt.Printf(" 代理: 已启用\n")
	} else {
		fmt.Printf(" 代理: 未使用\n")
	}
	fmt.Println("======================================")
	fmt.Println(" Ctrl+C 停止")
	fmt.Println()

	payload := make([]byte, size)
	rand.Read(payload)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go stats(ctx)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go tcpWorker(ctx, &wg, target, payload, dialer)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	fmt.Println("\n正在停止...")
	cancel()
	wg.Wait()

	totalGB := float64(atomic.LoadInt64(&totalBytes)) / 1024 / 1024 / 1024
	fmt.Printf("结束。总发送: %.2f GB，总包数: %d，总错误: %d\n",
		totalGB, atomic.LoadInt64(&totalPackets), atomic.LoadInt64(&totalErrors))
}
