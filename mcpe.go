// mcpe.go - Minecraft 基岩版（Bedrock Edition）压测模式
//
// 与 Java 版（mc.go）的根本差异：
//   传输层   Java = TCP            基岩版 = UDP (RakNet)
//   加密     Java = AES-128-CFB8   基岩版 = MCPE encryption (Blowfish-CFB8)
//   序列化   Java = VarInt+长度前缀 基岩版 = RakNet 帧 + Play batch
//
// 因此本文件不复用 mc.go 的连接代码。
//
// 流程（login 动作）：
//  1. 未加密 OfflineMessageDataID0x00 → 取 server GUID
//  2. 未加密 UnconnectedPing → OpenConnectionReply1 → 取服务器真实端口
//  3. 未加密 OpenConnectionRequest2（携带 client public key）
//  4. 未加密等待 ConnectionRequest(0x09)
//  5. 断开，启用 MCPE encryption，重连并重发 ConnectionRequest(0x09)
//  6. Play 层 RequestServerInformation → NetworkSettings
//  7. Play 层 LoginPacket → PlayStatus(成功)
//  8. 挂机循环，响应 KeepAlive / Ping
//
// 局限性：
//   - 仅 IPv4 直连，不做 DNS 多 IP 轮询
//   - Play 层包 ID 按 1.20.x/1.21.x 写死，跨大版本可能需调整
//   - 加密路径需要服务端公钥文件（官方客户端抓包可获得），
//     未提供公钥时自动降级为未加密握手
//   - 不支持 Xbox Live / 在线验证，目标需 offline-mode
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/blowfish"
)

// ---------------- 参数 ----------------

var (
	mcpeProtocol      = flag.Int("mcpe-protocol", 776, "基岩版协议版本号(1.21.4≈776)")
	mcpeAction        = flag.String("mcpe-action", "login", "基岩版子动作: login(登录并挂机) | status(仅查询服状态)")
	mcpeServerAddress = flag.String("mcpe-server-address", "", "RakNet 握手用的服务器地址，默认取 target 的 host 部分")
	mcpeKeyFile       = flag.String("mcpe-key-file", "/tmp/MCRaFT_PE/keys.dat", "服务端公钥文件(前32字节为公钥)")
	mcpeKeepAlive     = flag.Bool("mcpe-keepalive", true, "登录成功后是否响应 KeepAlive/Ping 维持连接")
)

// ---------------- 统计 ----------------

var (
	mcpeActiveConns   int64
	mcpeLoginOK       int64
	mcpeLoginFailed   int64
	mcpeKeepAlives    int64
	mcpeStatusOK      int64
	mcpeHandshakes    int64
	mcpeEncryptedSess int64
)

// ---------------- RakNet 常量 ----------------

var raknetMagic = []byte{
	0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe,
	0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78,
}

const (
	idOfflineMessageDataID0x00 = 0x00
	idUnconnectedPing          = 0x01
	idUnconnectedPingOpenConn  = 0x02
	idOpenConnectionReply1     = 0x06
	idOpenConnectionRequest2   = 0x07
	idOpenConnectionReply2     = 0x08
	idConnectionRequest        = 0x09
	idIncompatibleProtocol     = 0x19
	idUnconnectedPong          = 0x1c
	idAlreadyConnected         = 0x12
	idConnectionFailed         = 0x13
)

// Play 层包 ID（1.20.x / 1.21.x）
const (
	playRequestServerInfo          = 0x00
	playRequestInfoV2              = 0xc1
	playLogin                      = 0x01
	playPlayStatus                 = 0x02
	playDisconnect                 = 0x05
	playResourcePackInfo           = 0x06
	playResourcePackStack          = 0x07
	playResourcePackClientResponse = 0x08
	playNetworkSettings            = 0x8f
	playSetCompression             = 0x0b
	playKeepAlive                  = 0x00
	playPing                       = 0x02
	playPong                       = 0x03
)

// ---------------- Blowfish CFB8（标准库无此模式） ----------------

// blockCipher8 抽象出 Blowfish 的 8 字节分组接口
type blockCipher8 interface {
	Encrypt(dst, src []byte)
	BlockSize() int
}

// cfb8 是 8 位反馈模式：每字节密文回灌 IV。
type cfb8 struct {
	block blockCipher8
	iv    [8]byte
}

func newCFB8(b blockCipher8, iv []byte) *cfb8 {
	c := &cfb8{block: b}
	copy(c.iv[:], iv)
	return c
}

// encCFB8 加密：密文回灌
func (c *cfb8) encryptBlock(dst, src []byte) {
	blk := make([]byte, 8)
	copy(blk, c.iv[:])
	c.block.Encrypt(blk, blk)
	for j := 0; j < 8; j++ {
		dst[j] = src[j] ^ blk[j]
	}
	copy(c.iv[:], dst[:8])
}

// decCFB8 解密：收到的密文回灌 IV
func (c *cfb8) decryptBlock(dst, src []byte) {
	blk := make([]byte, 8)
	copy(blk, c.iv[:])
	c.block.Encrypt(blk, blk)
	for j := 0; j < 8; j++ {
		dst[j] = src[j] ^ blk[j]
	}
	copy(c.iv[:], src[:8])
}

func (c *cfb8) crypt(dst, src []byte, decrypt bool) {
	for i := 0; i+8 <= len(src); i += 8 {
		if decrypt {
			c.decryptBlock(dst[i:i+8], src[i:i+8])
		} else {
			c.encryptBlock(dst[i:i+8], src[i:i+8])
		}
	}
}

// mcpePubkey MCPE 的 pubkey 运算：Blowfish(serverPublicKey) 作密钥做 CFB8，
// 结果与输入异或。data 长度须为 8 的倍数（通常 32）。
func mcpePubkey(data, serverPublicKey []byte) []byte {
	block, err := blowfish.NewCipher(serverPublicKey)
	if err != nil {
		return nil
	}
	iv := make([]byte, 8)
	copy(iv, serverPublicKey[:8])

	c := newCFB8(block, iv)
	tmp := make([]byte, len(data))
	c.crypt(tmp, data, false)

	out := make([]byte, len(data))
	for i := range data {
		out[i] = data[i] ^ tmp[i]
	}
	return out
}

// mcpeEncryption 基岩版会话加密器（收发各一份 IV 状态）
type mcpeEncryption struct {
	enc *cfb8
	dec *cfb8
}

func mcpeNewEncryption(token []byte) (*mcpeEncryption, error) {
	block, err := blowfish.NewCipher(token)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 8)
	copy(iv, token[:8])

	// 收发方向需各自独立的 IV 状态，但初始值相同
	blk2, _ := blowfish.NewCipher(token)
	return &mcpeEncryption{
		enc: newCFB8(block, iv),
		dec: newCFB8(blk2, iv),
	}, nil
}

// ---------------- RakNet 帧 ----------------

// raknetFrame 封装：0x80|v [|0x40] + sessionID(2) + msgIndex(2) + body
func raknetFrame(packetID byte, body []byte, encrypted bool) []byte {
	flags := byte(0x80) | packetID
	if encrypted {
		flags |= 0x40
	}
	out := make([]byte, 0, 5+len(body))
	out = append(out, flags, 0x00, 0x00, 0x00, 0x00)
	return append(out, body...)
}

// parseRaknetFrame 从数据报解析 packetID 与 body
func parseRaknetFrame(data []byte) (byte, []byte, bool) {
	if len(data) < 4 {
		return 0, nil, false
	}
	flags := data[0]
	if flags&0x80 == 0 {
		return 0, nil, false // ACK/NACK 等，忽略
	}
	return flags & 0x7f, data[4:], true
}

func raknetLE(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func putRaknetLE(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func raknetNow() int32 {
	return int32(time.Now().UnixNano() / 1e6)
}

// raknetReadString: uint16 长度 + 内容
func raknetReadString(b []byte) (string, []byte, bool) {
	if len(b) < 2 {
		return "", nil, false
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if len(b) < 2+n {
		return "", nil, false
	}
	return string(b[2 : 2+n]), b[2+n:], true
}

func appendRaknetString(dst []byte, s string) []byte {
	if len(s) > 0xffff {
		s = s[:0xffff]
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(s)))
	dst = append(dst, l[:]...)
	return append(dst, s...)
}

// appendPlayString 追加 Play 层字符串：uint32 长度前缀（小端），与 RakNet 的
// BE uint16 不同层不可混用。
func appendPlayString(dst []byte, s string) []byte {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(s)))
	dst = append(dst, l[:]...)
	return append(dst, s...)
}

// raknetReadAddress: uint16 版本(LE) + IP + uint16 端口(BE)
func raknetReadAddress(b []byte) (string, []byte, bool) {
	if len(b) < 2 {
		return "", nil, false
	}
	switch binary.LittleEndian.Uint16(b[:2]) {
	case 4:
		if len(b) < 8 {
			return "", nil, false
		}
		ip := net.IP(b[2:6]).String()
		port := binary.BigEndian.Uint16(b[6:8])
		return fmt.Sprintf("%s:%d", ip, port), b[8:], true
	case 6:
		if len(b) < 20 {
			return "", nil, false
		}
		ip := net.IP(b[2:18]).String()
		port := binary.BigEndian.Uint16(b[18:20])
		return fmt.Sprintf("[%s]:%d", ip, port), b[20:], true
	}
	return "", nil, false
}

func appendRaknetAddress(dst []byte, ipStr string, port uint16) []byte {
	host, pstr, err := net.SplitHostPort(ipStr)
	if err != nil {
		host, pstr = ipStr, ""
	}
	var p uint16
	if pstr != "" {
		fmt.Sscanf(pstr, "%d", &p)
	}
	if p == 0 {
		p = 19132
	}

	ip := net.ParseIP(host)
	var v [2]byte
	if ip4 := ip.To4(); ip4 != nil {
		binary.LittleEndian.PutUint16(v[:], 4)
		dst = append(dst, v[:]...)
		dst = append(dst, ip4...)
	} else if ip16 := ip.To16(); ip16 != nil {
		binary.LittleEndian.PutUint16(v[:], 6)
		dst = append(dst, v[:]...)
		dst = append(dst, ip16...)
	} else {
		// 无效 IP，退化为 0.0.0.0
		binary.LittleEndian.PutUint16(v[:], 4)
		dst = append(dst, v[:]...)
		dst = append(dst, 0, 0, 0, 0)
	}
	var pv [2]byte
	binary.BigEndian.PutUint16(pv[:], p)
	return append(dst, pv[:]...)
}

// ---------------- RakNet 连接原语 ----------------

func mcpeClientGUID() []byte {
	g := make([]byte, 16)
	binary.LittleEndian.PutUint64(g[:8], uint64(time.Now().UnixNano()))
	binary.LittleEndian.PutUint64(g[8:], uint64(mcpeNowRand()))
	return g
}

func mcpeNowRand() int64 {
	var b [8]byte
	rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]))
}

// raknetReadUntil 读取直到命中 want 中的 packetID
func raknetReadUntil(conn *net.UDPConn, want map[byte]bool, timeout time.Duration) (byte, []byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return 0, nil, err
		}
		pid, body, ok := parseRaknetFrame(buf[:n])
		if !ok {
			continue
		}
		if want == nil || want[pid] {
			return pid, body, nil
		}
	}
}

func raknetWrite(conn *net.UDPConn, enc *mcpeEncryption, frame []byte) error {
	if enc == nil {
		_, err := conn.Write(frame)
		return err
	}
	ct := make([]byte, len(frame))
	enc.enc.crypt(ct, frame, false)
	_, err := conn.Write(ct)
	return err
}

func raknetRead(conn *net.UDPConn, enc *mcpeEncryption, timeout time.Duration) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	data := buf[:n]
	if enc != nil {
		pt := make([]byte, len(data))
		enc.dec.crypt(pt, data, true)
		return pt, nil
	}
	return data, nil
}

// ---------------- 阶段 1：server GUID ----------------

func mcpeFetchServerGUID(target string, clientGUID []byte, timeout time.Duration) (uint64, string, error) {
	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return 0, "", err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

	body := make([]byte, 0, 25)
	body = append(body, idOfflineMessageDataID0x00)
	body = append(body, putRaknetLE(uint32(raknetNow()))...)
	body = append(body, raknetMagic...)
	body = append(body, clientGUID...)

	if _, err := conn.Write(raknetFrame(idOfflineMessageDataID0x00, body, false)); err != nil {
		return 0, "", err
	}

	_, resp, err := raknetReadUntil(conn, map[byte]bool{idUnconnectedPong: true}, timeout)
	if err != nil {
		return 0, "", err
	}
	// UnconnectedPong: time(4) + guid(8) + MAGIC(16) + str
	if len(resp) < 28 {
		return 0, "", errors.New("UnconnectedPong 长度不足")
	}
	guid := uint64(raknetLE(resp[4:12]))
	motd, _, _ := raknetReadString(resp[28:])
	atomic.AddInt64(&mcpeHandshakes, 1)
	return guid, motd, nil
}

// ---------------- 阶段 2：服务器真实端口 ----------------

func mcpeResolveServerPort(target, serverAddr string, clientGUID []byte, timeout time.Duration) (uint16, error) {
	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return 0, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	body := make([]byte, 0, 64)
	body = append(body, idUnconnectedPing)
	body = append(body, putRaknetLE(uint32(raknetNow()))...)
	body = append(body, raknetMagic...)
	body = append(body, clientGUID...)
	body = appendRaknetString(body, serverAddr)

	if _, err := conn.Write(raknetFrame(idUnconnectedPing, body, false)); err != nil {
		return 0, err
	}

	_, resp, err := raknetReadUntil(conn, map[byte]bool{
		idOpenConnectionReply1:    true,
		idIncompatibleProtocol:    true,
		idAlreadyConnected:        true,
		idUnconnectedPingOpenConn: true,
	}, timeout)
	if err != nil {
		return 0, err
	}
	// OpenConnectionReply1: security(1) + mtu(2) + guid(8) + security(1) + mtu(2) + port(2)
	if len(resp) < 19 {
		return 0, errors.New("OpenConnectionReply1 长度不足")
	}
	return binary.BigEndian.Uint16(resp[17:19]), nil
}

// ---------------- 阶段 3-5：完整连接 ----------------

type mcpeSession struct {
	conn *net.UDPConn
	enc  *mcpeEncryption
}

func (s *mcpeSession) Close() {
	if s != nil && s.conn != nil {
		s.conn.Close()
	}
}

func mcpeHandshake(target, serverAddr string, clientGUID, serverPubKey []byte,
	timeout time.Duration) (*mcpeSession, error) {

	guid, _, err := mcpeFetchServerGUID(target, clientGUID, timeout)
	if err != nil {
		return nil, fmt.Errorf("获取 server GUID 失败: %w", err)
	}

	serverPort, err := mcpeResolveServerPort(target, serverAddr, clientGUID, timeout)
	if err != nil {
		return nil, fmt.Errorf("获取服务器端口失败: %w", err)
	}

	connAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", hostOf(target), serverPort))
	if err != nil {
		return nil, err
	}

	useEnc := len(serverPubKey) == 32

	// ---- 未加密阶段 ----
	conn, err := net.DialUDP("udp", nil, connAddr)
	if err != nil {
		return nil, err
	}

	var clientToken []byte
	if useEnc {
		clientPrivate := make([]byte, 32)
		clientToken = make([]byte, 32)
		if _, err := rand.Read(clientPrivate); err != nil {
			conn.Close()
			return nil, err
		}
		if _, err := rand.Read(clientToken); err != nil {
			conn.Close()
			return nil, err
		}

		clientRandom := mcpePubkey(clientPrivate, serverPubKey)
		clientPublicKey := mcpePubkey(clientRandom, serverPubKey)
		if clientRandom == nil || clientPublicKey == nil {
			conn.Close()
			return nil, errors.New("pubkey 运算失败")
		}

		body := make([]byte, 0, 96)
		body = append(body, idOpenConnectionRequest2)
		body = append(body, putRaknetLE(uint32(guid))...)
		body = appendRaknetAddress(body, serverAddr, serverPort)
		body = append(body, clientGUID...)
		body = append(body, clientPublicKey...)

		if _, err := conn.Write(raknetFrame(idOpenConnectionRequest2, body, false)); err != nil {
			conn.Close()
			return nil, err
		}

		pid, _, err := raknetReadUntil(conn, map[byte]bool{
			idConnectionRequest:       true,
			idOpenConnectionReply2:   true,
			idIncompatibleProtocol:   true,
			idConnectionFailed:       true,
			idAlreadyConnected:       true,
		}, timeout)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if pid != idConnectionRequest {
			conn.Close()
			return nil, fmt.Errorf("OpenConnectionRequest2 未收到 ConnectionRequest (收到 0x%02x)", pid)
		}

		// MCPE 要求：收到 ConnectionRequest 后断开，启用加密再重连
		conn.Close()

		conn, err = net.DialUDP("udp", nil, connAddr)
		if err != nil {
			return nil, err
		}
		enc, err := mcpeNewEncryption(clientToken)
		if err != nil {
			conn.Close()
			return nil, err
		}
		atomic.AddInt64(&mcpeEncryptedSess, 1)

		reqBody := make([]byte, 0, 64)
		reqBody = append(reqBody, idConnectionRequest)
		reqBody = append(reqBody, clientGUID...)
		reqBody = appendRaknetAddress(reqBody, serverAddr, serverPort)
		reqBody = append(reqBody, putRaknetLE(uint32(guid))...)
		reqBody = append(reqBody, 0x01) // requestSecurity

		if err := raknetWrite(conn, enc, raknetFrame(idConnectionRequest, reqBody, true)); err != nil {
			conn.Close()
			return nil, err
		}
		return &mcpeSession{conn: conn, enc: enc}, nil
	}

	// ---- 无公钥：降级为未加密会话 ----
	body := make([]byte, 0, 64)
	body = append(body, idConnectionRequest)
	body = append(body, clientGUID...)
	body = appendRaknetAddress(body, serverAddr, serverPort)
	body = append(body, putRaknetLE(uint32(guid))...)
	body = append(body, 0x00)

	if _, err := conn.Write(raknetFrame(idConnectionRequest, body, false)); err != nil {
		conn.Close()
		return nil, err
	}
	return &mcpeSession{conn: conn, enc: nil}, nil
}

func hostOf(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// ---------------- Play 层 ----------------

func mcpeSendPlay(s *mcpeSession, payload []byte) error {
	full := make([]byte, 0, len(payload)+9)
	full = append(full, 0xFE, 0x01, 0x00, 0x01) // 0xFE + playSessionID + 包数
	var ln [2]byte
	binary.LittleEndian.PutUint16(ln[:], uint16(len(payload)))
	full = append(full, ln[:]...)
	full = append(full, payload...)
	return raknetWrite(s.conn, s.enc, full)
}

func mcpeRecvPlay(s *mcpeSession, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		remain := deadline.Sub(time.Now())
		if remain <= 0 {
			break
		}
		data, err := raknetRead(s.conn, s.enc, remain)
		if err != nil {
			return nil, err
		}
		if len(data) < 9 || data[0] != 0xFE {
			continue
		}
		batch := data[8:]
		for len(batch) > 0 {
			var ln, used int
			if batch[0]&0x80 != 0 {
				if len(batch) < 3 {
					break
				}
				ln = int(binary.LittleEndian.Uint16(batch[1:3]) & 0x3fff)
				used = 3
			} else {
				if len(batch) < 2 {
					break
				}
				ln = int(binary.LittleEndian.Uint16(batch[:2]))
				used = 2
			}
			if len(batch) < used+ln || ln <= 0 {
				break
			}
			pkt := batch[used : used+ln]
			batch = batch[used+ln:]
			if len(pkt) > 0 {
				return pkt, nil
			}
		}
	}
	return nil, errors.New("Play 层读取超时")
}

// loadServerPublicKey 读取服务端公钥文件（前 32 字节）
func loadServerPublicKey(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 32 {
		return nil
	}
	return data[:32]
}

// ---------------- status 动作 ----------------

func mcpeStatusOnce(target, serverAddr string, clientGUID []byte) error {
	if _, _, err := mcpeFetchServerGUID(target, clientGUID, 5*time.Second); err != nil {
		return err
	}
	_, err := mcpeResolveServerPort(target, serverAddr, clientGUID, 5*time.Second)
	return err
}

// ---------------- login 动作 + 挂机 ----------------

func mcpeLoginAndHold(ctx context.Context, target, serverAddr, username string,
	clientGUID, serverPubKey []byte, keepAlive bool) error {

	sess, err := mcpeHandshake(target, serverAddr, clientGUID, serverPubKey, 10*time.Second)
	if err != nil {
		return err
	}
	defer sess.Close()

	atomic.AddInt64(&mcpeActiveConns, 1)
	defer atomic.AddInt64(&mcpeActiveConns, -1)

	// RequestServerInformation
	infoID := byte(playRequestServerInfo)
	if *mcpeProtocol >= 766 {
		infoID = playRequestInfoV2
	}
	if err := mcpeSendPlay(sess, []byte{infoID}); err != nil {
		return err
	}

	// 等 NetworkSettings / SetCompression
	gotSettings := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !gotSettings {
		pkt, err := mcpeRecvPlay(sess, deadline.Sub(time.Now()))
		if err != nil {
			return err
		}
		if len(pkt) == 0 {
			continue
		}
		switch pkt[0] {
		case playNetworkSettings, playSetCompression:
			gotSettings = true
		case playDisconnect:
			return errors.New("握手期间被服务器断开")
		}
	}
	if !gotSettings {
		return errors.New("未收到 NetworkSettings")
	}

	// LoginPacket：包头 + protocol(int32 LE) + 三个字符串字段
	// Play 层字符串长度为 uint32 小端前缀
	login := make([]byte, 0, 128)
	login = append(login, playLogin)
	login = append(login, putRaknetLE(uint32(*mcpeProtocol))...)
	login = appendPlayString(login, username) // 玩家名
	login = appendPlayString(login, "")       // XUID
	login = appendPlayString(login, "")       // identity UUID
	if err := mcpeSendPlay(sess, login); err != nil {
		return err
	}

	// 等 PlayStatus 登录成功
	loggedIn := false
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !loggedIn {
		pkt, err := mcpeRecvPlay(sess, deadline.Sub(time.Now()))
		if err != nil {
			return err
		}
		if len(pkt) == 0 {
			continue
		}
		switch pkt[0] {
		case playPlayStatus:
			// playId(1) + status(1)，LOGIN_SUCCESS = 0
			if len(pkt) >= 3 && pkt[2] == 0 {
				loggedIn = true
			}
		case playResourcePackInfo, playResourcePackStack, playResourcePackClientResponse:
			// 回复资源包已全部接受
			mcpeSendPlay(sess, []byte{playResourcePackClientResponse, 0x02})
		case playDisconnect:
			return errors.New("登录阶段被服务器断开")
		}
	}
	if !loggedIn {
		return errors.New("登录未在超时内完成")
	}

	// 挂机：响应 KeepAlive / Ping
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		pkt, err := mcpeRecvPlay(sess, 10*time.Second)
		if err != nil {
			return err
		}
		if len(pkt) == 0 {
			continue
		}
		switch pkt[0] {
		case playKeepAlive:
			if keepAlive {
				mcpeSendPlay(sess, []byte{playKeepAlive})
				atomic.AddInt64(&mcpeKeepAlives, 1)
			}
		case playPing:
			if keepAlive {
				mcpeSendPlay(sess, []byte{playPong})
				atomic.AddInt64(&mcpeKeepAlives, 1)
			}
		case playDisconnect:
			return errors.New("挂机期间被服务器断开")
		}
	}
}

// ---------------- worker ----------------

func mcpeWorker(ctx context.Context, wg *sync.WaitGroup, id int) {
	defer wg.Done()

	targetStr := *target
	serverAddr := *mcpeServerAddress
	if serverAddr == "" {
		serverAddr = hostOf(targetStr)
	}

	clientGUID := mcpeClientGUID()
	serverPubKey := loadServerPublicKey(*mcpeKeyFile)
	username := fmt.Sprintf("%s%d", *mcUsernamePrefix, id)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		switch *mcpeAction {
		case "status":
			if err := mcpeStatusOnce(targetStr, serverAddr, clientGUID); err != nil {
				atomic.AddInt64(&mcpeLoginFailed, 1)
				atomic.AddInt64(&totalErrors, 1)
			} else {
				atomic.AddInt64(&mcpeStatusOK, 1)
				atomic.AddInt64(&totalPackets, 1)
			}
		default: // login
			if err := mcpeLoginAndHold(ctx, targetStr, serverAddr, username,
				clientGUID, serverPubKey, *mcpeKeepAlive); err != nil {
				atomic.AddInt64(&mcpeLoginFailed, 1)
				atomic.AddInt64(&totalErrors, 1)
				log.Printf("基岩版登录失败(worker %d): %v", id, err)
			} else {
				atomic.AddInt64(&mcpeLoginOK, 1)
			}
		}

		if !*mcReconnect {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
