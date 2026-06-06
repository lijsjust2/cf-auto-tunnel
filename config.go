package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TunnelConfig 隧道配置
type TunnelConfig struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	TunnelID     string        `json:"tunnelId"`     // Cloudflare Tunnel ID
	TunnelToken  string        `json:"tunnelToken"`  // Tunnel Token (用于 cloudflared 运行)
	PublicDomain string        `json:"publicDomain"` // 公开访问域名 如 1panel.wqqq.eu.org
	Subdomain    string        `json:"subdomain"`    // 子域名 如 1panel
	ZoneID       string        `json:"zoneId"`
	ZoneName     string        `json:"zoneName"`
	ServiceURL   string        `json:"serviceUrl"` // 本地服务地址 如 http://localhost:9999
	Protocol     string        `json:"protocol"`   // http / https / tcp / ssh
	Status       string        `json:"status"`     // running / stopped / error
	CreatedAt    string        `json:"createdAt"`
	UpdatedAt    string        `json:"updatedAt"`
}

// PreferredIP 优选IP
type PreferredIP struct {
	Value    string `json:"value"`    // 原始值（IP或域名）
	Type     string `json:"type"`     // ip / domain
	Resolved string `json:"resolved"` // 解析后的IP
}

// AppConfig 应用配置
type AppConfig struct {
	APIToken     string        `json:"apiToken"`
	ZoneID       string        `json:"zoneId"`
	ZoneName     string        `json:"zoneName"`
	Tunnels      []TunnelConfig `json:"tunnels"`
	PreferredIPs []PreferredIP `json:"preferredIPs"`
}

// ConfigManager 配置管理器
type ConfigManager struct {
	dataDir string
	mu      sync.RWMutex
}

func NewConfigManager(dataDir string) *ConfigManager {
	return &ConfigManager{dataDir: dataDir}
}

func (cm *ConfigManager) configFilePath() string {
	return filepath.Join(cm.dataDir, "config.json")
}

func (cm *ConfigManager) Load() (*AppConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	b, err := os.ReadFile(cm.configFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &AppConfig{}, nil
		}
		return nil, err
	}
	var cfg AppConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cm *ConfigManager) Save(cfg *AppConfig) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cm.configFilePath(), b, 0644)
}

func (cm *ConfigManager) GetTunnel(id string) (*TunnelConfig, error) {
	cfg, err := cm.Load()
	if err != nil {
		return nil, err
	}
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].ID == id {
			return &cfg.Tunnels[i], nil
		}
	}
	return nil, fmt.Errorf("tunnel not found: %s", id)
}

func (cm *ConfigManager) AddTunnel(tunnel TunnelConfig) error {
	cfg, err := cm.Load()
	if err != nil {
		return err
	}
	tunnel.ID = generateID()
	tunnel.CreatedAt = time.Now().Format(time.RFC3339)
	tunnel.UpdatedAt = tunnel.CreatedAt
	cfg.Tunnels = append(cfg.Tunnels, tunnel)
	return cm.Save(cfg)
}

func (cm *ConfigManager) UpdateTunnel(id string, tunnel TunnelConfig) error {
	cfg, err := cm.Load()
	if err != nil {
		return err
	}
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].ID == id {
			tunnel.ID = id
			tunnel.CreatedAt = cfg.Tunnels[i].CreatedAt
			tunnel.UpdatedAt = time.Now().Format(time.RFC3339)
			cfg.Tunnels[i] = tunnel
			return cm.Save(cfg)
		}
	}
	return fmt.Errorf("tunnel not found: %s", id)
}

func (cm *ConfigManager) DeleteTunnel(id string) error {
	cfg, err := cm.Load()
	if err != nil {
		return err
	}
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].ID == id {
			cfg.Tunnels = append(cfg.Tunnels[:i], cfg.Tunnels[i+1:]...)
			return cm.Save(cfg)
		}
	}
	return fmt.Errorf("tunnel not found: %s", id)
}

func (cm *ConfigManager) UpdateTunnelStatus(id string, status string) error {
	cfg, err := cm.Load()
	if err != nil {
		return err
	}
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].ID == id {
			cfg.Tunnels[i].Status = status
			cfg.Tunnels[i].UpdatedAt = time.Now().Format(time.RFC3339)
			return cm.Save(cfg)
		}
	}
	return fmt.Errorf("tunnel not found: %s", id)
}

func (cm *ConfigManager) AddPreferredIP(ip PreferredIP) error {
	cfg, err := cm.Load()
	if err != nil {
		return err
	}
	for _, existing := range cfg.PreferredIPs {
		if existing.Value == ip.Value {
			return fmt.Errorf("该IP/域名已存在")
		}
	}
	cfg.PreferredIPs = append(cfg.PreferredIPs, ip)
	return cm.Save(cfg)
}

func (cm *ConfigManager) RemovePreferredIP(index int) error {
	cfg, err := cm.Load()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(cfg.PreferredIPs) {
		return fmt.Errorf("索引越界")
	}
	cfg.PreferredIPs = append(cfg.PreferredIPs[:index], cfg.PreferredIPs[index+1:]...)
	return cm.Save(cfg)
}

// GenerateCloudflaredConfig 生成 cloudflared 配置文件
func (cm *ConfigManager) GenerateCloudflaredConfig(tunnel *TunnelConfig) string {
	var b strings.Builder
	b.WriteString("tunnel: " + tunnel.TunnelID + "\n")
	b.WriteString("credentials-file: /app/data/" + tunnel.TunnelID + ".json\n\n")
	b.WriteString("ingress:\n")
	b.WriteString("  - hostname: " + tunnel.PublicDomain + "\n")
	b.WriteString("    service: " + tunnel.ServiceURL + "\n")
	b.WriteString("  - service: http_status:404\n")
	return b.String()
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
