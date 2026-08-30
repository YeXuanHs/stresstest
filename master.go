// master.go - 主控核心逻辑
// HTTP/WS 服务、.env 读写、密码管理、任务调度
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
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

//go:embed all:web/dist
var webDist embed.FS

// getEnvFilePath 获取 .env 文件的绝对路径
func getEnvFilePath() string {
	// 获取可执行文件路径
	exePath, err := os.Executable()
	if err != nil {
		return ".env"
	}
	// 获取可执行文件所在目录
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
	// 已注册的远程 Agent 列表（按 hostname 存储）
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
	ID       string `json:"id"`
	Name     string `json:"name"`
	Running  bool   `json:"running"`
	CPU      int    `json:"cpu"`
	Conn     *websocket.Conn `json:"-"`
	Selected bool   `json:"selected"`
	Order    int    `json:"order"`
	LastSeen time.Time `json:"-"`
}

// AgentStats Agent 统计数据
type AgentStats struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Running         bool    `json:"running"`
	Mode            string  `json:"mode"`
	Target          string  `json:"target"`
	Workers         int     `json:"workers"`
	TotalBytes      int64   `json:"total_bytes"`
	TotalPackets    int64   `json:"total_packets"`
	TotalErrors     int64   `json:"total_errors"`
	BytesPerSec     float64 `json:"bytes_per_sec"`
	PacketsPerSec   float64 `json:"packets_per_sec"`
	Errors          int64   `json:"errors"`
	CPU             int     `json:"cpu"`
	Uptime          string  `json:"uptime"`
}

// 远程 Agent 状态
var (
	remoteTaskCancel context.CancelFunc
	remoteTaskMu     sync.Mutex
	remoteRunning    bool
)

// MasterServer 主控服务器
type MasterServer struct {
	config         EnvConfig
	agents         map[string]*AgentInfo
	agentsMu       sync.RWMutex
	upgrader       websocket.Upgrader
	httpServer     *http.Server
	statsWS        map[*websocket.Conn]bool
	statsMu        sync.RWMutex
	lastStats      map[string]*AgentStats
	lastStatsMu    sync.RWMutex
	statsHist      map[string]*statsHistory
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
		statsWS:   make(map[*websocket.Conn]bool),
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

	// 保存已注册的 Agent 列表
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

// SetupRoutes 设置路由
func (s *MasterServer) SetupRoutes(mux *http.ServeMux) {
	// API 路由
	mux.HandleFunc("/api/mode", s.handleGetMode)
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/password", s.handleChangePassword)
	mux.HandleFunc("/api/settings/port", s.handleChangePort)
	mux.HandleFunc("/api/agents", s.handleGetAgents)
	mux.HandleFunc("/api/agents/name", s.handleUpdateAgentName)
	mux.HandleFunc("/api/agents/order", s.handleUpdateAgentOrder)
	mux.HandleFunc("/api/task/start", s.handleStartTask)
	mux.HandleFunc("/api/task/stop", s.handleStopTask)
	mux.HandleFunc("/api/agent/status", s.handleAgentStatus)
	mux.HandleFunc("/api/agent/config", s.handleAgentConfig)

	// WebSocket 路由
	mux.HandleFunc("/ws", s.handleAgentWS)
	mux.HandleFunc("/ws/stats", s.handleStatsWS)

	// 静态文件服务 - 使用嵌入的文件系统，支持 SPA 路由
	subFS, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		log.Printf("警告: 无法加载嵌入的前端文件: %v", err)
		// 尝试从磁盘加载（开发模式）
		if _, err := os.Stat("web/dist"); err == nil {
			s.setupSPAHandler(mux, http.Dir("web/dist"))
		}
	} else {
		s.setupSPAHandler(mux, http.FS(subFS))
	}
}

// setupSPAHandler 设置 SPA 路由处理
func (s *MasterServer) setupSPAHandler(mux *http.ServeMux, filesystem http.FileSystem) {
	fileServer := http.FileServer(filesystem)
	
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 清理路径
		path := r.URL.Path
		
		// 如果是根路径，直接返回 index.html
		if path == "/" || path == "" {
			data, err := fs.ReadFile(webDist, "web/dist/index.html")
			if err != nil {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		
		// 去掉尾部斜杠
		cleanPath := path
		if len(cleanPath) > 1 && cleanPath[len(cleanPath)-1] == '/' {
			cleanPath = cleanPath[:len(cleanPath)-1]
		}
		
		// 检查文件是否存在
		f, err := filesystem.Open(cleanPath)
		if err != nil {
			// 文件不存在，返回 index.html（SPA 路由）
			data, err := fs.ReadFile(webDist, "web/dist/index.html")
			if err != nil {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		defer f.Close()
		
		// 检查是否是目录
		stat, err := f.Stat()
		if err != nil || stat.IsDir() {
			// 是目录，返回 index.html（SPA 路由）
			data, err := fs.ReadFile(webDist, "web/dist/index.html")
			if err != nil {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		
		// 文件存在，正常服务
		fileServer.ServeHTTP(w, r)
	})
}

// Start 启动服务器
func (s *MasterServer) Start(addr string) error {
	mux := http.NewServeMux()
	s.SetupRoutes(mux)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s.httpServer.ListenAndServe()
}

// Stop 停止服务器
func (s *MasterServer) Stop() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

// API 处理函数

func (s *MasterServer) handleGetMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"mode":          s.config.Mode,
		"has_password":  s.config.PasswordHash != "",
	})
}

func (s *MasterServer) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Mode     string `json:"mode"`
		Password string `json:"password"`
		Host     string `json:"host"`
		Port     string `json:"port"`
		Name     string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	switch req.Mode {
	case "master", "both":
		if len(req.Password) < 6 {
			http.Error(w, `{"msg":"密码长度不能少于6位"}`, http.StatusBadRequest)
			return
		}

		hash, err := HashPassword(req.Password)
		if err != nil {
			http.Error(w, `{"msg":"密码加密失败"}`, http.StatusInternalServerError)
			return
		}

		s.config.Mode = req.Mode
		s.config.PasswordHash = hash
		s.config.AgentMasterToken = req.Password

	case "agent":
		if req.Host == "" || req.Port == "" || req.Password == "" {
			http.Error(w, `{"msg":"请填写完整的主控信息"}`, http.StatusBadRequest)
			return
		}

		hash, err := HashPassword(req.Password)
		if err != nil {
			http.Error(w, `{"msg":"密码加密失败"}`, http.StatusInternalServerError)
			return
		}

		s.config.Mode = "agent"
		s.config.PasswordHash = hash
		s.config.AgentMasterHost = req.Host
		s.config.AgentMasterPort = req.Port
		s.config.AgentMasterToken = req.Password
		s.config.AgentName = req.Name

	default:
		http.Error(w, `{"msg":"无效的模式"}`, http.StatusBadRequest)
		return
	}

	if err := s.SaveEnv(); err != nil {
		http.Error(w, `{"msg":"保存配置失败"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

func (s *MasterServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	if !CheckPassword(req.Password, s.config.PasswordHash) {
		http.Error(w, `{"msg":"密码错误"}`, http.StatusUnauthorized)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

func (s *MasterServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 验证当前 token
	token := extractToken(r)
	if token == "" {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	if !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"当前密码错误"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	if len(req.NewPassword) < 6 {
		http.Error(w, `{"msg":"新密码长度不能少于6位"}`, http.StatusBadRequest)
		return
	}

	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		http.Error(w, `{"msg":"密码加密失败"}`, http.StatusInternalServerError)
		return
	}

	s.config.PasswordHash = hash
	s.config.AgentMasterToken = req.NewPassword

	if err := s.SaveEnv(); err != nil {
		http.Error(w, `{"msg":"保存配置失败"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

func (s *MasterServer) handleChangePort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		Port int `json:"port"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	if req.Port < 1 || req.Port > 65535 {
		http.Error(w, `{"msg":"端口范围错误"}`, http.StatusBadRequest)
		return
	}

	s.config.Port = strconv.Itoa(req.Port)

	if err := s.SaveEnv(); err != nil {
		http.Error(w, `{"msg":"保存配置失败"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})

	// 延迟重启
	go func() {
		time.Sleep(1 * time.Second)
		log.Println("端口已更改，正在重启...")
		os.Exit(0)
	}()
}

func (s *MasterServer) handleGetAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	agentsCopy := make([]map[string]interface{}, 0)

	// 添加本地 Agent（如果模式包含 both 或 master）
	if s.config.Mode == "both" || s.config.Mode == "master" {
		hostname, _ := os.Hostname()
		localName := hostname + " (本机)"
		if s.config.AgentName != "" {
			localName = s.config.AgentName
		}

		s.agentsMu.RLock()
		localAgent, hasLocal := s.agents["local"]
		s.agentsMu.RUnlock()

		agentData := map[string]interface{}{
			"id":       "local",
			"name":     localName,
			"running":  false,
			"cpu":      runtime.NumCPU(),
			"selected": true,
			"order":    0,
			"online":   true,
		}

		if hasLocal {
			agentData["running"] = localAgent.Running
		}

		// 读取统计数据
		s.lastStatsMu.RLock()
		if stat, ok := s.lastStats["local"]; ok {
			agentData["bytesPerSec"] = stat.BytesPerSec
			agentData["packetsPerSec"] = stat.PacketsPerSec
			agentData["errors"] = stat.TotalErrors
			agentData["totalBytes"] = stat.TotalBytes
			agentData["totalPackets"] = stat.TotalPackets
			agentData["uptime"] = stat.Uptime
		}
		s.lastStatsMu.RUnlock()

		agentsCopy = append(agentsCopy, agentData)
	}

	// 从 .env 读取已注册的远程 Agent
	if s.config.RegisteredAgents == nil {
		s.config.RegisteredAgents = make(map[string]RegisteredAgent)
	}

	for agentID, regAgent := range s.config.RegisteredAgents {
		s.agentsMu.RLock()
		connectedAgent, isConnected := s.agents[agentID]
		s.agentsMu.RUnlock()

		agentData := map[string]interface{}{
			"id":       agentID,
			"name":     regAgent.Name,
			"running":  false,
			"cpu":      0,
			"selected": true,
			"order":    regAgent.Order,
			"online":   isConnected && connectedAgent.Conn != nil,
		}

		if isConnected {
			agentData["running"] = connectedAgent.Running
			agentData["cpu"] = connectedAgent.CPU

			// 读取统计数据
			s.lastStatsMu.RLock()
			if stat, ok := s.lastStats[agentID]; ok {
				agentData["bytesPerSec"] = stat.BytesPerSec
				agentData["packetsPerSec"] = stat.PacketsPerSec
				agentData["errors"] = stat.TotalErrors
				agentData["totalBytes"] = stat.TotalBytes
				agentData["totalPackets"] = stat.TotalPackets
				agentData["uptime"] = stat.Uptime
			}
			s.lastStatsMu.RUnlock()
		}

		agentsCopy = append(agentsCopy, agentData)
	}

	// 按 order 排序
	sort.Slice(agentsCopy, func(i, j int) bool {
		orderI, _ := agentsCopy[i]["order"].(int)
		orderJ, _ := agentsCopy[j]["order"].(int)
		if orderI != orderJ {
			return orderI < orderJ
		}
		nameI, _ := agentsCopy[i]["name"].(string)
		nameJ, _ := agentsCopy[j]["name"].(string)
		return nameI < nameJ
	})

	result, _ := json.Marshal(map[string]interface{}{
		"agents": agentsCopy,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

func (s *MasterServer) handleUpdateAgentName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		AgentID string `json:"agent_id"`
		Name    string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	if req.AgentID == "" || req.Name == "" {
		http.Error(w, `{"msg":"参数不完整"}`, http.StatusBadRequest)
		return
	}

	s.agentsMu.Lock()
	agent, exists := s.agents[req.AgentID]
	if exists {
		agent.Name = req.Name
	}
	s.agentsMu.Unlock()

	if !exists {
		http.Error(w, `{"msg":"Agent不存在"}`, http.StatusNotFound)
		return
	}

	// 通过 WebSocket 通知 Agent 更新名称
	if agent.Conn != nil {
		updateMsg := map[string]interface{}{
			"type": "config_update",
			"data": map[string]interface{}{
				"agent_name": req.Name,
			},
		}
		agent.Conn.WriteJSON(updateMsg)
	}

	// 如果是本地 Agent，保存到 env
	if req.AgentID == "local" {
		s.config.AgentName = req.Name
		s.SaveEnv()
	}

	// 广播 Agent 列表更新
	s.broadcastAgentList()

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

func (s *MasterServer) handleUpdateAgentOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		Orders []struct {
			ID    string `json:"id"`
			Order int    `json:"order"`
		} `json:"orders"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	s.agentsMu.Lock()
	for _, order := range req.Orders {
		if agent, ok := s.agents[order.ID]; ok {
			agent.Order = order.Order
		}
	}
	s.agentsMu.Unlock()

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

func (s *MasterServer) handleStartTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	// 使用 map 接收，兼容各种类型
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	// 提取字段，兼容 string/int/float
	mode, _ := req["mode"].(string)
	target, _ := req["target"].(string)
	duration, _ := req["duration"].(string)
	
	workers := 0
	if v, ok := req["workers"].(float64); ok {
		workers = int(v)
	}
	
	size := 1400
	if v, ok := req["size"].(float64); ok {
		size = int(v)
	} else if v, ok := req["size"].(string); ok {
		size, _ = strconv.Atoi(v)
	}
	
	var agents []string
	if v, ok := req["agents"].([]interface{}); ok {
		for _, a := range v {
			if s, ok := a.(string); ok {
				agents = append(agents, s)
			}
		}
	}
	
	log.Printf("任务下发: mode=%s target=%s workers=%d size=%d agents=%v", mode, target, workers, size, agents)

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
	for _, agentID := range agents {
		if agent, ok := s.agents[agentID]; ok {
			if agent.Conn != nil {
				// 远程 Agent，通过 WebSocket 下发
				log.Printf("向 Agent %s (%s) 下发任务...", agentID, agent.Name)
				if err := agent.Conn.WriteJSON(startMsg); err != nil {
					log.Printf("向 Agent %s 下发任务失败: %v", agentID, err)
				} else {
					sent++
					agent.Running = true
					log.Printf("向 Agent %s 下发任务成功", agentID)
				}
			} else if agentID == "local" {
				// 本地 Agent，直接执行
				log.Printf("向本地 Agent 下发任务...")
				go s.runLocalTask(mode, target, workers, size, duration)
				sent++
			} else {
				// 远程 Agent 离线
				log.Printf("Agent %s 离线，无法下发任务", agentID)
			}
		} else {
			log.Printf("Agent %s 不存在", agentID)
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"msg":  fmt.Sprintf("已向 %d 个 Agent 下发任务", sent),
		"task_id": taskID,
	})
}

// runLocalTask 本地 Agent 执行任务
func (s *MasterServer) runLocalTask(taskMode, taskTarget string, taskWorkers, taskSize int, duration string) {
	log.Printf("本地任务启动: mode=%s target=%s workers=%d size=%d", taskMode, taskTarget, taskWorkers, taskSize)
	startTime := time.Now()
	
	// 重置统计
	atomic.StoreInt64(&totalBytes, 0)
	atomic.StoreInt64(&totalPackets, 0)
	atomic.StoreInt64(&totalErrors, 0)

	// 设置全局变量（通过指针修改）
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
	
	// 保存 cancel 函数用于停止
	s.localTaskCancel = cancel
	
	defer func() {
		cancel()
		s.localTaskCancel = nil
		log.Printf("本地任务结束")
	}()

	// 更新本地 Agent 状态
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

	lim := newLimiter(0) // 不限速
	var wg sync.WaitGroup

	// 启动连接清理器
	go connectionCleaner(ctx)

	// 启动统计更新 goroutine
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
				
				// 直接更新，不加锁
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

func (s *MasterServer) handleStopTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || !CheckPassword(token, s.config.PasswordHash) {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	// 接收可选的 agents 参数
	var req struct {
		Agents []string `json:"agents"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	
	log.Printf("停止任务请求: agents=%v, len=%d", req.Agents, len(req.Agents))

	stopMsg := map[string]string{"type": "stop"}

	// 先收集需要停止的 Agent
	s.agentsMu.RLock()
	agentsToStop := make([]string, 0)
	remoteAgents := make([]*AgentInfo, 0)
	
	if len(req.Agents) > 0 {
		for _, agentID := range req.Agents {
			if agent, ok := s.agents[agentID]; ok {
				if agent.Conn != nil {
					remoteAgents = append(remoteAgents, agent)
				} else if agentID == "local" {
					agentsToStop = append(agentsToStop, "local")
				}
			}
		}
	} else {
		for _, agent := range s.agents {
			if agent.Conn != nil {
				remoteAgents = append(remoteAgents, agent)
			} else if agent.ID == "local" {
				agentsToStop = append(agentsToStop, "local")
			}
		}
	}
	s.agentsMu.RUnlock()

	sent := 0
	
	// 停止远程 Agent
	for _, agent := range remoteAgents {
		if err := agent.Conn.WriteJSON(stopMsg); err != nil {
			log.Printf("向 Agent %s 下发停止指令失败: %v", agent.ID, err)
		} else {
			sent++
			agent.Running = false
		}
	}
	
	// 停止本地 Agent（不持有锁）
	for _, agentID := range agentsToStop {
		if agentID == "local" {
			s.stopLocalTask()
			sent++
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"msg": fmt.Sprintf("已向 %d 个 Agent 下发停止指令", sent),
	})
}

// stopLocalTask 停止本地任务
func (s *MasterServer) stopLocalTask() {
	if s.localTaskCancel != nil {
		s.localTaskCancel()
		s.localTaskCancel = nil
	}
	// 不要立即设置 Running = false，让 runLocalTask 的 defer 来设置
	// 这样前端轮询能看到真实的停止过程
}

func (s *MasterServer) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"master_host": s.config.AgentMasterHost,
		"master_port": s.config.AgentMasterPort,
		"agent_name":  s.config.AgentName,
	})
}

func (s *MasterServer) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractToken(r)
	if token == "" || token != s.config.AgentMasterToken {
		http.Error(w, `{"msg":"未授权"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		Host     string `json:"host"`
		Port     string `json:"port"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"msg":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	s.config.AgentMasterHost = req.Host
	s.config.AgentMasterPort = req.Port
	s.config.AgentMasterToken = req.Password

	if err := s.SaveEnv(); err != nil {
		http.Error(w, `{"msg":"保存配置失败"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

// WebSocket 处理

func (s *MasterServer) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket 升级失败: %v", err)
		return
	}
	defer conn.Close()

	// 设置 Ping/Pong 处理（保持连接活跃）
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})
	
	// 读取超时设置
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 等待注册消息
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

	// 注册成功
	conn.WriteJSON(map[string]string{"type": "auth_ok"})

	// 计算新 Agent 的排序值（当前最大值 + 1）
	s.agentsMu.RLock()
	maxOrder := 0
	for _, a := range s.agents {
		if a.Order > maxOrder {
			maxOrder = a.Order
		}
	}
	s.agentsMu.RUnlock()

	s.agentsMu.Lock()
	// 如果 agent 已存在（重连），保留运行状态
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

		// 首次连接，保存到 .env
		if s.config.RegisteredAgents == nil {
			s.config.RegisteredAgents = make(map[string]RegisteredAgent)
		}
		// 从 agentID 中提取 hostname（格式：hostname-pid）
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

	// 广播 Agent 列表更新
	s.broadcastAgentList()

	defer func() {
		s.agentsMu.Lock()
		// 不删除 agent，只标记为离线（断开连接）
		if agent, ok := s.agents[regMsg.AgentID]; ok {
			agent.Conn = nil
			// 保留 Running 状态，等重连后恢复
		}
		s.agentsMu.Unlock()

		s.broadcastAgentList()
		log.Printf("Agent 断开: %s", regMsg.AgentID)
	}()

	// 处理消息
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

func (s *MasterServer) handleAgentStatusMsg(agentID string, msg map[string]interface{}) {
	s.agentsMu.RLock()
	agent, ok := s.agents[agentID]
	s.agentsMu.RUnlock()

	if !ok {
		return
	}

	agent.LastSeen = time.Now()

	// 解析 data 字段（可能是字符串或 map）
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

	s.agentsMu.Lock()
	s.lastStats[agentID] = stats
	s.agentsMu.Unlock()

	// 广播统计
	s.broadcastStats()
}

func (s *MasterServer) handleStatsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Stats WebSocket 升级失败: %v", err)
		return
	}

	s.statsMu.Lock()
	s.statsWS[conn] = true
	s.statsMu.Unlock()

	log.Printf("Stats WebSocket 连接建立")

	defer func() {
		s.statsMu.Lock()
		delete(s.statsWS, conn)
		s.statsMu.Unlock()
		conn.Close()
	}()

	// 发送当前 Agent 列表
	s.sendAgentList(conn)

	// 保持连接
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (s *MasterServer) broadcastAgentList() {
	s.agentsMu.RLock()
	agents := make([]map[string]interface{}, 0)
	for _, agent := range s.agents {
		agents = append(agents, map[string]interface{}{
			"id":       agent.ID,
			"name":     agent.Name,
			"running":  agent.Running,
			"cpu":      agent.CPU,
			"selected": agent.Selected,
		})
	}
	s.agentsMu.RUnlock()

	msg := map[string]interface{}{
		"type":   "agents",
		"agents": agents,
	}

	s.statsMu.RLock()
	defer s.statsMu.RUnlock()

	for conn := range s.statsWS {
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("广播 Agent 列表失败: %v", err)
		}
	}
}

func (s *MasterServer) sendAgentList(conn *websocket.Conn) {
	s.agentsMu.RLock()
	agents := make([]map[string]interface{}, 0)
	for _, agent := range s.agents {
		agents = append(agents, map[string]interface{}{
			"id":       agent.ID,
			"name":     agent.Name,
			"running":  agent.Running,
			"cpu":      agent.CPU,
			"selected": agent.Selected,
		})
	}
	s.agentsMu.RUnlock()

	msg := map[string]interface{}{
		"type":   "agents",
		"agents": agents,
	}

	conn.WriteJSON(msg)
}

func (s *MasterServer) broadcastStats() {
	s.agentsMu.RLock()
	stats := make([]*AgentStats, 0)
	for _, stat := range s.lastStats {
		stats = append(stats, stat)
	}
	agentsCount := len(s.agents)
	s.agentsMu.RUnlock()

	msg := map[string]interface{}{
		"type":   "stats",
		"agents": stats,
	}

	s.statsMu.RLock()
	defer s.statsMu.RUnlock()

	if len(s.statsWS) > 0 {
		log.Printf("广播统计: %d 个连接, %d 个Agent, %d 条统计", len(s.statsWS), agentsCount, len(stats))
	}

	for conn := range s.statsWS {
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("广播统计失败: %v", err)
		}
	}
}

// 工具函数

func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// PromptPort 交互式询问端口
func PromptPort() string {
	fmt.Print("请输入面板端口 (默认 8443): ")
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

// RunMaster 运行主控模式
func RunMaster() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 如果没有端口，询问
	if server.config.Port == "" {
		server.config.Port = PromptPort()
		if err := server.SaveEnv(); err != nil {
			log.Fatalf("保存配置失败: %v", err)
		}
	}

	addr := "0.0.0.0:" + server.config.Port

	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│  主控已启动                                   │")
	fmt.Println("│                                             │")
	fmt.Printf("│  面板地址: https://%s              │\n", addr)
	fmt.Printf("│  被控连接地址: %s                  │\n", addr)
	fmt.Println("│                                             │")
	fmt.Println("│  首次使用请访问面板完成配置                    │")
	fmt.Println("└─────────────────────────────────────────────┘")
	fmt.Println()

	if err := server.Start(addr); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}
}

// RunAgent 运行被控模式
func RunAgent() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 如果没有端口，询问
	if server.config.Port == "" {
		server.config.Port = PromptPort()
		if err := server.SaveEnv(); err != nil {
			log.Fatalf("保存配置失败: %v", err)
		}
	}

	addr := "0.0.0.0:" + server.config.Port

	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│  被控已启动                                   │")
	fmt.Println("│                                             │")
	fmt.Printf("│  面板地址: https://%s              │\n", addr)
	fmt.Println("│  （面板仅用于修改连接配置）                    │")
	fmt.Println("└─────────────────────────────────────────────┘")
	fmt.Println()

	// 启动 WebSocket 连接到主控
	if server.config.AgentMasterHost != "" && server.config.AgentMasterPort != "" {
		go connectToMaster(server)
	}

	if err := server.Start(addr); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}
}

// RunBoth 运行主控+被控模式
func RunBoth() {
	server := NewMasterServer()

	if err := server.LoadEnv(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 如果没有端口，询问
	if server.config.Port == "" {
		server.config.Port = PromptPort()
		if err := server.SaveEnv(); err != nil {
			log.Fatalf("保存配置失败: %v", err)
		}
	}

	addr := "0.0.0.0:" + server.config.Port

	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│  主控+被控已启动                              │")
	fmt.Println("│                                             │")
	fmt.Printf("│  面板地址: https://%s              │\n", addr)
	fmt.Printf("│  被控连接地址: %s                  │\n", addr)
	fmt.Println("│                                             │")
	fmt.Println("│  首次使用请访问面板完成配置                    │")
	fmt.Println("└─────────────────────────────────────────────┘")
	fmt.Println()

	// 注册本地 Agent
	hostname, _ := os.Hostname()
	localAgent := &AgentInfo{
		ID:       "local",
		Name:     hostname + " (本机)",
		Selected: true,
		LastSeen: time.Now(),
	}
	server.agents["local"] = localAgent

	if err := server.Start(addr); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}
}

func connectToMaster(server *MasterServer) {
	masterURL := fmt.Sprintf("ws://%s:%s/ws", server.config.AgentMasterHost, server.config.AgentMasterPort)
	log.Printf("连接主控: %s", masterURL)

	hostname, _ := os.Hostname()
	agentName := server.config.AgentName
	if agentName == "" {
		agentName = hostname
	}
	agentID := hostname  // 使用 hostname 作为唯一标识，重启不会变

	// 重连循环
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

	// 注册
	reg := AgentMessage{
		Type:    "register",
		AgentID: agentID,
		Name:    agentName,
		Token:   token,
	}
	if err := conn.WriteJSON(reg); err != nil {
		return fmt.Errorf("注册失败: %v", err)
	}

	// 等待鉴权结果
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

	// 设置心跳
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	// 接收主控指令
	for {
		var cmd MasterCommand
		if err := conn.ReadJSON(&cmd); err != nil {
			return fmt.Errorf("读取消息失败: %v", err)
		}

		switch cmd.Type {
		case "start":
			// 解析任务配置
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
			// 异步执行任务，传递 conn 和 agentID 用于状态上报
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
				// 不要立即设置 remoteRunning = false
				// 让 runRemoteTask 的 defer 来设置
			}
			remoteTaskMu.Unlock()
		}
	}
}

// runRemoteTask 远程 Agent 执行任务
func (s *MasterServer) runRemoteTask(taskMode, taskTarget string, taskWorkers, taskSize int, duration string, conn *websocket.Conn, agentID string) {
	log.Printf("开始执行任务: mode=%s target=%s workers=%d size=%d", taskMode, taskTarget, taskWorkers, taskSize)

	// 重置统计
	atomic.StoreInt64(&totalBytes, 0)
	atomic.StoreInt64(&totalPackets, 0)
	atomic.StoreInt64(&totalErrors, 0)

	// 设置全局变量
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

	// 保存 cancel 函数用于停止，并更新 agent 状态
	remoteTaskMu.Lock()
	remoteTaskCancel = cancel
	remoteRunning = true
	remoteTaskMu.Unlock()
	
	// 更新 agent 的 Running 状态
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
		
		// 更新 agent 的 Running 状态
		s.agentsMu.Lock()
		if agent, ok := s.agents[agentID]; ok {
			agent.Running = false
		}
		s.agentsMu.Unlock()
	}()

	startTime := time.Now()
	lim := newLimiter(0)
	var wg sync.WaitGroup

	// 启动连接清理器
	go connectionCleaner(ctx)

	// 启动统计上报（通过 WebSocket 发送给主控）
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

				// 通过 WebSocket 发送状态给主控（忽略错误）
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
				conn.WriteJSON(statusMsg) // 忽略错误，任务继续运行
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
