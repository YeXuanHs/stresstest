// httpflood.go - HTTP flood 极限QPS压测模式
//
// 和现有 http 模式（用 net/http.Client）不同，这里手写最简 HTTP/1.1
// 请求和响应解析，跳过标准库的一些通用开销（Header映射分配、
// Transport连接池调度等），目的是把QPS往上顶。
//
// 现实提醒：HTTP请求-响应这套流程本质上比裸TCP/UDP慢得多——
// 每次至少要走 写请求 -> 服务端处理 -> 读状态行 -> 读响应头 -> 读body
// 这一整套流程，不可能达到 sendmmsg 批量发UDP包那种量级。这里只是
// 尽量优化，不代表能追平 flood tcp/udp 的pps。
//
// 局限性：目前只正确处理 Content-Length 响应，不支持 chunked
// transfer-encoding（遇到chunked编码的响应会解析出错，计入错误计数）。
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 预先构建好HTTP请求的原始字节，避免每次请求都重新拼接字符串
func buildHTTPFloodRequest(host, path string, keepAlive bool) []byte {
	conn := "close"
	if keepAlive {
		conn = "keep-alive"
	}
	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: %s\r\nUser-Agent: stresstest-flood\r\n\r\n",
		path, host, conn,
	)
	return []byte(req)
}

// 读取一个HTTP响应：状态行 + 头部 + (按Content-Length丢弃body)
// 不支持 chunked，遇到没有 Content-Length 且非0长度的响应会报错。
func readHTTPFloodResponse(r *bufio.Reader) error {
	// 状态行
	statusLine, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.") {
		return fmt.Errorf("非法状态行: %q", statusLine)
	}

	contentLength := -1
	chunked := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break // 头部结束
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "content-length:") {
			v := strings.TrimSpace(trimmed[len("content-length:"):])
			if n, perr := strconv.Atoi(v); perr == nil {
				contentLength = n
			}
		} else if strings.HasPrefix(lower, "transfer-encoding:") && strings.Contains(lower, "chunked") {
			chunked = true
		}
	}

	if chunked {
		return fmt.Errorf("响应使用chunked编码，本工具暂不支持解析")
	}

	if contentLength > 0 {
		if _, err := r.Discard(contentLength); err != nil {
			return err
		}
	}
	return nil
}

func httpFloodWorker(ctx context.Context, wg *sync.WaitGroup, id int) {
	defer wg.Done()

	u, err := url.Parse(*target)
	if err != nil || u.Host == "" {
		atomic.AddInt64(&totalErrors, 1)
		return
	}
	useTLS := u.Scheme == "https"
	host := u.Host
	path := *floodHTTPPath
	if path == "/" && u.Path != "" {
		path = u.Path
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
	}
	addr := host
	if !strings.Contains(addr, ":") {
		if useTLS {
			addr = addr + ":443"
		} else {
			addr = addr + ":80"
		}
	}

	dial := func() (net.Conn, error) {
		if useTLS {
			// TLS握手比明文多一次往返，QPS会比http模式低一些，这是协议本身的开销
			return tls.DialWithDialer(
				&net.Dialer{Timeout: 3 * time.Second},
				"tcp", addr,
				&tls.Config{
					InsecureSkipVerify: *floodTLSSkipVerify,
					ServerName:         strings.Split(host, ":")[0],
				},
			)
		}
		return net.DialTimeout("tcp", addr, 3*time.Second)
	}

	keepAlive := !*floodChurn
	reqBytes := buildHTTPFloodRequest(host, path, keepAlive)

	if *floodChurn {
		// 每次都新建连接，发一个请求，读完响应就关闭
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			conn, derr := dial()
			if derr != nil {
				atomic.AddInt64(&totalErrors, 1)
				continue
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				tc.SetNoDelay(true)
			}
			if _, werr := conn.Write(reqBytes); werr != nil {
				atomic.AddInt64(&totalErrors, 1)
				conn.Close()
				continue
			}
			r := bufio.NewReaderSize(conn, 4096)
			if rerr := readHTTPFloodResponse(r); rerr != nil {
				atomic.AddInt64(&totalErrors, 1)
			} else {
				atomic.AddInt64(&totalPackets, 1)
				atomic.AddInt64(&totalBytes, int64(len(reqBytes)))
			}
			conn.Close()
		}
	}

	// keep-alive长连接模式：一条连接上连续发请求，最大化QPS
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, derr := dial()
		if derr != nil {
			atomic.AddInt64(&totalErrors, 1)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
		}

		func() {
			defer conn.Close()
			r := bufio.NewReaderSize(conn, 4096)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, werr := conn.Write(reqBytes); werr != nil {
					atomic.AddInt64(&totalErrors, 1)
					return
				}
				if rerr := readHTTPFloodResponse(r); rerr != nil {
					atomic.AddInt64(&totalErrors, 1)
					return // 连接可能被服务端关闭或不支持keep-alive，重新建连接
				}
				atomic.AddInt64(&totalPackets, 1)
				atomic.AddInt64(&totalBytes, int64(len(reqBytes)))
			}
		}()
	}
}
