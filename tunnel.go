package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// TunnelProcess 隧道进程管理器
type TunnelProcess struct {
	mu       sync.Mutex
	processes map[string]*exec.Cmd // tunnelID -> cmd
	dataDir  string
	cloudflaredPath string
}

func NewTunnelProcess(dataDir, cloudflaredPath string) *TunnelProcess {
	return &TunnelProcess{
		processes: make(map[string]*exec.Cmd),
		dataDir:   dataDir,
		cloudflaredPath: cloudflaredPath,
	}
}

// StartTunnel 使用 Tunnel Token 启动 cloudflared
func (tp *TunnelProcess) StartTunnel(tunnelID, tunnelToken string) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	// 如果已经在运行，先停止
	if cmd, exists := tp.processes[tunnelID]; exists {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
			time.Sleep(time.Second)
		}
		delete(tp.processes, tunnelID)
	}

	// 使用 token 方式运行 cloudflared
	args := []string{"tunnel", "--no-autoupdate", "run", "--token", tunnelToken}

	cmd := exec.Command(tp.cloudflaredPath, args...)
	cmd.Env = append(os.Environ(), "TUNNEL_ORIGIN_CERT="+filepath.Join(tp.dataDir))

	// 将日志输出到文件
	logFile := filepath.Join(tp.dataDir, tunnelID+".log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("无法创建日志文件: %v", err)
	}
	cmd.Stdout = f
	cmd.Stderr = f

	if err := cmd.Start(); err != nil {
		f.Close()
		return fmt.Errorf("启动 cloudflared 失败: %v", err)
	}

	tp.processes[tunnelID] = cmd

	// 异步等待进程结束
	go func() {
		err := cmd.Wait()
		if err != nil {
			log.Printf("Tunnel %s 进程退出: %v", tunnelID, err)
		}
		tp.mu.Lock()
		delete(tp.processes, tunnelID)
		tp.mu.Unlock()
		f.Close()
	}()

	log.Printf("Tunnel %s 已启动, PID: %d", tunnelID, cmd.Process.Pid)
	return nil
}

// StopTunnel 停止隧道
func (tp *TunnelProcess) StopTunnel(tunnelID string) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	cmd, exists := tp.processes[tunnelID]
	if !exists {
		return fmt.Errorf("隧道未运行")
	}

	if cmd.Process != nil {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			cmd.Process.Kill()
		}
	}
	delete(tp.processes, tunnelID)
	log.Printf("Tunnel %s 已停止", tunnelID)
	return nil
}

// IsRunning 检查隧道是否在运行
func (tp *TunnelProcess) IsRunning(tunnelID string) bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	cmd, exists := tp.processes[tunnelID]
	if !exists {
		return false
	}
	// 检查进程是否还在运行
	if cmd.Process != nil {
		// 发送 signal 0 检查进程是否存在
		err := cmd.Process.Signal(syscall.Signal(0))
		return err == nil
	}
	return false
}

// GetTunnelLog 获取隧道日志
func (tp *TunnelProcess) GetTunnelLog(tunnelID string) (string, error) {
	logFile := filepath.Join(tp.dataDir, tunnelID+".log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "暂无日志", nil
		}
		return "", err
	}
	logContent := string(data)
	if len(logContent) > 5000 {
		logContent = logContent[len(logContent)-5000:]
	}
	return logContent, nil
}

// StopAll 停止所有隧道
func (tp *TunnelProcess) StopAll() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	for tunnelID, cmd := range tp.processes {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
		delete(tp.processes, tunnelID)
	}
}

// AutoStartTunnels 自动启动所有隧道
func (tp *TunnelProcess) AutoStartTunnels(configMgr *ConfigManager) {
	cfg, err := configMgr.Load()
	if err != nil {
		log.Printf("加载配置失败: %v", err)
		return
	}

	for i := range cfg.Tunnels {
		tunnel := &cfg.Tunnels[i]
		if tunnel.TunnelToken != "" {
			log.Printf("自动启动隧道: %s (%s)", tunnel.Name, tunnel.TunnelID)
			if err := tp.StartTunnel(tunnel.TunnelID, tunnel.TunnelToken); err != nil {
				log.Printf("自动启动隧道 %s 失败: %v", tunnel.Name, err)
				tunnel.Status = "error"
			} else {
				tunnel.Status = "running"
			}
		}
	}
	configMgr.Save(cfg)
}

// GetRunningCount 获取运行中的隧道数量
func (tp *TunnelProcess) GetRunningCount() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	count := 0
	for tunnelID, cmd := range tp.processes {
		if cmd.Process != nil {
			err := cmd.Process.Signal(syscall.Signal(0))
			if err == nil {
				count++
			} else {
				delete(tp.processes, tunnelID)
			}
		}
	}
	return count
}

// HealthCheck 健康检查，清理已退出的进程
func (tp *TunnelProcess) HealthCheck(ctx context.Context, configMgr *ConfigManager) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg, err := configMgr.Load()
			if err != nil {
				continue
			}
			changed := false
			for i := range cfg.Tunnels {
				tunnel := &cfg.Tunnels[i]
				running := tp.IsRunning(tunnel.TunnelID)
				expectedStatus := "stopped"
				if running {
					expectedStatus = "running"
				}
				if tunnel.Status != expectedStatus {
					tunnel.Status = expectedStatus
					changed = true
				}
			}
			if changed {
				configMgr.Save(cfg)
			}
		}
	}
}
