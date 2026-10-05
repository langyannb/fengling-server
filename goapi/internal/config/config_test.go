package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestDefaultsMatchProductionContract(t *testing.T) {
	// 清掉可能被外部环境注入的值，保证断言的是纯默认值。
	for _, k := range []string{
		"GOAPI_ADDR", "FPM_NETWORK", "FPM_ADDR", "PHP_SCRIPT", "PHP_DOCROOT",
		"PHP_SCRIPT_NAME", "DB_SOCKET", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASS",
		"DB_CHARSET", "REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB", "LOG_LEVEL",
		"POLL_INTERVAL", "SSE_HEARTBEAT", "SSE_MAX_AGE", "WS_HEARTBEAT", "WS_IDLE",
		"WS_WRITE_WAIT", "WS_SEND_BUFFER", "ONLINE_TTL",
	} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.Addr != "127.0.0.1:9100" {
		t.Errorf("Addr=%s", c.Addr)
	}
	if c.FPMAddr != "/tmp/php-cgi-85.sock" || c.FPMNetwork != "unix" {
		t.Errorf("fpm=%s/%s", c.FPMNetwork, c.FPMAddr)
	}
	if c.PHPScript != "/www/wwwroot/flfxk/api.php" || c.PHPDocRoot != "/www/wwwroot/flfxk" {
		t.Errorf("php=%s/%s", c.PHPScript, c.PHPDocRoot)
	}
	// MySQL 默认走 unix socket（对齐 PDO 的 host=localhost 语义）
	if c.DBSocket != "/tmp/mysql.sock" {
		t.Errorf("DBSocket=%s", c.DBSocket)
	}
	if c.RedisAddr != "127.0.0.1:6379" || c.RedisPassword != "" {
		t.Errorf("redis=%s", c.RedisAddr)
	}
	// 时序必须与 PHP sse_run 一致：1s 轮询 / 10s 心跳 / 25s 封顶
	if c.PollInterval != time.Second || c.SSEHeartbeat != 10*time.Second || c.SSEMaxAge != 25*time.Second {
		t.Errorf("sse timing=%v/%v/%v", c.PollInterval, c.SSEHeartbeat, c.SSEMaxAge)
	}
	// WS: 15s 心跳 / 60s 空闲 / 10s 写超时 / 64 缓冲
	if c.WSHeartbeat != 15*time.Second || c.WSIdle != 60*time.Second ||
		c.WSWriteWait != 10*time.Second || c.WSSendBuffer != 64 {
		t.Errorf("ws timing=%v/%v/%v/%d", c.WSHeartbeat, c.WSIdle, c.WSWriteWait, c.WSSendBuffer)
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("level=%v", c.LogLevel)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("GOAPI_ADDR", "127.0.0.1:19100")
	t.Setenv("DB_PORT", "3307")
	t.Setenv("SSE_MAX_AGE", "30s")
	t.Setenv("SSE_HEARTBEAT", "12") // 纯数字按秒解析
	t.Setenv("LOG_LEVEL", "debug")
	c := Load()
	if c.Addr != "127.0.0.1:19100" {
		t.Errorf("Addr=%s", c.Addr)
	}
	if c.DBPort != 3307 {
		t.Errorf("DBPort=%d", c.DBPort)
	}
	if c.SSEMaxAge != 30*time.Second {
		t.Errorf("SSEMaxAge=%v", c.SSEMaxAge)
	}
	if c.SSEHeartbeat != 12*time.Second {
		t.Errorf("SSEHeartbeat=%v", c.SSEHeartbeat)
	}
	if c.LogLevel != slog.LevelDebug {
		t.Errorf("level=%v", c.LogLevel)
	}
}

func TestDBAddr(t *testing.T) {
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_PORT", "3306")
	if got := Load().DBAddr(); got != "127.0.0.1:3306" {
		t.Errorf("DBAddr=%s", got)
	}
}

// 阶段 1：S3 / 限流 / 清理 ticker 的默认值必须与契约一致。
func TestStage1Defaults(t *testing.T) {
	for _, k := range []string{
		"S3_ENDPOINT", "S3_BUCKET", "S3_REGION", "S3_ACCESS_KEY", "S3_SECRET_KEY",
		"S3_PUBLIC_URL", "UPLOAD_TMP_DIR", "UPLOAD_RATE_LIMIT", "UPLOAD_RATE_WINDOW",
		"VIDEO_CLEANUP_INTERVAL", "VIDEO_CLEANUP_INITIAL",
	} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.S3Endpoint != "" || c.S3SecretKey != "" {
		t.Fatalf("S3 配置不得有默认值: %q", c.S3Endpoint)
	}
	if c.UploadTmpDir != "" {
		t.Fatalf("临时目录默认应为空(=os.TempDir): %q", c.UploadTmpDir)
	}
	// 契约：每人每 10 分钟最多 30 次上传
	if c.UploadRateLimit != 30 || c.UploadRateWindow != 10*time.Minute {
		t.Fatalf("限流默认=%d/%v", c.UploadRateLimit, c.UploadRateWindow)
	}
	// 契约：启动 30 秒后跑一次, 之后每 10 分钟一次
	if c.VideoCleanupInitial != 30*time.Second || c.VideoCleanupInterval != 10*time.Minute {
		t.Fatalf("清理默认=%v/%v", c.VideoCleanupInitial, c.VideoCleanupInterval)
	}
}

// 契约第 5 节：必需环境变量缺失必须能被启动流程发现（缺任何一个都不许带默认值上线）。
func TestMissingEnv(t *testing.T) {
	// DB_HOST 也要清掉：DB_SOCKET 与 DB_HOST 是「二选一」，不清会有环境残留导致不稳定。
	for _, k := range []string{
		"GOAPI_ADDR", "DB_SOCKET", "DB_HOST", "DB_NAME", "DB_USER", "DB_PASS", "REDIS_ADDR",
		"S3_ENDPOINT", "S3_BUCKET", "S3_REGION", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_PUBLIC_URL",
	} {
		t.Setenv(k, "")
	}
	want := []string{
		"GOAPI_ADDR", "DB_NAME", "DB_USER", "DB_PASS", "REDIS_ADDR",
		"S3_ENDPOINT", "S3_BUCKET", "S3_REGION", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_PUBLIC_URL",
		"DB_SOCKET", // socket 与 host 都缺 → 报 DB_SOCKET
	}
	got := Load().MissingEnv()
	if len(got) != len(want) {
		t.Fatalf("缺失键数量=%d, 期望 %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个=%s, 期望 %s (全量 %v)", i, got[i], want[i], got)
		}
	}

	// 只补 DB_HOST（不补 DB_SOCKET）也算够用：走 TCP 的场景不该被拒绝启动。
	t.Setenv("DB_HOST", "127.0.0.1")
	if got := Load().MissingEnv(); len(got) != len(want)-1 {
		t.Fatalf("只缺 DB_SOCKET 时不该报它: %v", got)
	}

	// 全部补齐 → 不再报缺失
	for _, k := range want {
		t.Setenv(k, "x")
	}
	if got := Load().MissingEnv(); len(got) != 0 {
		t.Fatalf("补齐后仍报缺失: %v", got)
	}
}

// 时区必须固定为 Asia/Shanghai（对齐 api.php 的 date_default_timezone_set）。
func TestLocalZoneIsShanghai(t *testing.T) {
	loc := LocalZone()
	// 2026-01-01 00:00:00 UTC → 上海 08:00
	utc := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := utc.In(loc).Format("2006-01-02 15:04:05"); got != "2026-01-01 08:00:00" {
		t.Fatalf("上海时区不对: %s", got)
	}
	// 对象键里的时间是服务器本地时间格式
	if got := time.Date(2026, 10, 5, 20, 30, 15, 0, loc).Format("20060102150405"); got != "20261005203015" {
		t.Fatalf("键时间格式=%s", got)
	}
}
