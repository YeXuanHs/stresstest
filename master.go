// master.go - 主控核心逻辑
// WebSocket 服务、.env 读写、密码管理、任务调度、CLI 交互菜单
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

// getEnvFilePath 获取 .env 文件的绝对路径
func getEnvFilePath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ".env"
	}
	dir := filepath.Dir(exePath)
	return filepath.Join(dir, ".env")
}

// .env 文件路径
var envFile = getEnvFilePath()

// EnvConfig 环境配置
type EnvConfig struct {
	Port             string `json:"port"`
	Mode             string `json:"mode"` // master / agent / both
	PasswordHash     string `json:"password_hash"`
	AgentMasterHost  string `json:"agent_master_host"`
	AgentMasterPort  string `json:"agent_master_port"`
	AgentMasterToken string `json:"agent_master_token"`
	AgentName        string `json:"agent_name"`
	RegisteredAgents map[string]RegisteredAgent `json:"registered_agents"`
}

// RegisteredAgent 已注册的 Agent 信息
type RegisteredAgent struct {
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
	Order    int    `json:"order"`
}

// AgentInfo Agent 信息
type AgentInfo struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Running  bool            `json:"running"`
	CPU      int             `json:"cpu"`
	Conn     *websocket.Conn `json:"-"`
	Selected bool            `json:"selected"`
	Order    int             `json:"order"`
	LastSeen time.Time       `json:"-"`
}

// AgentStats Agent 统计数据
type AgentStats struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Running       bool    `json:"running"`
	Mode          string  `json:"mode"`
	Target        string  `json:"target"`
	Workers       int     `json:"workers"`
	TotalBytes    int64   `json:"total_bytes"`
	TotalPackets  int64   `json:"total_packets"`
	TotalErrors   int64   `json:"total_errors"`
	BytesPerSec   float64 `json:"bytes_per_sec"`
	PacketsPerSec float64 `json:"packets_per_sec"`
	Errors        int64   `json:"errors"`
	CPU           int     `json:"cpu"`
	Uptime        string  `json:"uptime"`
}

// 远程 Agent 状态
var (
	remoteTaskCancel context.CancelFunc
	remoteTaskMu     sync.Mutex
	remoteRunning    bool
)

// MasterServer 主控服务器
type MasterServer struct {
	config          EnvConfig
	agents          map[string]*AgentInfo
	agentsMu        sync.RWMutex
	upgrader        websocket.Upgrader
	httpServer      *http.Server
	lastStats       map[string]*AgentStats
	lastStatsMu     sync.RWMutex
	statsHist       map[string]*statsHistory
	localTaskCancel context.CancelFunc
}

type statsHistory struct {
	lastBytes   int64
	lastPackets int64
	lastErrors  int64
	lastTime    time.Time
}

// NewMasterServer 创建主控服务器
func NewMasterServer() *MasterServer {
	return &MasterServer{
		agents:    make(map[string]*AgentInfo),
		lastStats: make(map[string]*AgentStats),
		statsHist: make(map[string]*statsHistory),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

// LoadEnv 加载 .env 文件
func (s *MasterServer) LoadEnv() error {
	data, err := os.ReadFile(envFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "PORT":
			s.config.Port = value
		case "MODE":
			s.config.Mode = value
		case "MASTER_PASSWORD_HASH":
			s.config.PasswordHash = value
		case "MASTER_HOST":
			s.config.AgentMasterHost = value
		case "MASTER_PORT":
			s.config.AgentMasterPort = value
		case "MASTER_TOKEN":
			s.config.AgentMasterToken = value
		case "AGENT_NAME":
			s.config.AgentName = value
		case "REGISTERED_AGENTS":
			var agents map[string]RegisteredAgent
			if err := json.Unmarshal([]byte(value), &agents); err == nil {
				s.config.RegisteredAgents = agents
			}
		}
	}

	if s.config.RegisteredAgents == nil {
		s.config.RegisteredAgents = make(map[string]RegisteredAgent)
	}

	return nil
}

// SaveEnv 保存 .env 文件
func (s *MasterServer) SaveEnv() error {
	var lines []string

	lines = append(lines, "# 压力测试工具配置文件")
	lines = append(lines, fmt.Sprintf("PORT=%s", s.config.Port))
	lines = append(lines, fmt.Sprintf("MODE=%s", s.config.Mode))

	if s.config.PasswordHash != "" {
		lines = append(lines, fmt.Sprintf("MASTER_PASSWORD_HASH=%s", s.config.PasswordHash))
	}

	if s.config.AgentMasterHost != "" {
		lines = append(lines, fmt.Sprintf("MASTER_HOST=%s", s.config.AgentMasterHost))
	}
	if s.config.AgentMasterPort != "" {
		lines = append(lines, fmt.Sprintf("MASTER_PORT=%s", s.config.AgentMasterPort))
	}
	if s.config.AgentMasterToken != "" {
		lines = append(lines, fmt.Sprintf("MASTER_TOKEN=%s", s.config.AgentMasterToken))
	}
	if s.config.AgentName != "" {
		lines = append(lines, fmt.Sprintf("AGENT_NAME=%s", s.config.AgentName))
	}

	if len(s.config.RegisteredAgents) > 0 {
		agentsJSON, err := json.Marshal(s.config.RegisteredAgents)
		if err == nil {
			lines = append(lines, fmt.Sprintf("REGISTERED_AGENTS=%s", string(agentsJSON)))
		}
	}

	return os.WriteFile(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// HashPassword 密码哈希
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// CheckPassword 验证密码
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// PromptPort 交互式询问端口
func PromptPort() string {
	fmt.Print("请输入监听端口 (默认 8443): ")
	var input string
	fmt.Scanln(&input)

	input = strings.TrimSpace(input)
	if input == "" {
		return "8443"
	}

	port, err := strconv.Atoi(input)
	if err != nil || port < 1 || port > 65535 {
		fmt.Println("端口无效，使用默认端口 8443")
		return "8443"
	}

	return strconv.Itoa(port)
}

// getString 从 map 中安全获取字符串
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ============================================================
//  WebSocket 服务器 - 仅处理 /ws 端点
// ============================================================

// startWSServer 启动仅处理 /ws 的最小 HTTP 服务器
func (s *MasterServer) startWSServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleAgentWS)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		log.Printf("WebSocket 服务器监听: %s", addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("WebSocket 服务器错误: %v", err)
		}
	}()
}

// Stop 停止服务器
func (s *MasterServer) Stop() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

// handleAgentWS 处理 Agent WebSocket 连接
func (s *MasterServer) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket 升级失败: %v", err)
		return
	}
	defer conn.Close()

	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	var regMsg struct {
		Type    string `json:"type"`
		AgentID string `json:"agent_id"`
		Name    string `json:"name"`
		Token   string `json:"token"`
	}

	if err := conn.ReadJSON(&regMsg); err != nil {
		conn.WriteJSON(map[string]string{"type": "auth_fail", "msg": "读取消息失败"})
		return
	}

	if regMsg.Type != "register" {
		conn.WriteJSON(map[string]string{"type": "auth_fail", "msg": "无效的消息类型"})
		return
	}

	if !CheckPassword(regMsg.Token, s.config.PasswordHash) {
		conn.WriteJSON(map[string]string{"type": "auth_fail", "msg": "token错误"})
		return
	}

	conn.WriteJSON(map[string]string{"type": "auth_ok"})

	s.agentsMu.RLock()
	maxOrder := 0
	for _, a := range s.agents {
		if a.Order > maxOrder {
			maxOrder = a.Order
		}
	}
	s.agentsMu.RUnlock()

	s.agentsMu.Lock()
	var agent *AgentInfo
	if existing, ok := s.agents[regMsg.AgentID]; ok {
		existing.Conn = conn
		existing.Name = regMsg.Name
		existing.LastSeen = time.Now()
		agent = existing
		log.Printf("Agent 重连: %s (%s) running=%v", regMsg.AgentID, regMsg.Name, existing.Running)
	} else {
		agent = &AgentInfo{
			ID:       regMsg.AgentID,
			Name:     regMsg.Name,
			Conn:     conn,
			Selected: true,
			Order:    maxOrder + 1,
			LastSeen: time.Now(),
		}
		s.agents[regMsg.AgentID] = agent
		log.Printf("Agent 注册成功: %s (%s)", regMsg.AgentID, regMsg.Name)

		if s.config.RegisteredAgents == nil {
			s.config.RegisteredAgents = make(map[string]RegisteredAgent)
		}
		hostname := regMsg.AgentID
		if idx := strings.LastIndex(regMsg.AgentID, "-"); idx > 0 {
			hostname = regMsg.AgentID[:idx]
		}
		s.config.RegisteredAgents[regMsg.AgentID] = RegisteredAgent{
			Hostname: hostname,
			Name:     regMsg.Name,
			Order:    maxOrder + 1,
		}
		s.SaveEnv()
	}
	s.agentsMu.Unlock()

	defer func() {
		s.agentsMu.Lock()
		if a, ok := s.agents[regMsg.AgentID]; ok {
			a.Conn = nil
		}
		s.agentsMu.Unlock()
		log.Printf("Agent 断开: %s", regMsg.AgentID)
	}()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}

		msgType, _ := msg["type"].(string)
		switch msgType {
		case "status":
			s.handleAgentStatusMsg(regMsg.AgentID, msg)
		}
	}
}

// handleAgentStatusMsg 处理 Agent 状态上报
func (s *MasterServer) handleAgentStatusMsg(agentID string, msg map[string]interface{}) {
	s.agentsMu.RLock()
	agent, ok := s.agents[agentID]
	s.agentsMu.RUnlock()

	if !ok {
		return
	}

	agent.LastSeen = time.Now()

	var data map[string]interface{}
	switch v := msg["data"].(type) {
	case string:
		json.Unmarshal([]byte(v), &data)
	case map[string]interface{}:
		data = v
	}
	if data == nil {
		return
	}

	running, _ := data["running"].(bool)
	agent.Running = running

	if cpu, ok := data["cpu"].(float64); ok {
		agent.CPU = int(cpu)
	}

	totalBytes, _ := data["total_bytes"].(float64)
	totalPackets, _ := data["total_packets"].(float64)
	totalErrors, _ := data["total_errors"].(float64)
	bytesPerSec, _ := data["bytes_per_sec"].(float64)
	packetsPerSec, _ := data["packets_per_sec"].(float64)

	stats := &AgentStats{
		ID:            agentID,
		Name:          agent.Name,
		Running:       running,
		CPU:           agent.CPU,
		TotalBytes:    int64(totalBytes),
		TotalPackets:  int64(totalPackets),
		TotalErrors:   int64(totalErrors),
		BytesPerSec:   bytesPerSec,
		PacketsPerSec: packetsPerSec,
		Errors:        int64(totalErrors),
		Uptime:        getString(data, "uptime"),
	}

	s.lastStatsMu.Lock()
	s.lastStats[agentID] = stats
	s.lastStatsMu.Unlock()
}

// ============================================================
//  任务调度
// ============================================================

// startTaskOnAgents 向指定 Agent 下发启动任务
func (s *MasterServer) startTaskOnAgents(mode, target string, workers, size int, duration string, agentIDs []string) int {
	if size <= 0 {
		size = 1400
	}

	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	startMsg := map[string]interface{}{
		"type":    "start",
		"task_id": taskID,
		"config": map[string]interface{}{
			"mode":     mode,
			"target":   target,
			"workers":  workers,
			"size":     size,
			"duration": duration,
		},
	}

	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()

	sent := 0
	for _, agentID := range agentIDs {
		if agent, ok := s.agents[agentID]; ok {
			if agent.Conn != nil {
				log.Printf("向 Agent %s (%s) 下发任务...", agentID, agent.Name)
				if err := agent.Conn.WriteJSON(startMsg); err != nil {
					log.Printf("向 Agent %s 下发任务失败: %v", agentID, err)
				} else {
					sent++
					agent.Running = true
					log.Printf("向 Agent %s 下发任务成功", agentID)
				}
			} else if agentID == "local" {
				log.Printf("向本地 Agent 下发任务...")
				go s.runLocalTask(mode, target, workers, size, duration)
				sent++
			} else {
				log.Printf("Agent %s 离线，无法下发任务", agentID)
			}
		}
	}
	return sent
}

// stopTaskOnAgents 向指定 Agent 下发停止任务
func (s *MasterServer) stopTaskOnAgents(agentIDs []string) int {
	stopMsg := map[string]string{"type": "stop"}

	s.agentsMu.RLock()
	remoteAgents := make([]*AgentInfo, 0)
	localToStop := false

	for _, agentID := range agentIDs {
		if agent, ok := s.agents[agentID]; ok {
			if agent.Conn != nil {
				remoteAgents = append(remoteAgents, agent)
			} else if agentID == "local" {
				localToStop = true
			}
		}
	}
	s.agentsMu.RUnlock()

	sent := 0

	for _, agent := range remoteAgents {
		if err := agent.Conn.WriteJSON(stopMsg); err != nil {
			log.Printf("向 Agent %s 下发停止指令失败: %v", agent.ID, err)
		} else {
			sent++
			agent.Running = false
		}
	}

	if localToStop {
		s.stopLocalTask()
		sent++
	}

	return sent
}

// stopLocalTask 停止本地任务
func (s *MasterServer) stopLocalTask() {
	if s.localTaskCancel != nil {
		s.localTaskCancel()
		s.localTaskCancel = nil
	}
}

// runLocalTask 本地 Agent 执行任务
func (s *MasterServer) runLocalTask(taskMode, taskTarget string, taskWorkers, taskSize int, duration string) {
	log.Printf("本地任务启动: mode=%s target=%s workers=%d size=%d", taskMode, taskTarget, taskWorkers, taskSize)
	startTime := time.Now()

	atomic.StoreInt64(&totalBytes, 0)
	atomic.StoreInt64(&totalPackets, 0)
	atomic.StoreInt64(&totalErrors, 0)

	*mode = taskMode
	*target = taskTarget
	*workers = taskWorkers
	*payloadSize = taskSize

	var dur time.Duration
	if duration != "" && duration != "0" {
		dur, _ = time.ParseDuration(duration + "s")
	}

	ctx, cancel := context.WithCancel(context.Background())
	if dur > 0 {
		ctx, cancel = context.WithTimeout(ctx, dur)
	}

	s.localTaskCancel = cancel

	defer func() {
		cancel()
		s.localTaskCancel = nil
		log.Printf("本地任务结束")
	}()

	s.agentsMu.Lock()
	if agent, ok := s.agents["local"]; ok {
		agent.Running = true
	}
	s.agentsMu.Unlock()

	defer func() {
		s.agentsMu.Lock()
		if agent, ok := s.agents["local"]; ok {
			agent.Running = false
		}
		s.agentsMu.Unlock()
	}()

	lim := newLimiter(0)
	var wg sync.WaitGroup

	go connectionCleaner(ctx)

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var lastBytes, lastPackets int64
		lastTime := time.Now()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				currentBytes := atomic.LoadInt64(&totalBytes)
				currentPackets := atomic.LoadInt64(&totalPackets)
				currentErrors := atomic.LoadInt64(&totalErrors)

				elapsed := now.Sub(lastTime).Seconds()
				bytesPerSec := float64(currentBytes-lastBytes) / elapsed
				packetsPerSec := float64(currentPackets-lastPackets) / elapsed

				lastBytes = currentBytes
				lastPackets = currentPackets
				lastTime = now

				s.lastStatsMu.Lock()
				s.lastStats["local"] = &AgentStats{
					ID:            "local",
					Name:          "本机",
					Running:       true,
					TotalBytes:    currentBytes,
					TotalPackets:  currentPackets,
					TotalErrors:   currentErrors,
					BytesPerSec:   bytesPerSec,
					PacketsPerSec: packetsPerSec,
					Errors:        currentErrors,
					CPU:           runtime.NumCPU(),
					Uptime:        time.Since(startTime).Round(time.Second).String(),
				}
				s.lastStatsMu.Unlock()
			}
		}
	}()

	log.Printf("启动 %d 个 worker", taskWorkers)
	for i := 0; i < taskWorkers; i++ {
		wg.Add(1)
		switch taskMode {
		case "tcp":
			go tcpWorker(ctx, &wg, i, lim)
		case "udp":
			go udpWorker(ctx, &wg, i, lim)
		case "http":
			go httpWorker(ctx, &wg, i, lim)
		default:
			wg.Done()
		}
	}

	wg.Wait()
	log.Printf("本地任务完成")
}

// ============================================================
//  Agent 通信 (连接主控)
// ============================================================

func connectToMaster(server *MasterServer) {
	masterURL := fmt.Sprintf("ws://%s:%s/ws", server.config.AgentMasterHost, server.config.AgentMasterPort)
	log.Printf("连接主控: %s", masterURL)

	hostname, _ := os.Hostname()
	agentName := server.config.AgentName
	if agentName == "" {
		agentName = hostname
	}
	agentID := hostname

	for {
		err := runAgentConnection(server, masterURL, agentID, agentName, server.config.AgentMasterToken)
		if err != nil {
			log.Printf("连接断开: %v，3秒后重连...", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func runAgentConnection(server *MasterServer, masterURL, agentID, agentName, token string) error {
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.Dial(masterURL, nil)
	if err != nil {
		return fmt.Errorf("连接主控失败: %v", err)
	}
	defer conn.Close()

	reg := AgentMessage{
		Type:    "register",
		AgentID: agentID,
		Name:    agentName,
		Token:   token,
	}
	if err := conn.WriteJSON(reg); err != nil {
		return fmt.Errorf("注册失败: %v", err)
	}

	var authResult MasterCommand
	if err := conn.ReadJSON(&authResult); err != nil {
		return fmt.Errorf("读取鉴权结果失败: %v", err)
	}

	if authResult.Type == "auth_fail" {
		return fmt.Errorf("鉴权失败: %s", authResult.Msg)
	}

	if authResult.Type != "auth_ok" {
		return fmt.Errorf("未知的鉴权响应: %s", authResult.Type)
	}

	log.Printf("已连接到主控，等待任务...")

	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	for {
		var cmd MasterCommand
		if err := conn.ReadJSON(&cmd); err != nil {
			return fmt.Errorf("读取消息失败: %v", err)
		}

		switch cmd.Type {
		case "start":
			var cfg struct {
				Mode     string `json:"mode"`
				Target   string `json:"target"`
				Workers  int    `json:"workers"`
				Size     int    `json:"size"`
				Duration string `json:"duration"`
			}
			if err := json.Unmarshal(cmd.Config, &cfg); err != nil {
				log.Printf("任务配置解析失败: %v", err)
				continue
			}
			log.Printf("收到任务: mode=%s target=%s workers=%d size=%d", cfg.Mode, cfg.Target, cfg.Workers, cfg.Size)
			go server.runRemoteTask(cfg.Mode, cfg.Target, cfg.Workers, cfg.Size, cfg.Duration, conn, agentID)

		case "config_update":
			var updateData ConfigUpdateData
			if err := json.Unmarshal(cmd.Data, &updateData); err == nil {
				if updateData.AgentName != "" {
					agentName = updateData.AgentName
					log.Printf("名称已更新: %s", agentName)
				}
			}
		case "stop":
			log.Printf("收到停止指令")
			remoteTaskMu.Lock()
			if remoteTaskCancel != nil {
				remoteTaskCancel()
			}
			remoteTaskMu.Unlock()
		}
	}
}

// runRemoteTask 远程 Agent 执行任务
func (s *MasterServer) runRemoteTask(taskMode, taskTarget string, taskWorkers, taskSize int, duration string, conn *websocket.Conn, agentID string) {
	log.Printf("开始执行任务: mode=%s target=%s workers=%d size=%d", taskMode, taskTarget, taskWorkers, taskSize)

	atomic.StoreInt64(&totalBytes, 0)
	atomic.StoreInt64(&totalPackets, 0)
	atomic.StoreInt64(&totalErrors, 0)

	*mode = taskMode
	*target = taskTarget
	*workers = taskWorkers
	*payloadSize = taskSize

	var dur time.Duration
	if duration != "" && duration != "0" {
		dur, _ = time.ParseDuration(duration + "s")
	}

	ctx, cancel := context.WithCancel(context.Background())
	if dur > 0 {
		ctx, cancel = context.WithTimeout(ctx, dur)
	}
	defer cancel()

	remoteTaskMu.Lock()
	remoteTaskCancel = cancel
	remoteRunning = true
	remoteTaskMu.Unlock()

	s.agentsMu.Lock()
	if agent, ok := s.agents[agentID]; ok {
		agent.Running = true
	}
	s.agentsMu.Unlock()

	defer func() {
		remoteTaskMu.Lock()
		remoteTaskCancel = nil
		remoteRunning = false
		remoteTaskMu.Unlock()

		s.agentsMu.Lock()
		if agent, ok := s.agents[agentID]; ok {
			agent.Running = false
		}
		s.agentsMu.Unlock()
	}()

	startTime := time.Now()
	lim := newLimiter(0)
	var wg sync.WaitGroup

	go connectionCleaner(ctx)

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var lastBytes, lastPackets int64
		lastTime := time.Now()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				currentBytes := atomic.LoadInt64(&totalBytes)
				currentPackets := atomic.LoadInt64(&totalPackets)
				currentErrors := atomic.LoadInt64(&totalErrors)

				elapsed := now.Sub(lastTime).Seconds()
				bytesPerSec := float64(currentBytes-lastBytes) / elapsed
				packetsPerSec := float64(currentPackets-lastPackets) / elapsed

				lastBytes = currentBytes
				lastPackets = currentPackets
				lastTime = now

				statusMsg := AgentMessage{
					Type: "status",
					Data: mustMarshal(StatusReport{
						Running:       true,
						Mode:          taskMode,
						Target:        taskTarget,
						Workers:       taskWorkers,
						TotalBytes:    currentBytes,
						TotalPackets:  currentPackets,
						TotalErrors:   currentErrors,
						BytesPerSec:   bytesPerSec,
						PacketsPerSec: packetsPerSec,
						CPU:           runtime.NumCPU(),
						Uptime:        time.Since(startTime).Round(time.Second).String(),
					}),
				}
				conn.WriteJSON(statusMsg)
			}
		}
	}()

	log.Printf("启动 %d 个 worker", taskWorkers)
	for i := 0; i < taskWorkers; i++ {
		wg.Add(1)
		switch taskMode {
		case "tcp":
			go tcpWorker(ctx, &wg, i, lim)
		case "udp":
			go udpWorker(ctx, &wg, i, lim)
		case "http":
			go httpWorker(ctx, &wg, i, lim)
		default:
			wg.Done()
		}
	}

	wg.Wait()
	log.Printf("任务完成")
}

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// ============================================================
//  CLI 交互菜单
// ============================================================

// ANSI 颜色
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[37m"
	colorBold   = "\033[1m"
	clearScreen = "\033[2J\033[H"
)

// clearScreenCmd 清屏
func clearScreenCmd() {
	fmt.Print(clearScreen)
}

// readLine 读取一行输入
func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// getSortedAgentList 获取排序后的 Agent 列表（本地在前，远程按 order 排序）
func (s *MasterServer) getSortedAgentList() []struct {
	ID      string
	Name    string
	Running bool
	Online  bool
} {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()

	type agentEntry struct {
		ID      string
		Name    string
		Running bool
		Online  bool
		Order   int
		IsLocal bool
	}

	entries := make([]agentEntry, 0)

	// 先添加本地 Agent
	if local, ok := s.agents["local"]; ok {
		entries = append(entries, agentEntry{
			ID:      "local",
			Name:    local.Name,
			Running: local.Running,
			Online:  true,
			Order:   -1,
			IsLocal: true,
		})
	}

	// 再添加远程 Agent
	for _, reg := range s.config.RegisteredAgents {
		// 尝试匹配所有已知 agent ID
		var foundAgent *AgentInfo
		for id, a := range s.agents {
			if id == reg.Hostname || strings.HasPrefix(id, reg.Hostname+"-") {
				if foundAgent == nil || a.Order < foundAgent.Order {
					foundAgent = a
				}
			}
		}

		online := false
		running := false
		if foundAgent != nil {
			online = foundAgent.Conn != nil
			running = foundAgent.Running
		}

		entries = append(entries, agentEntry{
			ID:      reg.Hostname,
			Name:    reg.Name,
			Running: running,
			Online:  online,
			Order:   reg.Order,
			IsLocal: false,
		})
	}

	// 排序：本地在前，远程按 order 排
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsLocal != entries[j].IsLocal {
			return entries[i].IsLocal
		}
		if entries[i].Order != entries[j].Order {
			return entries[i].Order < entries[j].Order
		}
		return entries[i].Name < entries[j].Name
	})

	result := make([]struct {
		ID      string
		Name    string
		Running bool
		Online  bool
	}, len(entries))

	for i, e := range entries {
		result[i] = struct {
			ID      string
			Name    string
			Running bool
			Online  bool
		}{e.ID, e.Name, e.Running, e.Online}
	}

	return result
}

// cliMenu 主控 CLI 交互菜单
func (s *MasterServer) cliMenu() {
	reader := bufio.NewReader(os.Stdin)

	for {
		clearScreenCmd()
		fmt.Println(colorBold + colorCyan + "╔══════════════════════════════════════════════════╗" + colorReset)
		fmt.Println(colorBold + colorCyan + "║           压力测试工具 - 主控面板               ║" + colorReset)
		fmt.Println(colorBold + colorCyan + "╚══════════════════════════════════════════════════╝" + colorReset)
		fmt.Println()

		// 显示 Agent 列表
		agents := s.getSortedAgentList()
		if len(agents) == 0 {
			fmt.Println(colorYellow + "  暂无已连接的 Agent" + colorReset)
		} else {
			fmt.Println(colorBold + "  已注册 Agent:" + colorReset)
			for i, a := range agents {
				statusIcon := colorRed + "● 离线" + colorReset
				taskStatus := ""
				if a.Online {
					if a.Running {
						statusIcon = colorGreen + "● 在线" + colorReset
						taskStatus = colorYellow + " [运行中]" + colorReset
					} else {
						statusIcon = colorGreen + "● 在线" + colorReset
						taskStatus = " [空闲]"
					}
				}
				fmt.Printf("    [%d] %s  %s%s\n", i+1, a.Name, statusIcon, taskStatus)
			}
		}

		fmt.Println()
		fmt.Println(colorBold + "  操作:" + colorReset)
		fmt.Println("    [1] 查看 Agent 实时统计")
		fmt.Println("    [2] 查看全部统计汇总")
		fmt.Println("    [3] 启动任务")
		fmt.Println("    [4] 停止任务")
		fmt.Println("    [5] 刷新")
		fmt.Println("    [0] 退出")
		fmt.Println()
		fmt.Print(colorBold + "  请选择 > " + colorReset)

		choice := readLine(reader)

		switch choice {
		case "1":
			s.cliSelectAgentStats(reader)
		case "2":
			s.cliTotalStats(reader)
		case "3":
			s.cliStartTask(reader)
		case "4":
			s.cliStopTask(reader)
		case "5":
			continue
		case "0":
			fmt.Println(colorYellow + "  正在退出..." + colorReset)
			s.Stop()
			os.Exit(0)
		default:
			fmt.Print(colorRed + "  无效选择，按 Enter 继续..." + colorReset)
			readLine(reader)
		}
	}
}

// cliSelectAgentStats 选择 Agent 查看实时统计
func (s *MasterServer) cliSelectAgentStats(reader *bufio.Reader) {
	agents := s.getSortedAgentList()
	if len(agents) == 0 {
		fmt.Println(colorRed + "  暂无 Agent，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	fmt.Println()
	fmt.Println(colorBold + "  选择 Agent:" + colorReset)
	for i, a := range agents {
		status := colorRed + "离线" + colorReset
		if a.Online {
			status = colorGreen + "在线" + colorReset
		}
		fmt.Printf("    [%d] %s (%s)\n", i+1, a.Name, status)
	}
	fmt.Println("    [0] 返回")
	fmt.Print(colorBold + "  选择 > " + colorReset)

	choice := readLine(reader)
	if choice == "0" || choice == "" {
		return
	}

	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 1 || idx > len(agents) {
		fmt.Print(colorRed + "  无效选择，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	selected := agents[idx-1]
	s.cliMonitorAgent(selected.ID, selected.Name, reader)
}

// cliMonitorAgent 实时监控单个 Agent
func (s *MasterServer) cliMonitorAgent(agentID, agentName string, reader *bufio.Reader) {
	done := make(chan struct{})

	// 监听 Enter 键
	go func() {
		reader.ReadString('\n')
		close(done)
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			clearScreenCmd()
			fmt.Println(colorBold + colorCyan + fmt.Sprintf("=== 实时监控: %s ===", agentName) + colorReset)
			fmt.Println()

			s.lastStatsMu.RLock()
			stats, ok := s.lastStats[agentID]
			s.lastStatsMu.RUnlock()

			if ok && stats != nil {
				mbps := stats.BytesPerSec / 1024 / 1024
				gbps := mbps / 1024
				fmt.Printf("  吞吐:     %s%.2f MB/s%s (%.3f GB/s)\n", colorBold, mbps, colorReset, gbps)
				fmt.Printf("  包速率:   %s%.0f pkt/s%s\n", colorBold, stats.PacketsPerSec, colorReset)
				fmt.Printf("  累计错误: %s%d%s\n", colorBold, stats.TotalErrors, colorReset)
				fmt.Printf("  累计发送: %.2f GB\n", float64(stats.TotalBytes)/1024/1024/1024)
				fmt.Printf("  累计包数: %d\n", stats.TotalPackets)
				fmt.Printf("  运行时间: %s\n", stats.Uptime)
			} else {
				fmt.Println(colorYellow + "  等待数据..." + colorReset)
			}

			fmt.Println()
			fmt.Println(colorYellow + "  按 Enter 返回菜单..." + colorReset)
		}
	}
}

// cliTotalStats 查看全部 Agent 汇总统计
func (s *MasterServer) cliTotalStats(reader *bufio.Reader) {
	done := make(chan struct{})

	go func() {
		reader.ReadString('\n')
		close(done)
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			clearScreenCmd()
			fmt.Println(colorBold + colorCyan + "=== 全部 Agent 统计汇总 ===" + colorReset)
			fmt.Println()

			s.lastStatsMu.RLock()
			var totalBPS, totalPPS float64
			var totalBytes, totalPackets, totalErrors int64
			var runningCount, totalCount int

			for _, stats := range s.lastStats {
				totalCount++
				if stats.Running {
					runningCount++
				}
				totalBPS += stats.BytesPerSec
				totalPPS += stats.PacketsPerSec
				totalBytes += stats.TotalBytes
				totalPackets += stats.TotalPackets
				totalErrors += stats.TotalErrors
			}
			s.lastStatsMu.RUnlock()

			s.agentsMu.RLock()
			agentCount := len(s.agents)
			s.agentsMu.RUnlock()

			mbps := totalBPS / 1024 / 1024
			gbps := mbps / 1024

			fmt.Printf("  活跃 Agent: %d / %d\n", runningCount, agentCount)
			fmt.Println()
			fmt.Printf("  总吞吐:     %s%.2f MB/s%s (%.3f GB/s)\n", colorBold, mbps, colorReset, gbps)
			fmt.Printf("  总包速率:   %s%.0f pkt/s%s\n", colorBold, totalPPS, colorReset)
			fmt.Printf("  总累计错误: %s%d%s\n", colorBold, totalErrors, colorReset)
			fmt.Printf("  总累计发送: %.2f GB\n", float64(totalBytes)/1024/1024/1024)
			fmt.Printf("  总累计包数: %d\n", totalPackets)

			fmt.Println()
			fmt.Println(colorYellow + "  按 Enter 返回菜单..." + colorReset)
		}
	}
}

// cliStartTask 启动任务交互流程
func (s *MasterServer) cliStartTask(reader *bufio.Reader) {
	fmt.Println()
	fmt.Println(colorBold + "  === 启动任务 ===" + colorReset)
	fmt.Println()

	// 选择模式
	fmt.Println("  选择模式:")
	fmt.Println("    [1] tcp")
	fmt.Println("    [2] udp")
	fmt.Println("    [3] http")
	fmt.Print("  模式 > ")
	modeChoice := readLine(reader)

	var taskMode string
	switch modeChoice {
	case "1":
		taskMode = "tcp"
	case "2":
		taskMode = "udp"
	case "3":
		taskMode = "http"
	default:
		fmt.Println(colorRed + "  无效模式，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	// 目标地址
	fmt.Print("  目标地址 (host:port 或 http://url): ")
	taskTarget := readLine(reader)
	if taskTarget == "" {
		fmt.Println(colorRed + "  目标不能为空，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	// 并发数
	fmt.Print("  并发数 (默认 9999): ")
	workersStr := readLine(reader)
	taskWorkers := 9999
	if workersStr != "" {
		if w, err := strconv.Atoi(workersStr); err == nil && w > 0 {
			taskWorkers = w
		}
	}

	// 包大小
	fmt.Print("  包大小 字节 (默认 1450): ")
	sizeStr := readLine(reader)
	taskSize := 1450
	if sizeStr != "" {
		if sz, err := strconv.Atoi(sizeStr); err == nil && sz > 0 {
			taskSize = sz
		}
	}

	// 时长
	fmt.Print("  时长秒 (0=无限): ")
	durStr := readLine(reader)
	if durStr == "" {
		durStr = "0"
	}

	// 选择 Agent
	agents := s.getSortedAgentList()
	if len(agents) == 0 {
		fmt.Println(colorRed + "  无可用 Agent，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	fmt.Println()
	fmt.Println("  选择 Agent:")
	fmt.Println("    [0] 全部")
	for i, a := range agents {
		status := colorRed + "离线" + colorReset
		if a.Online {
			status = colorGreen + "在线" + colorReset
		}
		fmt.Printf("    [%d] %s (%s)\n", i+1, a.Name, status)
	}
	fmt.Print("  选择 > ")
	agentChoice := readLine(reader)

	var selectedIDs []string
	if agentChoice == "0" || agentChoice == "" {
		for _, a := range agents {
			selectedIDs = append(selectedIDs, a.ID)
		}
	} else {
		idx, err := strconv.Atoi(agentChoice)
		if err != nil || idx < 1 || idx > len(agents) {
			fmt.Println(colorRed + "  无效选择，按 Enter 返回..." + colorReset)
			readLine(reader)
			return
		}
		selectedIDs = append(selectedIDs, agents[idx-1].ID)
	}

	// 确认
	fmt.Println()
	fmt.Printf(colorYellow+"  即将向 %d 个 Agent 下发任务:\n", len(selectedIDs))
	fmt.Printf("    模式: %s\n", taskMode)
	fmt.Printf("    目标: %s\n", taskTarget)
	fmt.Printf("    并发: %d\n", taskWorkers)
	fmt.Printf("    包大小: %d\n", taskSize)
	if durStr != "0" {
		fmt.Printf("    时长: %ss\n", durStr)
	} else {
		fmt.Printf("    时长: 无限\n")
	}
	fmt.Println(colorReset)
	fmt.Print("  确认? (y/N): ")
	confirm := readLine(reader)

	if strings.ToLower(confirm) != "y" {
		fmt.Println(colorYellow + "  已取消，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	sent := s.startTaskOnAgents(taskMode, taskTarget, taskWorkers, taskSize, durStr, selectedIDs)
	fmt.Printf(colorGreen+"  已向 %d 个 Agent 下发任务，按 Enter 返回..."+colorReset, sent)
	readLine(reader)
}

// cliStopTask 停止任务交互流程
func (s *MasterServer) cliStopTask(reader *bufio.Reader) {
	agents := s.getSortedAgentList()

	// 检查是否有运行中的任务
	anyRunning := false
	for _, a := range agents {
		if a.Running {
			anyRunning = true
			break
		}
	}

	if !anyRunning {
		fmt.Println(colorYellow + "  当前没有运行中的任务，按 Enter 返回..." + colorReset)
		readLine(reader)
		return
	}

	fmt.Println()
	fmt.Println(colorBold + "  === 停止任务 ===" + colorReset)
	fmt.Println()
	fmt.Println("  选择要停止的 Agent:")
	fmt.Println("    [0] 全部停止")
	for i, a := range agents {
		status := ""
		if a.Running {
			status = colorYellow + " [运行中]" + colorReset
		}
		fmt.Printf("    [%d] %s%s\n", i+1, a.Name, status)
	}
	fmt.Print("  选择 > ")
	choice := readLine(reader)

	var selectedIDs []string
	if choice == "0" || choice == "" {
		for _, a := range agents {
			selectedIDs = append(selectedIDs, a.ID)
		}
	} else {
		idx, err := strconv.Atoi(choice)
		if err != nil || idx < 1 || idx > len(agents) {
			fmt.Println(colorRed + "  无效选择，按 Enter 返回..." + colorReset)
			readLine(reader)
			return
		}
		selectedIDs = append(selectedIDs, agents[idx-1].ID)
	}

	sent := s.stopTaskOnAgents(selectedIDs)
	fmt.Printf(colorGreen+"  已向 %d 个 Agent 下发停止指令，按 Enter 返回..."+colorReset, sent)
	readLine(reader)
}

// ============================================================
//  运行入口
// ============================================================

// RunMaster 运行主控模式
func RunMaster() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 确保有密码（首次运行时设置）
	if server.config.PasswordHash == "" {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println(colorBold + "  首次运行，请设置管理密码:" + colorReset)
		fmt.Print("  密码: ")
		password := readLine(reader)
		if len(password) < 6 {
			fmt.Println(colorRed + "  密码长度不能少于6位，使用默认密码 123456" + colorReset)
			password = "123456"
		}
		hash, err := HashPassword(password)
		if err != nil {
			log.Fatalf("密码加密失败: %v", err)
		}
		server.config.PasswordHash = hash
		server.config.AgentMasterToken = password
		server.config.Mode = "master"
	}

	if server.config.Port == "" {
		server.config.Port = PromptPort()
		if err := server.SaveEnv(); err != nil {
			log.Fatalf("保存配置失败: %v", err)
		}
	}

	addr := "0.0.0.0:" + server.config.Port

	fmt.Println()
	fmt.Println(colorBold + colorCyan + "┌─────────────────────────────────────────────┐" + colorReset)
	fmt.Println(colorBold + colorCyan + "│  主控已启动                                  │" + colorReset)
	fmt.Println(colorBold + colorCyan + "│                                             │" + colorReset)
	fmt.Printf(colorBold+colorCyan+"│  WebSocket: ws://%s/ws          │\n"+colorReset, addr)
	fmt.Println(colorBold + colorCyan + "│                                             │" + colorReset)
	fmt.Println(colorBold + colorCyan + "└─────────────────────────────────────────────┘" + colorReset)
	fmt.Println()

	// 启动 WebSocket 服务器
	server.startWSServer(addr)

	// 运行 CLI 菜单
	server.cliMenu()
}

// RunAgent 运行被控模式
func RunAgent() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	fmt.Println()
	fmt.Println(colorBold + colorCyan + "┌─────────────────────────────────────────────┐" + colorReset)
	fmt.Println(colorBold + colorCyan + "│  被控已启动                                  │" + colorReset)
	fmt.Println(colorBold + colorCyan + "│  等待连接主控...                             │" + colorReset)
	fmt.Println(colorBold + colorCyan + "└─────────────────────────────────────────────┘" + colorReset)
	fmt.Println()

	// 启动 WebSocket 连接到主控
	if server.config.AgentMasterHost != "" && server.config.AgentMasterPort != "" {
		connectToMaster(server)
	} else {
		log.Fatal("未配置主控地址，请在 .env 中设置 MASTER_HOST 和 MASTER_PORT")
	}
}

// RunBoth 运行主控+被控模式
func RunBoth() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 确保有密码（首次运行时设置）
	if server.config.PasswordHash == "" {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println(colorBold + "  首次运行，请设置管理密码:" + colorReset)
		fmt.Print("  密码: ")
		password := readLine(reader)
		if len(password) < 6 {
			fmt.Println(colorRed + "  密码长度不能少于6位，使用默认密码 123456" + colorReset)
			password = "123456"
		}
		hash, err := HashPassword(password)
		if err != nil {
			log.Fatalf("密码加密失败: %v", err)
		}
		server.config.PasswordHash = hash
		server.config.AgentMasterToken = password
		server.config.Mode = "both"
	}

	if server.config.Port == "" {
		server.config.Port = PromptPort()
		if err := server.SaveEnv(); err != nil {
			log.Fatalf("保存配置失败: %v", err)
		}
	}

	addr := "0.0.0.0:" + server.config.Port

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

	fmt.Println()
	fmt.Println(colorBold + colorCyan + "┌─────────────────────────────────────────────┐" + colorReset)
	fmt.Println(colorBold + colorCyan + "│  主控+被控已启动                             │" + colorReset)
	fmt.Println(colorBold + colorCyan + "│                                             │" + colorReset)
	fmt.Printf(colorBold+colorCyan+"│  WebSocket: ws://%s/ws          │\n"+colorReset, addr)
	fmt.Println(colorBold + colorCyan + "└─────────────────────────────────────────────┘" + colorReset)
	fmt.Println()

	// 启动 WebSocket 服务器
	server.startWSServer(addr)

	// 运行 CLI 菜单
	server.cliMenu()
}
