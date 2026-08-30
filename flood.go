// flood.go - TCP flood 极限压测模式
//
// 与 tcp 模式的区别：
//  1. 默认使用小包（64字节），关闭 Nagle 合并（TCP_NODELAY），
//     目标是打满 pps（每秒包数），而不是打满带宽——很多设备/服务端
//     真正的瓶颈是内核处理包的速率，不是带宽。
//  2. 完全不限速（tcp模式的 -rate 限速器在这里不生效）。
//  3. 支持 -flood-churn：每次都重新三次握手建立新连接、写一次就关闭，
//     用来压测服务端 accept() 连接建立/销毁的速率上限（仍然是真实握手，
//     不涉及伪造源IP或半连接攻击）。
//
// 用法:
//   ./stresstest -mode flood -target x.x.x.x:9000 -workers 128
//   ./stresstest -mode flood -target x.x.x.x:9000 -workers 256 -flood-size 32
//   ./stresstest -mode flood -target x.x.x.x:9000 -workers 200 -flood-churn
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	floodSize    = flag.Int("flood-size", 64, "flood模式每次发送的payload大小（字节），默认给小包以最大化pps")
	floodNoDelay = flag.Bool("flood-nodelay", true, "flood模式(tcp)是否关闭Nagle算法（TCP_NODELAY），关闭后每次Write都会立即发出，pps更高")
	floodChurn   = flag.Bool("flood-churn", false, "flood模式(tcp)是否每次都重新建立连接（写一次就关闭），用于压测连接建立/销毁速率而非单连接吞吐")
	floodProto   = flag.String("flood-proto", "tcp", "flood模式使用的协议: tcp | udp | http")
	floodBatch   = flag.Int("flood-batch", 64, "UDP flood长连接模式下用sendmmsg批量发送的包数(仅linux生效，设为1退化为普通逐包发送)")
	floodHTTPPath = flag.String("flood-http-path", "/", "HTTP flood请求的路径")
	floodTLSSkipVerify = flag.Bool("flood-tls-skip-verify", false, "HTTPS flood时是否跳过证书校验（测试自签名证书时用）")
)

func floodWorker(ctx context.Context, wg *sync.WaitGroup, id int) {
	defer wg.Done()
	payload := randomPayload(*floodSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := net.DialTimeout("tcp", *target, 3*time.Second)
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(*floodNoDelay)
		}

		if *floodChurn {
			n, werr := conn.Write(payload)
			if werr != nil {
				atomic.AddInt64(&totalErrors, 1)
			} else {
				atomic.AddInt64(&totalBytes, int64(n))
				atomic.AddInt64(&totalPackets, 1)
			}
			conn.Close()
			continue
		}

		func() {
			defer conn.Close()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				n, werr := conn.Write(payload)
				if werr != nil {
					atomic.AddInt64(&totalErrors, 1)
					return
				}
				atomic.AddInt64(&totalBytes, int64(n))
				atomic.AddInt64(&totalPackets, 1)
			}
		}()
	}
}

// ---------- UDP flood worker ----------
// 与普通udp模式的区别：不限速、默认小包、专注打满pps。
// -flood-churn 在udp下表现为"每次都重新创建socket再发一个包"，
// 用于测服务端处理新源端口/新五元组流量的能力（比如conntrack表压力）。
func udpFloodWorker(ctx context.Context, wg *sync.WaitGroup, id int) {
	defer wg.Done()
	payload := randomPayload(*floodSize)

	raddr, err := net.ResolveUDPAddr("udp", *target)
	if err != nil {
		log.Fatalf("解析UDP目标地址失败: %v", err)
	}

	if *floodChurn {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			conn, derr := net.DialUDP("udp", nil, raddr)
			if derr != nil {
				atomic.AddInt64(&totalErrors, 1)
				continue
			}
			n, werr := conn.Write(payload)
			if werr != nil {
				atomic.AddInt64(&totalErrors, 1)
			} else {
				atomic.AddInt64(&totalBytes, int64(n))
				atomic.AddInt64(&totalPackets, 1)
			}
			conn.Close()
		}
	}

	// 长期复用一个socket，持续无限速发送，专注打满pps
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		atomic.AddInt64(&totalErrors, 1)
		return
	}
	defer conn.Close()

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
		n, werr := conn.Write(payload)
		if werr != nil {
			atomic.AddInt64(&totalErrors, 1)
			continue
		}
		atomic.AddInt64(&totalBytes, int64(n))
		atomic.AddInt64(&totalPackets, 1)
	}
}
