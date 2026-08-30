// icmp.go - ICMP echo (ping) 压测模式
//
// 原理和系统自带的 ping -f（flood ping）一致：发送真实的 ICMP Echo
// Request，源IP由内核根据出口网卡自动填写（本工具不使用 IP_HDRINCL，
// 也不提供任何自定义源IP的选项，所以不可能伪造来源，这点和 tcp/udp/http
// 模式的原则完全一致）。
//
// 需要 root 权限或 CAP_NET_RAW capability 才能打开 raw socket，
// 这和系统 ping 命令的权限要求一样（ping命令本身也是靠setuid-root
// 或capability实现的）。
//
// 统计说明：
//   - RTT（往返时延）和丢包率是通过给每个包打上唯一(ID,Seq)标记，
//     记录发送时间，收到匹配的Echo Reply后计算差值得到的，类似
//     ping命令的做法。
//   - 因为是单个raw socket接收本机所有到达的ICMP包，如果压测期间
//     系统上还有其他程序也在ping同一个目标，可能会有极少量误差，
//     属于可接受的近似值。
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	icmpSize      = flag.Int("icmp-size", 56, "ICMP echo请求的payload大小（字节），默认56字节（与标准ping一致）")
	icmpRatePPS   = flag.Int("icmp-rate-pps", 0, "ICMP模式限速，单位包/秒(pps)，0表示不限速（尽力打满，类似ping -f）")
	icmpTimeout   = flag.Duration("icmp-timeout", 2*time.Second, "等待Echo Reply的超时时间，超时未收到视为丢包")

	icmpSent      int64
	icmpReceived  int64
	icmpRTTSumUs  int64 // 累计RTT（微秒），配合icmpReceived计算平均值
	icmpRTTMinUs  int64 = -1
	icmpRTTMaxUs  int64

	icmpPendingMu  sync.Mutex
	icmpPending    = make(map[uint32]time.Time) // key: uint32(id)<<16 | seq
	icmpBaseID     = uint16(os.Getpid() & 0x7fff)
)

func icmpChecksum(b []byte) uint16 {
	var sum uint32
	n := len(b)
	for i := 0; i+1 < n; i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if n%2 == 1 {
		sum += uint32(b[n-1]) << 8
	}
	for sum>>16 > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func buildICMPEcho(id, seq uint16, payload []byte) []byte {
	pkt := make([]byte, 8+len(payload))
	pkt[0] = 8 // Type: Echo Request
	pkt[1] = 0 // Code
	binary.BigEndian.PutUint16(pkt[4:6], id)
	binary.BigEndian.PutUint16(pkt[6:8], seq)
	copy(pkt[8:], payload)
	cs := icmpChecksum(pkt)
	binary.BigEndian.PutUint16(pkt[2:4], cs)
	return pkt
}

func pendingKey(id, seq uint16) uint32 {
	return uint32(id)<<16 | uint32(seq)
}

// icmpReceiver 用一个共享的raw socket持续读取所有到达本机的ICMP包，
// 匹配Echo Reply并计算RTT。
func icmpReceiver(ctx context.Context, fd int) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		syscall.SetNonblock(fd, true)
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == syscall.EAGAIN {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			continue
		}
		if n < 28 { // IP头(至少20) + ICMP头(8)
			continue
		}
		ihl := int(buf[0]&0x0f) * 4
		if n < ihl+8 {
			continue
		}
		icmpType := buf[ihl]
		if icmpType != 0 { // 0 = Echo Reply
			continue
		}
		id := binary.BigEndian.Uint16(buf[ihl+4 : ihl+6])
		seq := binary.BigEndian.Uint16(buf[ihl+6 : ihl+8])
		key := pendingKey(id, seq)

		icmpPendingMu.Lock()
		sentAt, ok := icmpPending[key]
		if ok {
			delete(icmpPending, key)
		}
		icmpPendingMu.Unlock()

		if ok {
			rtt := time.Since(sentAt).Microseconds()
			atomic.AddInt64(&icmpReceived, 1)
			atomic.AddInt64(&icmpRTTSumUs, rtt)
			for {
				old := atomic.LoadInt64(&icmpRTTMinUs)
				if old != -1 && old <= rtt {
					break
				}
				if atomic.CompareAndSwapInt64(&icmpRTTMinUs, old, rtt) {
					break
				}
			}
			for {
				old := atomic.LoadInt64(&icmpRTTMaxUs)
				if old >= rtt {
					break
				}
				if atomic.CompareAndSwapInt64(&icmpRTTMaxUs, old, rtt) {
					break
				}
			}
		}
	}
}

// icmpCleanup 定期清理超时未收到回复的pending记录（视为丢包，防止内存无限增长）
func icmpCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			icmpPendingMu.Lock()
			for k, t := range icmpPending {
				if now.Sub(t) > *icmpTimeout {
					delete(icmpPending, k)
				}
			}
			icmpPendingMu.Unlock()
		}
	}
}

func icmpWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, 1 /* IPPROTO_ICMP */)
	if err != nil {
		fmt.Println("创建raw socket失败（需要root权限或CAP_NET_RAW）:", err)
		atomic.AddInt64(&totalErrors, 1)
		return
	}
	defer syscall.Close(fd)

	ipaddr, err := net.ResolveIPAddr("ip4", *target)
	if err != nil {
		fmt.Println("解析目标地址失败:", err)
		atomic.AddInt64(&totalErrors, 1)
		return
	}
	var sa syscall.SockaddrInet4
	copy(sa.Addr[:], ipaddr.IP.To4())

	id := (icmpBaseID + uint16(workerID)) & 0x7fff
	payload := randomPayload(*icmpSize)

	var interval time.Duration
	if *icmpRatePPS > 0 {
		interval = time.Second / time.Duration(*icmpRatePPS)
	}

	var seq uint16
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		seq++
		pkt := buildICMPEcho(id, seq, payload)

		icmpPendingMu.Lock()
		icmpPending[pendingKey(id, seq)] = time.Now()
		icmpPendingMu.Unlock()

		if err := syscall.Sendto(fd, pkt, 0, &sa); err != nil {
			atomic.AddInt64(&totalErrors, 1)
		} else {
			atomic.AddInt64(&icmpSent, 1)
			atomic.AddInt64(&totalPackets, 1)
			atomic.AddInt64(&totalBytes, int64(len(pkt)))
		}

		if interval > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}
}
