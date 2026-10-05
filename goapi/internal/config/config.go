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

	// S3（雨云对象存储；只从 /etc/fengling/goapi.env 读，绝不进仓库）。
	// 这 6 项缺失时 main 会拒绝启动（见 MissingEnv）。
	S3Endpoint  string
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string
	S3PublicURL string

	// 上传：临时文件目录（空 = os.TempDir()，即 /tmp；systemd 单元是 PrivateTmp=no，
	// 所以与 php-cgi 共用 /tmp 是安全的）与每人每窗口的上传次数限流。
	UploadTmpDir     string
	UploadRateLimit  int
	UploadRateWindow time.Duration

	// 视频清理常驻 ticker（语义等于 video_cleanup(true,true,true)）。
	VideoCleanupInterval time.Duration
	VideoCleanupInitial  time.Duration

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

		S3Endpoint:  getStr("S3_ENDPOINT", ""),
		S3Bucket:    getStr("S3_BUCKET", ""),
		S3Region:    getStr("S3_REGION", ""),
		S3AccessKey: getStr("S3_ACCESS_KEY", ""),
		S3SecretKey: getStr("S3_SECRET_KEY", ""),
		S3PublicURL: getStr("S3_PUBLIC_URL", ""),

		UploadTmpDir:     getStr("UPLOAD_TMP_DIR", ""),
		UploadRateLimit:  getInt("UPLOAD_RATE_LIMIT", 30),
		UploadRateWindow: getDur("UPLOAD_RATE_WINDOW", 10*time.Minute),

		VideoCleanupInterval: getDur("VIDEO_CLEANUP_INTERVAL", 10*time.Minute),
		VideoCleanupInitial:  getDur("VIDEO_CLEANUP_INITIAL", 30*time.Second),

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

// LocalZone 对齐 api.php:25 的 date_default_timezone_set('Asia/Shanghai')：
// 对象键里的日期、以及视频清理的 keep_days 截止时间都按它算。
// 刻意用固定时区而不是进程本地时区，避免服务器 TZ 与 PHP 不一致时整体错位。
func LocalZone() *time.Location { return localZone }

var localZone = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	// 极端情况下系统没有 tzdata（容器里极少见）：固定 +08:00，结果仍然正确。
	return time.FixedZone("CST", 8*3600)
}()

// MissingEnv 返回**缺失的必需环境变量**（契约第 5 节：缺任何一个键都要启动即
// 报错退出，不要静默用默认值跑到线上）。
//
// 注意是直接看**环境变量本身**（env(key)），不是看 Load 解析后的值：这些键在 Load
// 里都有生产默认值，只看 c.X 分辨不出「没配」和「配了默认值」。
//
// 为什么校验不放进 Load：Load 还要服务单测与本地开发（config_test.go 断言默认值），
// 所以做成独立方法，由 main 在启动最前面调用。
//
// 不列入强制的键及理由：
//   - DB_HOST / DB_PORT：只在 DB_SOCKET 为空时才用（PHP 的 host=localhost 就是 socket）；
//     所以 DB_SOCKET 与 DB_HOST 做成「二选一」而不是硬要求 DB_SOCKET；
//   - DB_CHARSET：默认 utf8mb4 与 config.php 一致，写错只会让中文乱码；
//   - 其它时序参数：都有与 PHP 逐字一致的默认值。
func (c Config) MissingEnv() []string {
	keys := []string{
		"GOAPI_ADDR", "DB_NAME", "DB_USER", "DB_PASS", "REDIS_ADDR",
		"S3_ENDPOINT", "S3_BUCKET", "S3_REGION", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_PUBLIC_URL",
	}
	missing := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		if strings.TrimSpace(env(k)) == "" {
			missing = append(missing, k)
		}
	}
	if strings.TrimSpace(env("DB_SOCKET")) == "" && strings.TrimSpace(env("DB_HOST")) == "" {
		missing = append(missing, "DB_SOCKET")
	}
	return missing
}
