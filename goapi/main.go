// goapi 是风铃分享库的 Go 网关（阶段 0）。
//
// 职责：接管 health / version / stream(SSE) / WebSocket(/ws)，
// 其余全部 action 原样透传给 PHP（FastCGI），保证客户端零改动。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/httpapi"
	"github.com/langyannb/fengling-server/goapi/internal/realtime"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 构建信息由 CI 用 -ldflags -X 注入：
//
//	-X main.buildVersion=$GITHUB_SHA -X main.buildTime=$(date -u +%FT%TZ)
var (
	buildVersion = "dev"
	buildTime    = "unknown"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	st, err := store.Open(cfg, logger)
	if err != nil {
		logger.Error("MySQL 连接池初始化失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	rd := store.NewRedis(cfg, logger)
	defer rd.Close()

	engine := realtime.New(cfg, st, rd, logger)
	defer engine.Close()

	router := httpapi.New(httpapi.Env{
		Cfg:       cfg,
		Store:     st,
		Redis:     rd,
		Engine:    engine,
		Log:       logger,
		Version:   buildVersion,
		BuildTime: buildTime,
		Started:   time.Now(),
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		// 刻意不设 ReadTimeout / WriteTimeout：SSE 与 WebSocket 是长连接，
		// 一旦设了就会在到点时把连接掐掉（这是最容易被忽视的坑）。
	}

	go func() {
		logger.Info("goapi 启动",
			"addr", cfg.Addr,
			"version", buildVersion,
			"build_time", buildTime,
			"fpm", cfg.FPMNetwork+"://"+cfg.FPMAddr,
			"php_script", cfg.PHPScript,
			"mysql_socket", cfg.DBSocket,
			"redis", cfg.RedisAddr,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("收到退出信号, 开始优雅关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("优雅关闭超时", "err", err)
	}
	engine.Close()
	logger.Info("goapi 已退出")
}
