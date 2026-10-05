// Package config 汇总 goapi 的全部运行期配置（环境变量 + 安全默认值）。
//
// 生产上由 systemd 的 EnvironmentFile=/etc/fengling/goapi.env 注入
// （deploy/install.sh 生成，脚本里的变量名与本文件严格一致）。
package config

import (
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config 是 goapi 的全部可调项。
type Config struct {
	// 监听地址：只监听回环，外部一律走 nginx（deploy/goapi.conf）。
	Addr string

	// PHP FastCGI（透传目标）。
	FPMNetwork    string // unix
	FPMAddr       string // /tmp/php-cgi-85.sock
	PHPScript     string // /www/wwwroot/flfxk/api.php
	PHPDocRoot    string // /www/wwwroot/flfxk
	PHPScriptName string // /api.php（nginx 对外暴露的路径）

	// MySQL（与 config.php 的 DB_* 对齐）。
	//
	// 生产上 config.php 里 DB_HOST='localhost'，在 PDO/mysqli 语义下这是**走 unix socket**
	// （/tmp/mysql.sock），而且授权是 'user'@'localhost' —— 走 TCP 到 127.0.0.1 要靠反解
	// 才能匹配，所以默认用 socket，DB_HOST/DB_PORT 只在 DBSocket 为空时兜底。
	DBSocket  string
	DBHost    string
	DBPort    int
	DBName    string
	DBUser    string
	DBPass    string
	DBCharset string

	// Redis（127.0.0.1:6379 无密码；连不上必须降级）。
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// 时序参数（默认值与 PHP sse_run 完全一致）。
	PollInterval time.Duration // 1s
	SSEHeartbeat time.Duration // 10s
	SSEMaxAge    time.Duration // 25s
	WSHeartbeat  time.Duration // 15s
	WSIdle       time.Duration // 60s
	WSWriteWait  time.Duration // 10s
	WSSendBuffer int           // 64
	OnlineTTL    time.Duration // 90s

	LogLevel slog.Level
}

// Load 从环境变量读取配置；任何一项缺失都回落到与生产一致的默认值。
func Load() Config {
	return Config{
		Addr: getStr("GOAPI_ADDR", "127.0.0.1:9100"),

		FPMNetwork:    getStr("FPM_NETWORK", "unix"),
		FPMAddr:       getStr("FPM_ADDR", "/tmp/php-cgi-85.sock"),
		PHPScript:     getStr("PHP_SCRIPT", "/www/wwwroot/flfxk/api.php"),
		PHPDocRoot:    getStr("PHP_DOCROOT", "/www/wwwroot/flfxk"),
		PHPScriptName: getStr("PHP_SCRIPT_NAME", "/api.php"),

		DBSocket:  getStr("DB_SOCKET", "/tmp/mysql.sock"),
		DBHost:    getStr("DB_HOST", "localhost"),
		DBPort:    getInt("DB_PORT", 3306),
		DBName:    getStr("DB_NAME", "flfxk"),
		DBUser:    getStr("DB_USER", "flfxk"),
		DBPass:    getStr("DB_PASS", ""),
		DBCharset: getStr("DB_CHARSET", "utf8mb4"),

		RedisAddr:     getStr("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword: getStr("REDIS_PASSWORD", ""),
		RedisDB:       getInt("REDIS_DB", 0),

		PollInterval: getDur("POLL_INTERVAL", time.Second),
		SSEHeartbeat: getDur("SSE_HEARTBEAT", 10*time.Second),
		SSEMaxAge:    getDur("SSE_MAX_AGE", 25*time.Second),
		WSHeartbeat:  getDur("WS_HEARTBEAT", 15*time.Second),
		WSIdle:       getDur("WS_IDLE", 60*time.Second),
		WSWriteWait:  getDur("WS_WRITE_WAIT", 10*time.Second),
		WSSendBuffer: getInt("WS_SEND_BUFFER", 64),
		OnlineTTL:    getDur("ONLINE_TTL", 90*time.Second),

		LogLevel: parseLevel(getStr("LOG_LEVEL", "info")),
	}
}

// DBAddr 返回 host:port。
func (c Config) DBAddr() string {
	return net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort))
}

func getStr(key, def string) string {
	if v := strings.TrimSpace(env(key)); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(env(key))); err == nil {
		return v
	}
	return def
}

func getDur(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(env(key))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
