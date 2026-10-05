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
	"github.com/langyannb/fengling-server/goapi/internal/jobs"
	"github.com/langyannb/fengling-server/goapi/internal/realtime"
	"github.com/langyannb/fengling-server/goapi/internal/store"
	"github.com/langyannb/fengling-server/goapi/internal/upload"
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

	// 契约第 5 节：必需环境变量（DB_* / REDIS_ADDR / S3_*）缺任何一个都**拒绝启动**，
	// 不允许带着默认值跑到线上。校验放在 Load 之外，避免影响单测的默认值断言。
	if missing := cfg.MissingEnv(); len(missing) > 0 {
		logger.Error("缺少必需的环境变量, 拒绝启动", "missing", missing)
		os.Exit(1)
	}

	st, err := store.Open(cfg, logger)
	if err != nil {
		logger.Error("MySQL 连接池初始化失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	rd := store.NewRedis(cfg, logger)
	defer rd.Close()

	// S3 客户端：阶段 1 的原生上传与视频清理都要用。
	s3c, err := upload.NewClient(upload.Options{
		Endpoint:  cfg.S3Endpoint,
		Bucket:    cfg.S3Bucket,
		Region:    cfg.S3Region,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		PublicURL: cfg.S3PublicURL,
		Logger:    logger,
	})
	if err != nil {
		// 错误信息里不含凭据（NewClient 只回「配置缺失/非法」，不回显值）。
		logger.Error("S3 客户端初始化失败", "err", err)
		os.Exit(1)
	}

	// 视频清理常驻任务：启动 30 秒后跑一次，之后每 10 分钟一次（可用
	// VIDEO_CLEANUP_INITIAL / VIDEO_CLEANUP_INTERVAL 调），语义 = video_cleanup(true,true,true)。
	cleaner := jobs.NewVideoCleaner(jobs.CleanerDeps{
		Store:  st,
		Object: s3c,
		Redis:  rd,
		Cfg:    cfg,
		Log:    logger,
	})
	jobCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	go cleaner.Run(jobCtx)

	engine := realtime.New(cfg, st, rd, logger)
	defer engine.Close()

	router := httpapi.New(httpapi.Env{
		Cfg:       cfg,
		Store:     st,
		Redis:     rd,
		Engine:    engine,
		Log:       logger,
		S3:        s3c,
		Cleaner:   cleaner,
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
			"s3_endpoint", cfg.S3Endpoint,
			"s3_bucket", cfg.S3Bucket,
			"video_cleanup_interval", cfg.VideoCleanupInterval.String(),
			"video_cleanup_initial", cfg.VideoCleanupInitial.String(),
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

	// 关闭顺序是这个网关的关键：
	// ① srv.Shutdown 会立刻关掉监听套接字（不再收新连接），但它要等所有在途请求结束，
	//    而 SSE 最长 25 秒、WS 更是永不自行结束 —— 所以必须和 ② 并发做，否则必定超时。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Shutdown(shutdownCtx) }()

	// ② 先停后台清理任务（它也在用 MySQL / Redis / S3）：必须在 engine.Close 与
	//    defer rd.Close 之前收干净，避免「往已关闭的 Redis 客户端写 key」。
	stopJobs()
	cleaner.Stop()

	// ③ 主动断开所有实时长连接，并等它们在各自的 defer 里跑完收尾（含 rd.MarkOffline）。
	//    必须在 main 的 defer rd.Close() 之前完成，否则 MarkOffline 会打到已关闭的
	//    Redis 客户端上（日志里的 `redis: client is closed` 就是这么来的）。
	engine.Close()

	// ④ 长连接都已收尾，Shutdown 正常会立刻返回；真超时也是长连接场景的正常现象，
	//    降为 INFO，避免每次重启都报一条 WARN 吓人。
	if err := <-shutdownDone; err != nil {
		logger.Info("优雅关闭超时(实时长连接属正常现象)", "err", err)
	}
	logger.Info("goapi 已退出")
}
