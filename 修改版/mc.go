// mc.go - Minecraft 服务器压测模式
//
// 模拟真实 MC 客户端行为：TCP连接 -> Handshake -> (Status 或 Login)
// login 模式下登录成功后会持续响应服务端 KeepAlive 包以维持连接，
// 从而真实占用服务器的一个"在线玩家"连接位，而不只是打字节流量。
//
// 已支持自动 zlib 压缩：当服务器发送 Set Compression (0x03) 时自动启用。
//
// 局限性说明：
//  1. 不支持在线模式（正版验证/加密）。测试前需将目标服务器设为
//     offline-mode=true（离线模式/破解模式），否则登录会在 Encryption
//     Request 阶段失败。
//  2. 不同 MC 版本的协议号(protocol version)不同，可通过 -mc-protocol
//     指定，参考 https://wiki.vg/Protocol_version_numbers
package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	mcActiveConns          int64
	mcLoginOK              int64
	mcLoginFailed          int64
	mcKeepAlives           int64
	mcStatusOK             int64
	mcCompressionThreshold int32 = -1 // -1 = 未启用压缩，由服务器 Set Compression 自动设置
)

// ---------- VarInt 编解码 ----------

func writeVarInt(buf *bytes.Buffer, value int32) {
	uv := uint32(value)
	for {
		b := byte(uv & 0x7F)
		uv >>= 7
		if uv != 0 {
			b |= 0x80
		}
		buf.WriteByte(b)
		if uv == 0 {
			break
		}
	}
}

func readVarInt(r io.Reader) (int32, error) {
	var result int32
	var numRead int
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		value := int32(b[0] & 0x7F)
		result |= value << (7 * numRead)
		numRead++
		if numRead > 5 {
			return 0, fmt.Errorf("VarInt 长度超出限制")
		}
		if b[0]&0x80 == 0 {
			break
		}
	}
	return result, nil
}

func decodeVarIntFromBytes(b []byte) (int32, int) {
	var result int32
	var numRead int
	for numRead < len(b) {
		cur := b[numRead]
		value := int32(cur & 0x7F)
		result |= value << (7 * numRead)
		numRead++
		if cur&0x80 == 0 {
			break
		}
		if numRead > 5 {
			break
		}
	}
	return result, numRead
}

func writeMCString(buf *bytes.Buffer, s string) {
	writeVarInt(buf, int32(len(s)))
	buf.WriteString(s)
}

// ---------- zlib 压缩 / 解压 ----------

func zlibCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func zlibDecompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func varIntSize(v int32) int {
	uv := uint32(v)
	n := 0
	for {
		n++
		uv >>= 7
		if uv == 0 {
			break
		}
	}
	return n
}

// ---------- 发送完整 Minecraft 数据包（自动支持压缩） ----------

func sendMCPacket(conn net.Conn, packetID int32, data []byte) error {
	var body bytes.Buffer
	writeVarInt(&body, packetID)
	body.Write(data)
	bodyBytes := body.Bytes()

	var full bytes.Buffer

	if mcCompressionThreshold >= 0 && int32(len(bodyBytes)) >= mcCompressionThreshold {
		compressed, err := zlibCompress(bodyBytes)
		if err != nil {
			return err
		}
		writeVarInt(&full, int32(len(compressed))+int32(varIntSize(int32(len(bodyBytes)))))
		writeVarInt(&full, int32(len(bodyBytes)))
		full.Write(compressed)
	} else {
		writeVarInt(&full, int32(len(bodyBytes)))
		full.Write(bodyBytes)
	}

	_, err := conn.Write(full.Bytes())
	return err
}

// ---------- 读取完整数据包（自动解压） ----------

func readMCPacket(r *bufio.Reader) (int32, []byte, error) {
	length, err := readVarInt(r)
	if err != nil {
		return 0, nil, err
	}
	if length <= 0 || length > 5*1024*1024 {
		return 0, nil, fmt.Errorf("非法包长度: %d", length)
	}

	packetBytes := make([]byte, length)
	if _, err := io.ReadFull(r, packetBytes); err != nil {
		return 0, nil, err
	}

	if mcCompressionThreshold >= 0 {
		dataLen, n := decodeVarIntFromBytes(packetBytes)
		if dataLen == 0 {
			id, n2 := decodeVarIntFromBytes(packetBytes[n:])
			return id, packetBytes[n+n2:], nil
		}
		compressed := packetBytes[n:]
		decompressed, err := zlibDecompress(compressed)
		if err != nil {
			return 0, nil, fmt.Errorf("zlib 解压失败: %v", err)
		}
		id, n2 := decodeVarIntFromBytes(decompressed)
		return id, decompressed[n2:], nil
	}

	id, n := decodeVarIntFromBytes(packetBytes)
	return id, packetBytes[n:], nil
}

// ---------- 拆分 host:port ----------

func splitHostPort(target string) (string, uint16) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return target, 25565
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		p = 25565
	}
	return host, uint16(p)
}

// ---------- Status ----------

func mcStatusOnce(conn net.Conn, host string, port uint16, protocol int32) error {
	var hs bytes.Buffer
	writeVarInt(&hs, protocol)
	writeMCString(&hs, host)
	binary.Write(&hs, binary.BigEndian, port)
	writeVarInt(&hs, 1)
	if err := sendMCPacket(conn, 0x00, hs.Bytes()); err != nil {
		return err
	}
	if err := sendMCPacket(conn, 0x00, nil); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	id, payload, err := readMCPacket(r)
	if err != nil {
		return err
	}
	if id != 0x00 {
		return fmt.Errorf("非预期的响应包ID: 0x%02x", id)
	}
	_ = payload

	var pingBuf bytes.Buffer
	binary.Write(&pingBuf, binary.BigEndian, time.Now().UnixMilli())
	if err := sendMCPacket(conn, 0x01, pingBuf.Bytes()); err != nil {
		return err
	}
	_, _, err = readMCPacket(r)
	return err
}

// ---------- Login + Hold ----------

func mcLoginAndHold(ctx context.Context, conn net.Conn, host string, port uint16, protocol int32, username string, keepAlive bool) error {
	mcCompressionThreshold = -1 // 每个连接独立重置

	var hs bytes.Buffer
	writeVarInt(&hs, protocol)
	writeMCString(&hs, host)
	binary.Write(&hs, binary.BigEndian, port)
	writeVarInt(&hs, 2)
	if err := sendMCPacket(conn, 0x00, hs.Bytes()); err != nil {
		return err
	}

	var loginBuf bytes.Buffer
	writeMCString(&loginBuf, username)
	if err := sendMCPacket(conn, 0x00, loginBuf.Bytes()); err != nil {
		return err
	}

	r := bufio.NewReader(conn)

	for {
		id, payload, err := readMCPacket(r)
		if err != nil {
			return err
		}

		switch id {
		case 0x00:
			reason, _ := decodeMCString(payload)
			return fmt.Errorf("被服务器拒绝: %s", reason)
		case 0x01:
			return fmt.Errorf("目标服务器为在线模式(online-mode)，需设置 offline-mode=true")
		case 0x03: // Set Compression —— 自动启用
			threshold, err := readVarInt(bytes.NewReader(payload))
			if err != nil {
				return fmt.Errorf("解析压缩阈值失败: %v", err)
			}
			mcCompressionThreshold = threshold
			continue
		case 0x02: // Login Success
			atomic.AddInt64(&mcLoginOK, 1)
			goto play
		default:
			return fmt.Errorf("登录阶段收到未知包ID: 0x%02x", id)
		}
	}

play:
	if !keepAlive {
		return nil
	}

	atomic.AddInt64(&mcActiveConns, 1)
	defer atomic.AddInt64(&mcActiveConns, -1)

	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			conn.SetReadDeadline(time.Now())
		case <-watchDone:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		pid, ppayload, err := readMCPacket(r)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if len(ppayload) == 8 {
			_ = sendMCPacket(conn, int32(*mcKeepAliveServerboundID), ppayload)
			atomic.AddInt64(&mcKeepAlives, 1)
		}
		_ = pid
	}
}

func decodeMCString(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	_, n := decodeVarIntFromBytes(b)
	if n >= len(b) {
		return "", nil
	}
	return string(b[n:]), nil
}

// ---------- MC worker ----------

func mcWorker(ctx context.Context, wg *sync.WaitGroup, id int) {
	defer wg.Done()
	host, port := splitHostPort(*target)
	serverAddr := *mcServerAddress
	if serverAddr == "" {
		serverAddr = host
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := net.DialTimeout("tcp", *target, 5*time.Second)
		if err != nil {
			atomic.AddInt64(&totalErrors, 1)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
		}

		if *mcAction == "status" {
			err = mcStatusOnce(conn, serverAddr, port, int32(*mcProtocol))
			if err != nil {
				atomic.AddInt64(&totalErrors, 1)
			} else {
				atomic.AddInt64(&mcStatusOK, 1)
				atomic.AddInt64(&totalPackets, 1)
			}
			conn.Close()
			if !*mcReconnect {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}

		username := fmt.Sprintf("%s%d", *mcUsernamePrefix, id)
		if strings.HasPrefix(username, "_") {
			username = "u" + username
		}
		err = mcLoginAndHold(ctx, conn, serverAddr, port, int32(*mcProtocol), username, *mcKeepAlive)
		conn.Close()
		if err != nil {
			atomic.AddInt64(&mcLoginFailed, 1)
			atomic.AddInt64(&totalErrors, 1)
		}
		if !*mcReconnect {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}
