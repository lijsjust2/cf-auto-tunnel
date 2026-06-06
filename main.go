package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"context"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	resetPassword := flag.String("reset-password", "", "重置管理密码")
	flag.Parse()

	// 处理密码重置
	if *resetPassword != "" {
		dataDir := os.Getenv("DATA_DIR")
		if dataDir == "" {
			dataDir = "/app/data"
		}
		authMgr := NewAuthManager(dataDir)
		if err := authMgr.SetPassword(*resetPassword); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("密码已重置成功!")
		os.Exit(0)
	}

	port := os.Getenv("WEB_PORT")
	if port == "" {
		port = "8080"
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/app/data"
	}

	cloudflaredPath := os.Getenv("CLOUDFLARED_PATH")
	if cloudflaredPath == "" {
		cloudflaredPath = "/app/cloudflared"
	}

	// 确保数据目录存在
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	// 初始化管理器
	configMgr := NewConfigManager(dataDir)
	tunnelProc := NewTunnelProcess(dataDir, cloudflaredPath)
	authMgr := NewAuthManager(dataDir)
	handler := NewHandler(configMgr, tunnelProc, authMgr)

	// 启动健康检查
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tunnelProc.HealthCheck(ctx, configMgr)

	// 自动启动隧道
	tunnelProc.AutoStartTunnels(configMgr)

	// 设置路由
	mux := http.NewServeMux()

	// 请求日志中间件
	loggedMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		mux.ServeHTTP(w, r)
	})

	// 健康检查（无需认证）
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{
			"status":  "ok",
			"version": "v1.0.0",
		})
	})

	// 认证路由（无需认证）
	mux.HandleFunc("GET /api/auth/status", handler.AuthStatus)
	mux.HandleFunc("POST /api/auth/setup", handler.AuthSetup)
	mux.HandleFunc("POST /api/auth/login", handler.AuthLogin)

	// 需要认证的API路由
	mux.Handle("POST /api/auth/change-password", authMgr.Middleware(http.HandlerFunc(handler.AuthChangePassword)))
	mux.Handle("POST /api/cf/test-token", authMgr.Middleware(http.HandlerFunc(handler.TestAPIToken)))
	mux.Handle("GET /api/cf/zones", authMgr.Middleware(http.HandlerFunc(handler.GetZones)))
	mux.Handle("POST /api/cf/select-zone", authMgr.Middleware(http.HandlerFunc(handler.SelectZone)))
	mux.Handle("GET /api/cf/dns", authMgr.Middleware(http.HandlerFunc(handler.GetDNSRecords)))
	mux.Handle("POST /api/cf/ssl", authMgr.Middleware(http.HandlerFunc(handler.UpdateSSL)))

	// 隧道管理
	mux.Handle("POST /api/tunnels", authMgr.Middleware(http.HandlerFunc(handler.CreateTunnel)))
	mux.Handle("GET /api/tunnels", authMgr.Middleware(http.HandlerFunc(handler.ListTunnels)))
	mux.Handle("POST /api/tunnels/{id}/start", authMgr.Middleware(http.HandlerFunc(handler.StartTunnel)))
	mux.Handle("POST /api/tunnels/{id}/stop", authMgr.Middleware(http.HandlerFunc(handler.StopTunnel)))
	mux.Handle("DELETE /api/tunnels/{id}", authMgr.Middleware(http.HandlerFunc(handler.DeleteTunnel)))
	mux.Handle("GET /api/tunnels/{id}/logs", authMgr.Middleware(http.HandlerFunc(handler.TunnelLogs)))

	// 优选IP
	mux.Handle("POST /api/preferred-ips", authMgr.Middleware(http.HandlerFunc(handler.AddPreferredIP)))
	mux.Handle("DELETE /api/preferred-ips", authMgr.Middleware(http.HandlerFunc(handler.RemovePreferredIP)))
	mux.Handle("POST /api/preferred-ips/apply", authMgr.Middleware(http.HandlerFunc(handler.ApplyPreferredIP)))

	// 系统信息
	mux.Handle("GET /api/system/info", authMgr.Middleware(http.HandlerFunc(handler.SystemInfo)))

	// 静态文件（嵌入到二进制中）
	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("加载静态文件失败: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticSub)))

	log.Printf("CF Tunnel Manager 启动在端口 %s", port)
	log.Printf("数据目录: %s", dataDir)
	log.Printf("cloudflared 路径: %s", cloudflaredPath)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), loggedMux); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
