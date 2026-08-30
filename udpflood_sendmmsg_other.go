//go:build !linux

// udpflood_sendmmsg_other.go - 非Linux平台兼容实现
//
// sendmmsg() 是Linux专属系统调用，其他平台（Windows/macOS）没有对应能力，
// 这里退化为普通逐包 Write() 发送，行为与 udpFloodWorker 一致。
package main

import (
	"context"
	"sync"
)

func udpFloodBatchWorker(ctx context.Context, wg *sync.WaitGroup, id int, batchSize int) {
	// 非Linux平台没有sendmmsg，直接复用普通UDP flood实现
	udpFloodWorker(ctx, wg, id)
}
