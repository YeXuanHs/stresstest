// agent.go - WebSocket Agent 模式
// 启动后主动连接主控，接收任务并上报状态
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	agentMode   = flag.Bool("agent", false, "以 Agent 模式运行，主动连接主控")
	masterURL   = flag.String("master", "", "主控地址 (host:port)")
	agentID     = flag.String("agent-id", "", "Agent 标识（默认自动生成）")
	agentName   = flag.String("agent-name", "", "Agent 显示名称")
	agentToken  = flag.String("token", "", "鉴权 token")
)

// AgentMessage 与主控通信的消息格式
type AgentMessage struct {
	Type    string          `json:"type"` // register / status / result / log
	AgentID string          `json:"agent_id"`
	Name    string          `json:"name,omitempty"`
	Token   string          `json:"token,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// MasterCommand 主控下发的指令
type MasterCommand struct {
	Type   string          `json:"type"` // start / stop / config_update / auth_ok / auth_fail
	TaskID string          `json:"task_id,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Msg    string          `json:"msg,omitempty"`
}

// TaskConfig 任务配置
type TaskConfig struct {
	Mode       string  `json:"mode"`
	Target     string  `json:"target"`
	Workers    int     `json:"workers"`
	Duration   string  `json:"duration"`
	Size       int     `json:"size"`
	Rate       float64 `json:"rate"`
	MCAction   string  `json:"mc_action"`
	MCProtocol int     `json:"mc_protocol"`
}

// StatusReport 状态上报
type StatusReport struct {
	Running       bool    `json:"running"`
	Mode          string  `json:"mode"`
	Target        string  `json:"target"`
	Workers       int     `json:"workers"`
	TotalBytes    int64   `json:"total_bytes"`
	TotalPackets  int64   `json:"total_packets"`
	TotalErrors   int64   `json:"total_errors"`
	BytesPerSec   float64 `json:"bytes_per_sec"`
	PacketsPerSec float64 `json:"packets_per_sec"`
	MCActive      int64   `json:"mc_active"`
	MCLoginOK     int64   `json:"mc_login_ok"`
	MCLoginFail   int64   `json:"mc_login_fail"`
	CPU           int     `json:"cpu"`
	Uptime        string  `json:"uptime"`
}

// ConfigUpdateData 配置更新数据
type ConfigUpdateData struct {
	AgentName string `json:"agent_name"`
}

func runAgent() {
	hostname, _ := os.Hostname()
	
	id := *agentID
	if id == "" {
		id = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}
	
	// 默认名称是 hostname，如果 env 中有自定义名称则使用自定义的
	name := hostname

	masterAddr := *masterURL
	if masterAddr == "" {
		// 从 .env 读取
		server := NewMasterServer()
		if err := server.LoadEnv(); err == nil {
			if server.config.AgentMasterHost != "" && server.config.AgentMasterPort != "" {
				masterAddr = server.config.AgentMasterHost + ":" + server.config.AgentMasterPort
			}
			if server.config.AgentMasterToken != "" && *agentToken == "" {
				*agentToken = server.config.AgentMasterToken
			}
			// 如果 env 中有自定义名称，使用自定义名称
			if server.config.AgentName != "" {
				name = server.config.AgentName
			}
		}
	}
	
	// 命令行参数优先
	if *agentName != "" {
		name = *agentName
	}

	if masterAddr == "" {
		log.Fatal("请指定主控地址: -master host:port")
	}

	if *agentToken == "" {
		log.Fatal("请指定鉴权 token: -token xxx")
	}

	// 确保有 ws 前缀
	if !strings.HasPrefix(masterAddr, "ws://") && !strings.HasPrefix(masterAddr, "wss://") {
		masterAddr = "ws://" + masterAddr
	}

	u, err := url.Parse(masterAddr + "/ws")
	if err != nil {
		log.Fatalf("主控地址无效: %v", err)
	}

	fmt.Printf("Agent 启动中... ID=%s  连接主控: %s\n", id, u.String())

	// 重连循环
	for {
		err := connectAndRun(u.String(), id, name, *agentToken)
		if err != nil {
			log.Printf("连接断开: %v，3秒后重连...", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func connectAndRun(masterWSURL, id, name, token string) error {
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.Dial(masterWSURL, nil)
	if err != nil {
		return fmt.Errorf("连接主控失败: %v", err)
	}
	defer conn.Close()

	// 注册
	reg := AgentMessage{
		Type:    "register",
		AgentID: id,
		Name:    name,
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

	fmt.Println("已连接到主控，等待任务...")

	var (
		taskCancel context.CancelFunc
		taskMu     sync.Mutex
		startTime  = time.Now()
		currentCfg TaskConfig
		running    bool
	)

	// 设置心跳保活 - 每30秒发送 Ping
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})
	
	// 定期发送 Ping 保活（防止 NAT 超时）
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				log.Printf("发送心跳失败: %v", err)
				return
			}
		}
	}()

	// 状态上报 - 运行中 200ms，空闲 5s
	go func() {
		for {
			taskMu.Lock()
			isRunning := running
			taskMu.Unlock()

			if isRunning {
				time.Sleep(200 * time.Millisecond)
			} else {
				time.Sleep(5 * time.Second)
			}

			taskMu.Lock()
			st := StatusReport{
				Running:      running,
				Mode:         currentCfg.Mode,
				Target:       currentCfg.Target,
				Workers:      currentCfg.Workers,
				TotalBytes:   atomic.LoadInt64(&totalBytes),
				TotalPackets: atomic.LoadInt64(&totalPackets),
				TotalErrors:  atomic.LoadInt64(&totalErrors),
				MCActive:     atomic.LoadInt64(&mcActiveConns),
				MCLoginOK:    atomic.LoadInt64(&mcLoginOK),
				MCLoginFail:  atomic.LoadInt64(&mcLoginFailed),
				CPU:          runtime.NumCPU(),
				Uptime:       time.Since(startTime).Round(time.Second).String(),
			}
			taskMu.Unlock()

			data, _ := json.Marshal(st)
			msg := AgentMessage{Type: "status", AgentID: id, Data: data}
			if err := conn.WriteJSON(msg); err != nil {
				log.Printf("状态上报失败: %v", err)
				return
			}
		}
	}()

	// 接收主控指令
	for {
		var cmd MasterCommand
		if err := conn.ReadJSON(&cmd); err != nil {
			return fmt.Errorf("读取消息失败: %v", err)
		}

		switch cmd.Type {
		case "start":
			var cfg TaskConfig
			if err := json.Unmarshal(cmd.Config, &cfg); err != nil {
				log.Printf("任务配置解析失败: %v", err)
				continue
			}
			taskMu.Lock()
			if running {
				taskMu.Unlock()
				log.Println("已有任务在运行，先停止再启动")
				continue
			}
			// 应用配置到全局 flag 变量
			*mode = cfg.Mode
			*target = cfg.Target
			*workers = cfg.Workers
			if cfg.Size > 0 {
				*payloadSize = cfg.Size
			}
			*rateMBs = cfg.Rate
			if cfg.MCAction != "" {
				*mcAction = cfg.MCAction
			}
			if cfg.MCProtocol > 0 {
				*mcProtocol = cfg.MCProtocol
			}
			var dur time.Duration
			if cfg.Duration != "" {
				if cfg.Duration == "0" {
					dur = 0
				} else {
					dur, _ = time.ParseDuration(cfg.Duration + "s")
				}
			}
			*duration = dur

			ctx, cancel := context.WithCancel(context.Background())
			if dur > 0 {
				ctx, cancel = context.WithTimeout(ctx, dur)
			}
			taskCancel = cancel
			running = true
			currentCfg = cfg
			taskMu.Unlock()

			// 重置统计
			atomic.StoreInt64(&totalBytes, 0)
			atomic.StoreInt64(&totalPackets, 0)
			atomic.StoreInt64(&totalErrors, 0)
			atomic.StoreInt64(&mcActiveConns, 0)
			atomic.StoreInt64(&mcLoginOK, 0)
			atomic.StoreInt64(&mcLoginFailed, 0)
			atomic.StoreInt64(&mcKeepAlives, 0)
			atomic.StoreInt64(&mcStatusOK, 0)

			go func() {
				runTask(ctx, cfg)
				taskMu.Lock()
				running = false
				taskMu.Unlock()
				log.Println("任务结束")
			}()
			log.Printf("收到启动任务: mode=%s target=%s workers=%d", cfg.Mode, cfg.Target, cfg.Workers)

		case "stop":
			taskMu.Lock()
			if taskCancel != nil {
				taskCancel()
				taskCancel = nil
			}
			running = false
			taskMu.Unlock()
			log.Println("收到停止指令")

		case "config_update":
			var updateData ConfigUpdateData
			if err := json.Unmarshal(cmd.Data, &updateData); err != nil {
				log.Printf("配置更新数据解析失败: %v", err)
				continue
			}
			if updateData.AgentName != "" {
				name = updateData.AgentName
				// 更新 .env
				server := NewMasterServer()
				if err := server.LoadEnv(); err == nil {
					server.config.AgentName = name
					server.SaveEnv()
				}
				log.Printf("名称已更新: %s", name)
			}
		}
	}
}

// runTask 根据配置启动对应的压测 workers
func runTask(ctx context.Context, cfg TaskConfig) {
	lim := newLimiter(cfg.Rate)
	var wg sync.WaitGroup

	n := cfg.Workers
	if n <= 0 {
		n = runtime.NumCPU() * 4
	}

	for i := 0; i < n; i++ {
		wg.Add(1)
		switch cfg.Mode {
		case "tcp":
			go tcpWorker(ctx, &wg, i, lim)
		case "udp":
			go udpWorker(ctx, &wg, i, lim)
		case "http":
			go httpWorker(ctx, &wg, i, lim)
		case "mc":
			go mcWorker(ctx, &wg, i)
		case "flood":
			if *floodProto == "udp" {
				if !*floodChurn && *floodBatch > 1 {
					go udpFloodBatchWorker(ctx, &wg, i, *floodBatch)
				} else {
					go udpFloodWorker(ctx, &wg, i)
				}
			} else if *floodProto == "http" {
				go httpFloodWorker(ctx, &wg, i)
			} else {
				go floodWorker(ctx, &wg, i)
			}
		case "icmp":
			go icmpWorker(ctx, &wg, i)
		default:
			log.Printf("未知模式: %s", cfg.Mode)
			wg.Done()
		}
	}
	wg.Wait()
}

func initAgentFlags() {
	// 已经在 var 里定义了
}
