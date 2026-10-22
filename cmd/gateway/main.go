// 程序入口：读取配置、初始化数据库与 HTTP 服务，对外提供 LLM 网关。
// 启动顺序大致为：配置 → 日志 →（可选）建库 → 迁移 → 依赖检查 → 监听端口 → 优雅退出。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/db"
	"github.com/whl/llm-gateway/internal/server"
)

// main 组装整条启动链路；收到 SIGINT/SIGTERM 时会先关闭 HTTP 再退出。
func main() {
	// 1. 读配置文件：环境变量 CONFIG_FILE 可覆盖默认路径。
	configPath := os.Getenv("CONFIG_FILE")
	if configPath == "" {
		configPath = "/etc/llm-gateway/config.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatal(err)
	}
	applog.Init(cfg.LoggingEnabled(), cfg.Logging.Verbose)

	// 2. 数据库：可选自动建库建用户，再连接并执行 SQL 迁移（建表/补字段）。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if cfg.Database.AutoInit {
		err = db.BootstrapDatabase(ctx, cfg.Database.Host, cfg.Database.Port, cfg.Database.AdminUser, cfg.Database.AdminPassword, cfg.Database.Name, cfg.Database.User, cfg.Database.Password)
		if err != nil {
			cancel()
			log.Fatal(err)
		}
	}
	appDSN := db.AppDSN(cfg.Database.Host, cfg.Database.Port, cfg.Database.Name, cfg.Database.User, cfg.Database.Password)
	d, err := db.Open(appDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	if err := db.Migrate(ctx, d.SQL, cfg.Database.Host, cfg.Database.Port, cfg.Database.MigrationFile); err != nil {
		cancel()
		log.Fatal(err)
	}
	cancel()

	// 3. 创建 HTTP 业务核心（路由、鉴权、代理、模型管理等都在 server 包里）。
	s := server.New(cfg, d.SQL)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	if err := s.CheckDependencies(ctx); err != nil {
		cancel()
		// Redis 或上游 LLM 暂时不可用时仍启动，只记日志；可用时会恢复未完成的模型下载。
		applog.Printf("dependency check failed at startup: %v", err)
	} else {
		cancel()
		s.RecoverDownloads()
	}
	s.ReconcileModelDiskSizes()

	// 4. 在单独 goroutine 里监听 HTTP；主 goroutine 等待退出信号。
	httpServer := &http.Server{Addr: cfg.Server.Host + ":" + itoa(cfg.Server.Port), Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 5 * time.Minute, MaxHeaderBytes: 32 << 10}
	go func() {
		applog.Printf("llm-gateway listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	if err := s.ShutdownAudit(ctx); err != nil {
		applog.Printf("audit shutdown: %v", err)
	}
}

// itoa 把整数（例如端口号 8088）转成字符串，用来拼 HTTP 监听地址。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	b := make([]byte, 0, 8)
	for v > 0 {
		b = append(b, byte('0'+v%10))
		v /= 10
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
