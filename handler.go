package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Handler HTTP 请求处理器
type Handler struct {
	configMgr  *ConfigManager
	tunnelProc *TunnelProcess
	authMgr    *AuthManager
}

func NewHandler(configMgr *ConfigManager, tunnelProc *TunnelProcess, authMgr *AuthManager) *Handler {
	return &Handler{
		configMgr:  configMgr,
		tunnelProc: tunnelProc,
		authMgr:    authMgr,
	}
}

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) handleError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]interface{}{
		"success": false,
		"message": message,
	})
}

func (h *Handler) handleSuccess(w http.ResponseWriter, data interface{}) {
	resp := map[string]interface{}{
		"success": true,
	}
	if m, ok := data.(map[string]interface{}); ok {
		for k, v := range m {
			resp[k] = v
		}
	} else {
		resp["data"] = data
	}
	jsonResponse(w, 200, resp)
}

// ==================== 认证 ====================

func (h *Handler) AuthStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := h.configMgr.Load()
	hasPassword := cfg.APIToken != "" || h.authMgr.HasPassword()
	jsonResponse(w, 200, map[string]interface{}{
		"success":     true,
		"hasPassword": hasPassword,
	})
}

func (h *Handler) AuthSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}
	if len(req.Password) < 4 {
		h.handleError(w, 400, "密码至少4位")
		return
	}
	if err := h.authMgr.SetPassword(req.Password); err != nil {
		h.handleError(w, 500, "设置密码失败")
		return
	}
	h.handleSuccess(w, nil)
}

func (h *Handler) AuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}
	if !h.authMgr.CheckPassword(req.Password) {
		h.handleError(w, 401, "密码错误")
		return
	}
	token := h.authMgr.GenerateToken()
	http.SetCookie(w, &http.Cookie{
		Name:     "cf_tunnel_auth",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	h.handleSuccess(w, nil)
}

func (h *Handler) AuthChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}
	if !h.authMgr.CheckPassword(req.OldPassword) {
		h.handleError(w, 401, "原密码错误")
		return
	}
	if len(req.NewPassword) < 4 {
		h.handleError(w, 400, "新密码至少4位")
		return
	}
	if err := h.authMgr.SetPassword(req.NewPassword); err != nil {
		h.handleError(w, 500, "设置密码失败")
		return
	}
	h.handleSuccess(w, nil)
}

// ==================== Cloudflare API ====================

func (h *Handler) TestAPIToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	cf := NewCloudflareAPI(req.Token)
	zones, err := cf.GetZones()
	if err != nil {
		h.handleError(w, 400, "API Token 验证失败: "+err.Error())
		return
	}

	// 保存 token
	cfg, _ := h.configMgr.Load()
	cfg.APIToken = req.Token
	h.configMgr.Save(cfg)

	h.handleSuccess(w, map[string]interface{}{
		"message": "连接成功",
		"zones":   len(zones),
	})
}

func (h *Handler) GetZones(w http.ResponseWriter, r *http.Request) {
	cfg, _ := h.configMgr.Load()
	if cfg.APIToken == "" {
		h.handleError(w, 400, "请先配置API Token")
		return
	}

	cf := NewCloudflareAPI(cfg.APIToken)
	zones, err := cf.GetZones()
	if err != nil {
		h.handleError(w, 500, err.Error())
		return
	}

	jsonResponse(w, 200, map[string]interface{}{
		"success": true,
		"zones":   zones,
	})
}

func (h *Handler) SelectZone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID   string `json:"zoneId"`
		ZoneName string `json:"zoneName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	cfg, _ := h.configMgr.Load()
	cfg.ZoneID = req.ZoneID
	cfg.ZoneName = req.ZoneName
	h.configMgr.Save(cfg)

	h.handleSuccess(w, nil)
}

// ==================== 隧道管理 ====================

func (h *Handler) CreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`       // 隧道名称
		Subdomain  string `json:"subdomain"`  // 子域名 如 1panel
		ServiceURL string `json:"serviceUrl"` // 本地服务地址 如 http://localhost:9999
		Protocol   string `json:"protocol"`   // http / https
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	cfg, _ := h.configMgr.Load()
	if cfg.APIToken == "" {
		h.handleError(w, 400, "请先配置API Token")
		return
	}
	if cfg.ZoneID == "" {
		h.handleError(w, 400, "请先选择域名")
		return
	}
	if req.Subdomain == "" {
		h.handleError(w, 400, "请输入子域名")
		return
	}
	if req.ServiceURL == "" {
		h.handleError(w, 400, "请输入本地服务地址")
		return
	}

	cf := NewCloudflareAPI(cfg.APIToken)

	// 1. 获取 Account ID
	accountID, err := cf.GetAccountID(cfg.ZoneID)
	if err != nil {
		h.handleError(w, 500, "获取Account ID失败: "+err.Error())
		return
	}

	// 2. 创建 Tunnel（config_src=cloudflare，远程配置）
	tunnelName := req.Name
	if tunnelName == "" {
		tunnelName = req.Subdomain
	}
	tunnel, err := cf.CreateTunnel(accountID, tunnelName)
	if err != nil {
		h.handleError(w, 500, "创建隧道失败: "+err.Error())
		return
	}

	// 3. 获取 Tunnel Token（创建响应中可能已包含，也单独获取确保拿到）
	tunnelToken := tunnel.Token
	if tunnelToken == "" {
		token, err := cf.GetTunnelToken(accountID, tunnel.ID)
		if err != nil {
			log.Printf("获取Tunnel Token失败: %v，尝试继续", err)
		} else {
			tunnelToken = token
		}
	}

	// 4. 配置 Tunnel 的 ingress 规则（关键步骤！）
	// 告诉 Cloudflare：收到 publicDomain 的请求时，转发到 serviceURL
	publicDomain := req.Subdomain + "." + cfg.ZoneName
	if err := cf.ConfigureTunnel(accountID, tunnel.ID, publicDomain, req.ServiceURL); err != nil {
		log.Printf("配置隧道ingress规则失败: %v", err)
		// 不返回错误，继续尝试创建DNS记录
	}

	// 5. 创建 CNAME DNS 记录指向隧道（始终用CNAME，优选IP单独管理）
	cnameTarget := tunnel.ID + ".cfargotunnel.com"
	_, err = cf.CreateDNSRecord(cfg.ZoneID, "CNAME", req.Subdomain, cnameTarget, true)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "Record already exists") {
			records, _ := cf.GetDNSRecords(cfg.ZoneID)
			for _, rec := range records {
				if rec.Name == publicDomain {
					cf.UpdateDNSRecord(cfg.ZoneID, rec.ID, "CNAME", req.Subdomain, cnameTarget, true)
					break
				}
			}
		} else {
			log.Printf("创建CNAME记录失败: %v", err)
		}
	}

	// 6. 设置 SSL
	protocol := req.Protocol
	if protocol == "" {
		protocol = "http"
	}
	if strings.HasPrefix(req.ServiceURL, "https://") {
		protocol = "https"
	}
	sslMode := "flexible"
	if protocol == "https" {
		sslMode = "full"
	}
	cf.UpdateSSLSetting(cfg.ZoneID, sslMode)

	// 7. 保存隧道配置
	tunnelCfg := TunnelConfig{
		Name:         tunnelName,
		TunnelID:     tunnel.ID,
		TunnelToken:  tunnelToken,
		PublicDomain: publicDomain,
		Subdomain:    req.Subdomain,
		ZoneID:       cfg.ZoneID,
		ZoneName:     cfg.ZoneName,
		ServiceURL:   req.ServiceURL,
		Protocol:     protocol,
		Status:       "stopped",
	}
	h.configMgr.AddTunnel(tunnelCfg)

	// 8. 自动启动隧道
	if tunnelToken != "" {
		if err := h.tunnelProc.StartTunnel(tunnel.ID, tunnelToken); err != nil {
			log.Printf("启动隧道失败: %v", err)
			h.configMgr.UpdateTunnelStatus(tunnel.ID, "error")
		} else {
			h.configMgr.UpdateTunnelStatus(tunnel.ID, "running")
		}
	}

	jsonResponse(w, 200, map[string]interface{}{
		"success": true,
		"message": "隧道创建成功",
		"tunnel": map[string]interface{}{
			"id":           tunnel.ID,
			"name":         tunnelName,
			"publicDomain": publicDomain,
			"serviceUrl":   req.ServiceURL,
		},
	})
}

func (h *Handler) ListTunnels(w http.ResponseWriter, r *http.Request) {
	cfg, _ := h.configMgr.Load()

	// 更新运行状态
	for i := range cfg.Tunnels {
		tunnel := &cfg.Tunnels[i]
		if tunnel.TunnelID != "" {
			running := h.tunnelProc.IsRunning(tunnel.TunnelID)
			if running {
				tunnel.Status = "running"
			} else if tunnel.Status == "running" {
				tunnel.Status = "stopped"
			}
		}
	}
	h.configMgr.Save(cfg)

	jsonResponse(w, 200, map[string]interface{}{
		"success": true,
		"tunnels": cfg.Tunnels,
	})
}

func (h *Handler) StartTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		// 兼容旧版 Go
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 5 {
			id = parts[4]
		}
	}

	tunnel, err := h.configMgr.GetTunnel(id)
	if err != nil {
		h.handleError(w, 404, "隧道不存在")
		return
	}

	if tunnel.TunnelToken == "" {
		h.handleError(w, 400, "隧道缺少Token，无法启动")
		return
	}

	if err := h.tunnelProc.StartTunnel(tunnel.TunnelID, tunnel.TunnelToken); err != nil {
		h.configMgr.UpdateTunnelStatus(id, "error")
		h.handleError(w, 500, "启动失败: "+err.Error())
		return
	}

	h.configMgr.UpdateTunnelStatus(id, "running")
	h.handleSuccess(w, map[string]interface{}{
		"message": "隧道已启动",
	})
}

func (h *Handler) StopTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 5 {
			id = parts[4]
		}
	}

	tunnel, err := h.configMgr.GetTunnel(id)
	if err != nil {
		h.handleError(w, 404, "隧道不存在")
		return
	}

	if err := h.tunnelProc.StopTunnel(tunnel.TunnelID); err != nil {
		h.handleError(w, 500, "停止失败: "+err.Error())
		return
	}

	h.configMgr.UpdateTunnelStatus(id, "stopped")
	h.handleSuccess(w, map[string]interface{}{
		"message": "隧道已停止",
	})
}

func (h *Handler) DeleteTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 5 {
			id = parts[4]
		}
	}

	tunnel, err := h.configMgr.GetTunnel(id)
	if err != nil {
		h.handleError(w, 404, "隧道不存在")
		return
	}

	// 停止隧道
	h.tunnelProc.StopTunnel(tunnel.TunnelID)

	// 删除 Cloudflare 上的隧道
	cfg, _ := h.configMgr.Load()
	if cfg.APIToken != "" {
		cf := NewCloudflareAPI(cfg.APIToken)
		accountID, err := cf.GetAccountID(tunnel.ZoneID)
		if err == nil {
			cf.DeleteTunnel(accountID, tunnel.TunnelID)
		}

		// 删除 DNS 记录
		records, err := cf.GetDNSRecords(tunnel.ZoneID)
		if err == nil {
			for _, rec := range records {
				if rec.Name == tunnel.PublicDomain {
					cf.DeleteDNSRecord(tunnel.ZoneID, rec.ID)
					break
				}
			}
		}
	}

	// 删除配置
	h.configMgr.DeleteTunnel(id)

	h.handleSuccess(w, map[string]interface{}{
		"message": "隧道已删除",
	})
}

func (h *Handler) TunnelLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 5 {
			id = parts[4]
		}
	}

	tunnel, err := h.configMgr.GetTunnel(id)
	if err != nil {
		h.handleError(w, 404, "隧道不存在")
		return
	}

	logs, err := h.tunnelProc.GetTunnelLog(tunnel.TunnelID)
	if err != nil {
		logs = "获取日志失败: " + err.Error()
	}

	jsonResponse(w, 200, map[string]interface{}{
		"success": true,
		"logs":    logs,
	})
}

// ==================== 优选IP ====================

func (h *Handler) AddPreferredIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	input := strings.TrimSpace(req.IP)
	if input == "" {
		h.handleError(w, 400, "请输入IP或域名")
		return
	}

	ip := PreferredIP{Value: input}

	// 判断是IP还是域名
	if isIPv4(input) {
		ip.Type = "ip"
		ip.Resolved = input
	} else {
		ip.Type = "domain"
		ips, err := ResolveDomain(input)
		if err != nil {
			ip.Resolved = ""
		} else {
			ip.Resolved = ips[0]
		}
	}

	if err := h.configMgr.AddPreferredIP(ip); err != nil {
		h.handleError(w, 400, err.Error())
		return
	}

	// 自动应用优选IP到所有隧道（更新DNS记录为A记录+小黄云）
	updated := 0
	if ip.Resolved != "" {
		cfg, _ := h.configMgr.Load()
		if cfg.APIToken != "" && len(cfg.Tunnels) > 0 {
			cf := NewCloudflareAPI(cfg.APIToken)
			for _, tunnel := range cfg.Tunnels {
				records, err := cf.GetDNSRecords(tunnel.ZoneID)
				if err != nil {
					continue
				}
				for _, rec := range records {
					if rec.Name == tunnel.PublicDomain {
						// 更新为优选IP的A记录（proxied=false，避免 Error 1000）
						_, err := cf.UpdateDNSRecord(tunnel.ZoneID, rec.ID, "A", tunnel.Subdomain, ip.Resolved, false)
						if err == nil {
							updated++
						}
						break
					}
				}
			}
		}
	}

	jsonResponse(w, 200, map[string]interface{}{
		"success":  true,
		"resolved": ip.Resolved,
		"updated":  updated,
		"message":  fmt.Sprintf("已添加并自动应用到 %d 个隧道", updated),
	})
}

func (h *Handler) RemovePreferredIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	if err := h.configMgr.RemovePreferredIP(req.Index); err != nil {
		h.handleError(w, 400, err.Error())
		return
	}

	h.handleSuccess(w, nil)
}

func (h *Handler) ApplyPreferredIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"` // 要应用的优选IP
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	cfg, _ := h.configMgr.Load()
	if cfg.APIToken == "" || cfg.ZoneID == "" {
		h.handleError(w, 400, "请先配置API Token和域名")
		return
	}

	if req.IP == "" {
		h.handleError(w, 400, "请提供优选IP")
		return
	}

	cf := NewCloudflareAPI(cfg.APIToken)
	updated := 0

	for _, tunnel := range cfg.Tunnels {
		records, err := cf.GetDNSRecords(tunnel.ZoneID)
		if err != nil {
			continue
		}

		for _, rec := range records {
			if rec.Name == tunnel.PublicDomain {
				// 更新为优选IP的A记录（proxied=false，避免 Error 1000）
				_, err := cf.UpdateDNSRecord(tunnel.ZoneID, rec.ID, "A", tunnel.Subdomain, req.IP, false)
				if err == nil {
					updated++
				}
				break
			}
		}
	}

	jsonResponse(w, 200, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("已更新 %d 个隧道的DNS记录为优选IP: %s", updated, req.IP),
		"updated": updated,
	})
}

// ==================== DNS管理 ====================

func (h *Handler) GetDNSRecords(w http.ResponseWriter, r *http.Request) {
	cfg, _ := h.configMgr.Load()
	if cfg.APIToken == "" || cfg.ZoneID == "" {
		h.handleError(w, 400, "请先配置API Token和域名")
		return
	}

	cf := NewCloudflareAPI(cfg.APIToken)
	records, err := cf.GetDNSRecords(cfg.ZoneID)
	if err != nil {
		h.handleError(w, 500, err.Error())
		return
	}

	jsonResponse(w, 200, map[string]interface{}{
		"success": true,
		"records": records,
	})
}

func (h *Handler) UpdateSSL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.handleError(w, 400, "无效请求")
		return
	}

	cfg, _ := h.configMgr.Load()
	if cfg.APIToken == "" || cfg.ZoneID == "" {
		h.handleError(w, 400, "请先配置API Token和域名")
		return
	}

	cf := NewCloudflareAPI(cfg.APIToken)
	if err := cf.UpdateSSLSetting(cfg.ZoneID, req.Mode); err != nil {
		h.handleError(w, 500, err.Error())
		return
	}

	h.handleSuccess(w, nil)
}

// ==================== 系统信息 ====================

func (h *Handler) SystemInfo(w http.ResponseWriter, r *http.Request) {
	cfg, _ := h.configMgr.Load()
	running := h.tunnelProc.GetRunningCount()

	jsonResponse(w, 200, map[string]interface{}{
		"success":      true,
		"hasApiToken":  cfg.APIToken != "",
		"zoneName":     cfg.ZoneName,
		"zoneId":       cfg.ZoneID,
		"tunnelCount":  len(cfg.Tunnels),
		"runningCount": running,
		"ipCount":      len(cfg.PreferredIPs),
		"preferredIPs": cfg.PreferredIPs,
	})
}

// ==================== 工具函数 ====================

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// generateToken 生成随机token
func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// ==================== 认证管理 ====================

type AuthManager struct {
	dataDir string
}

func NewAuthManager(dataDir string) *AuthManager {
	return &AuthManager{dataDir: dataDir}
}

func (am *AuthManager) passwordFilePath() string {
	return filepath.Join(am.dataDir, ".auth")
}

func (am *AuthManager) tokenFilePath() string {
	return filepath.Join(am.dataDir, ".token")
}

func (am *AuthManager) HasPassword() bool {
	_, err := os.Stat(am.passwordFilePath())
	return err == nil
}

func (am *AuthManager) SetPassword(password string) error {
	// 简单存储密码hash（生产环境应使用bcrypt）
	hash := fmt.Sprintf("%x", simpleHash(password))
	return os.WriteFile(am.passwordFilePath(), []byte(hash), 0600)
}

func (am *AuthManager) CheckPassword(password string) bool {
	data, err := os.ReadFile(am.passwordFilePath())
	if err != nil {
		return false
	}
	hash := fmt.Sprintf("%x", simpleHash(password))
	return string(data) == hash
}

func (am *AuthManager) GenerateToken() string {
	token := generateToken()
	os.WriteFile(am.tokenFilePath(), []byte(token), 0600)
	return token
}

func (am *AuthManager) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 如果没有设置密码，直接通过
		if !am.HasPassword() {
			next(w, r)
			return
		}

		cookie, err := r.Cookie("cf_tunnel_auth")
		if err != nil {
			jsonResponse(w, 401, map[string]interface{}{
				"success": false,
				"message": "未登录",
			})
			return
		}

		tokenData, err := os.ReadFile(am.tokenFilePath())
		if err != nil || string(tokenData) != cookie.Value {
			jsonResponse(w, 401, map[string]interface{}{
				"success": false,
				"message": "登录已过期",
			})
			return
		}

		next(w, r)
	}
}

func simpleHash(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}
