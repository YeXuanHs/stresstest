//go:build linux

// udpflood_sendmmsg_linux.go - 使用 sendmmsg() 批量发送UDP包
//
// 原理：普通 Write() 每次只发一个包，就要陷入一次内核（syscall）。
// sendmmsg() 允许一次系统调用发送多个包（一个 batch），大幅减少
// 用户态<->内核态切换次数，在小包高频场景下能显著提升pps。
//
// 只在 Linux 下编译生效；其他平台走 udpflood_sendmmsg_other.go 的
// 兼容实现（退化为普通逐包发送）。
package main

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const sysSendmmsg = 307 // SYS_SENDMMSG on linux/amd64 和 linux/arm64

type iovecT struct {
	Base *byte
	Len  uint64
}

type msghdrT struct {
	Name       *byte
	Namelen    uint32
	Iov        *iovecT
	Iovlen     uint64
	Control    *byte
	Controllen uint64
	Flags      int32
}

type mmsghdrT struct {
	Hdr msghdrT
	Len uint32
}

// udpFloodBatchWorker 使用 sendmmsg 一次系统调用发送 batchSize 个包，
// 持续无限速发送，专注打满pps。仅用于长期socket（非churn）模式。
func udpFloodBatchWorker(ctx context.Context, wg *sync.WaitGroup, id int, batchSize int) {
	defer wg.Done()
	payload := randomPayload(*floodSize)

	raddr, err := net.ResolveUDPAddr("udp", *target)
	if err != nil {
		atomic.AddInt64(&totalErrors, 1)
		return
	}
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

	if batchSize < 1 {
		batchSize = 1
	}

	iov := iovecT{Base: &payload[0], Len: uint64(len(payload))}
	batch := make([]mmsghdrT, batchSize)
	for i := range batch {
		batch[i] = mmsghdrT{
			Hdr: msghdrT{
				Iov:    &iov,
				Iovlen: 1,
			},
		}
	}

	rc, err := conn.SyscallConn()
	if err != nil {
		atomic.AddInt64(&totalErrors, 1)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		werr := rc.Write(func(fd uintptr) bool {
			n, _, errno := syscall.Syscall6(
				sysSendmmsg,
				fd,
				uintptr(unsafe.Pointer(&batch[0])),
				uintptr(len(batch)),
				0, 0, 0,
			)
			if errno == syscall.EAGAIN {
				return false // 让netpoller等待socket可写后重试
			}
			if errno != 0 {
				atomic.AddInt64(&totalErrors, 1)
				return true
			}
			atomic.AddInt64(&totalPackets, int64(n))
			atomic.AddInt64(&totalBytes, int64(n)*int64(len(payload)))
			return true
		})
		if werr != nil {
			atomic.AddInt64(&totalErrors, 1)
		}
	}
}
