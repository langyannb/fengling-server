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
