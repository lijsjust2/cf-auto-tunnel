package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net"
)

const cfAPIBase = "https://api.cloudflare.com/client/v4"

// cfResponse Cloudflare API 通用响应
type cfResponse struct {
	Success  bool            `json:"success"`
	Errors   []cfError       `json:"errors"`
	Messages []string        `json:"messages"`
	Result   json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Zone 域名信息
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// DNSRecord DNS记录
type DNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// TunnelInfo Cloudflare Tunnel 信息
type TunnelInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Token     string `json:"token,omitempty"`
}

// CloudflareAPI Cloudflare API 客户端
type CloudflareAPI struct {
	token string
}

func NewCloudflareAPI(token string) *CloudflareAPI {
	return &CloudflareAPI{token: token}
}

func (cf *CloudflareAPI) doRequest(method, path string, body interface{}) (json.RawMessage, error) {
	url := cfAPIBase + path
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cf.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return nil, err
	}

	if !cfResp.Success {
		var errMsg string
		for _, e := range cfResp.Errors {
			errMsg += e.Message + "; "
		}
		if errMsg == "" {
			errMsg = "API请求失败"
		}
		return nil, fmt.Errorf(errMsg)
	}

	return cfResp.Result, nil
}

// GetZones 获取域名列表
func (cf *CloudflareAPI) GetZones() ([]Zone, error) {
	result, err := cf.doRequest("GET", "/zones?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	var zones []Zone
	if err := json.Unmarshal(result, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

// GetDNSRecords 获取DNS记录
func (cf *CloudflareAPI) GetDNSRecords(zoneID string) ([]DNSRecord, error) {
	result, err := cf.doRequest("GET", fmt.Sprintf("/zones/%s/dns_records?per_page=100", zoneID), nil)
	if err != nil {
		return nil, err
	}
	var records []DNSRecord
	if err := json.Unmarshal(result, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// CreateDNSRecord 创建DNS记录
func (cf *CloudflareAPI) CreateDNSRecord(zoneID, recordType, name, content string, proxied bool) (*DNSRecord, error) {
	body := map[string]interface{}{
		"type":    recordType,
		"name":    name,
		"content": content,
		"proxied": proxied,
		"ttl":     1,
	}
	result, err := cf.doRequest("POST", fmt.Sprintf("/zones/%s/dns_records", zoneID), body)
	if err != nil {
		return nil, err
	}
	var record DNSRecord
	if err := json.Unmarshal(result, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// UpdateDNSRecord 更新DNS记录
func (cf *CloudflareAPI) UpdateDNSRecord(zoneID, recordID, recordType, name, content string, proxied bool) (*DNSRecord, error) {
	body := map[string]interface{}{
		"type":    recordType,
		"name":    name,
		"content": content,
		"proxied": proxied,
		"ttl":     1,
	}
	result, err := cf.doRequest("PUT", fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID), body)
	if err != nil {
		return nil, err
	}
	var record DNSRecord
	if err := json.Unmarshal(result, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// DeleteDNSRecord 删除DNS记录
func (cf *CloudflareAPI) DeleteDNSRecord(zoneID, recordID string) error {
	_, err := cf.doRequest("DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID), nil)
	return err
}

// CreateTunnel 创建 Cloudflare Tunnel
// config_src=cloudflare 表示配置由 Cloudflare 管理（远程配置）
func (cf *CloudflareAPI) CreateTunnel(accountID, name string) (*TunnelInfo, error) {
	body := map[string]interface{}{
		"name":       name,
		"tunnel_type": "cfd_tunnel",
		"config_src":  "cloudflare",
	}
	result, err := cf.doRequest("POST", fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID), body)
	if err != nil {
		return nil, err
	}
	var tunnel TunnelInfo
	if err := json.Unmarshal(result, &tunnel); err != nil {
		return nil, err
	}
	return &tunnel, nil
}

// ConfigureTunnel 配置隧道的 ingress 规则（远程配置）
// 这是关键步骤：告诉 Cloudflare 收到某个 hostname 的请求时转发到哪个本地服务
func (cf *CloudflareAPI) ConfigureTunnel(accountID, tunnelID, hostname, serviceURL string) error {
	body := map[string]interface{}{
		"config": map[string]interface{}{
			"ingress": []map[string]interface{}{
				{
					"hostname":      hostname,
					"service":       serviceURL,
					"originRequest": map[string]interface{}{},
				},
				{
					"service": "http_status:404",
				},
			},
		},
	}
	_, err := cf.doRequest("PUT", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID), body)
	return err
}

// GetTunnelConfig 获取隧道的当前配置
func (cf *CloudflareAPI) GetTunnelConfig(accountID, tunnelID string) (map[string]interface{}, error) {
	result, err := cf.doRequest("GET", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID), nil)
	if err != nil {
		return nil, err
	}
	var config map[string]interface{}
	if err := json.Unmarshal(result, &config); err != nil {
		return nil, err
	}
	return config, nil
}

// DeleteTunnel 删除 Cloudflare Tunnel
func (cf *CloudflareAPI) DeleteTunnel(accountID, tunnelID string) error {
	_, err := cf.doRequest("DELETE", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s", accountID, tunnelID), nil)
	return err
}

// ListTunnels 列出 Tunnels
func (cf *CloudflareAPI) ListTunnels(accountID string) ([]TunnelInfo, error) {
	result, err := cf.doRequest("GET", fmt.Sprintf("/accounts/%s/cfd_tunnel?per_page=100", accountID), nil)
	if err != nil {
		return nil, err
	}
	var tunnels []TunnelInfo
	if err := json.Unmarshal(result, &tunnels); err != nil {
		return nil, err
	}
	return tunnels, nil
}

// GetTunnelToken 获取 Tunnel Token
func (cf *CloudflareAPI) GetTunnelToken(accountID, tunnelID string) (string, error) {
	result, err := cf.doRequest("GET", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", accountID, tunnelID), nil)
	if err != nil {
		return "", err
	}
	var token string
	if err := json.Unmarshal(result, &token); err != nil {
		return "", err
	}
	return token, nil
}



// GetAccountID 通过 Zone ID 获取 Account ID
func (cf *CloudflareAPI) GetAccountID(zoneID string) (string, error) {
	result, err := cf.doRequest("GET", fmt.Sprintf("/zones/%s", zoneID), nil)
	if err != nil {
		return "", err
	}
	var zoneDetail struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	if err := json.Unmarshal(result, &zoneDetail); err != nil {
		return "", err
	}
	return zoneDetail.Account.ID, nil
}

// UpdateSSLSetting 更新SSL设置
func (cf *CloudflareAPI) UpdateSSLSetting(zoneID, value string) error {
	body := map[string]interface{}{
		"value": value,
	}
	_, err := cf.doRequest("PATCH", fmt.Sprintf("/zones/%s/settings/ssl", zoneID), body)
	return err
}

// ResolveDomain 解析域名为IP
func ResolveDomain(domain string) ([]string, error) {
	ips, err := net.LookupHost(domain)
	if err != nil {
		return nil, err
	}
	// 只返回IPv4
	var ipv4s []string
	for _, ip := range ips {
		if net.ParseIP(ip).To4() != nil {
			ipv4s = append(ipv4s, ip)
		}
	}
	if len(ipv4s) == 0 {
		return nil, fmt.Errorf("未找到IPv4地址")
	}
	return ipv4s, nil
}
