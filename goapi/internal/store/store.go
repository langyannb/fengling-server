// Package store 封装数据访问：MySQL（透传之外的原生 action 用）与 Redis（在线状态/限流/Pub-Sub）。
//
// 原则：MySQL 查询逐字对齐 api.php / config.php 里的 SQL 语义；Redis 一律「可降级」，
// 连不上只记日志不 panic、不阻塞请求（生产 Redis 与 PHP 共用 127.0.0.1:6379，无密码）。
package store

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/langyannb/fengling-server/goapi/internal/config"
)

// Store 是 MySQL 连接池的持有者。
type Store struct {
	db  *sql.DB
	log *slog.Logger

	colMu    sync.RWMutex
	colCache map[string]bool
}

// Open 建连接池。sql.Open 本身不建连，所以 MySQL 挂掉也不影响进程启动
// （与 PHP「每次请求才连库」的行为一致，启动即失败反而会让 systemd 反复重启）。
func Open(cfg config.Config, log *slog.Logger) (*Store, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.DBUser
	mc.Passwd = cfg.DBPass
	// MySQL 走 unix socket（对齐 PHP：DB_HOST='localhost' 在 PDO 下就是 socket 连接，
	// 且库用户是按 'user'@'localhost' 授权的）。只有显式清空 DB_SOCKET 时才回落 TCP。
	if cfg.DBSocket != "" {
		mc.Net = "unix"
		mc.Addr = cfg.DBSocket
	} else {
		mc.Net = "tcp"
		mc.Addr = cfg.DBAddr()
	}
	mc.DBName = cfg.DBName
	// Collation 留空 => 驱动发 `SET NAMES utf8mb4`，与 PDO 的 charset=utf8mb4 一致。
	mc.Collation = ""
	mc.Params = map[string]string{"charset": cfg.DBCharset}
	// 关键取舍：**不开 ParseTime**。
	// 开了之后 DATETIME 会变成 time.Time，透传给客户端就变成 RFC3339，而 PHP 给的是
	// "2026-10-05 15:29:24"；而且 driver 会按 loc 解释时区，进程 TZ 与 MySQL TZ 不一致时
	// 墙钟时间会被平移。不开的话驱动原样返回 MySQL 的字符串，天然与 PHP 逐字一致。
	// 阶段 2 若确实要 time.Time，用下面的 FormatDateTime() 转回 PHP 格式再输出。
	mc.ParseTime = false
	mc.Timeout = 5 * time.Second

	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	// php-fpm 那边 pm.max_children=100；Go 侧只服务原生 action，24 条连接足够。
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	return &Store{db: db, log: log, colCache: make(map[string]bool)}, nil
}

// DB 暴露底层连接池（realtime 轮询用）。
func (s *Store) DB() *sql.DB { return s.db }

// Close 关闭连接池。
func (s *Store) Close() error { return s.db.Close() }

// Ping 探活（health action 用）。
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}

// DateTimeLayout 是 PHP 的 date('Y-m-d H:i:s') 格式，也是 MySQL DATETIME 的文本形态。
const DateTimeLayout = "2006-01-02 15:04:05"

// FormatDateTime 把 time.Time 转成 PHP 的字符串格式。
// 阶段 2 之后凡是要透传给客户端的 DATETIME 字段，都必须经过它（绝不能直接塞 time.Time）。
func FormatDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(DateTimeLayout)
}

// User 是原生 action 需要的用户字段（对齐 current_user() 取到的那一行里用得到的列）。
type User struct {
	ID       int64
	Username string
	Nickname string
	Role     string
	// IsActive 恒为 1（查询条件里已经带了 is_active=1，与 PHP current_user() 同构）。
	// 上传 action 里仍按契约保留「你已被封禁」判定，与 PHP 的死分支语义一致。
	IsActive int
}

// CurrentUser 对齐 api.php:3348 current_user()：
// 先查 sessions(user_id,token) JOIN users 且 is_active=1，查不到再回落 users.token。
// token 为空或查不到时返回 (nil, nil)。
func (s *Store) CurrentUser(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	u, err := s.userBy(ctx,
		`SELECT u.id, u.username, u.nickname, u.role, u.is_active
		   FROM sessions s JOIN users u ON s.user_id = u.id
		  WHERE s.token = ? AND u.is_active = 1`, token)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	return s.userBy(ctx,
		`SELECT id, username, nickname, role, is_active FROM users WHERE token = ? AND is_active = 1`, token)
}

func (s *Store) userBy(ctx context.Context, query string, args ...any) (*User, error) {
	var (
		id                       int64
		username, nickname, role sql.NullString
		isActive                 sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&id, &username, &nickname, &role, &isActive)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &User{
		ID:       id,
		Username: username.String,
		Nickname: nickname.String,
		Role:     role.String,
		IsActive: int(isActive.Int64),
	}, nil
}

// allowedTables 是允许出现在 SQL 表名位置的白名单（表名只来自本仓库代码，这里是纵深防御）。
var allowedTables = map[string]bool{
	"social_pm_messages": true,
	"social_messages":    true,
	"settings":           true,
	"social_mutes":       true,
	"users":              true,
	"sessions":           true,
}

// HasColumn 对齐 api.php:3866 db_has_column()：`SHOW COLUMNS FROM t LIKE 'c'`。
// 出错按 false（迁移没跑时自动退回旧行为），并且只记一次日志。
// 注意：PHP 的 static 缓存是「每请求」，这里是「每进程」，跑完迁移需要重启 goapi。
func (s *Store) HasColumn(ctx context.Context, table, column string) bool {
	key := table + "." + column
	s.colMu.RLock()
	v, ok := s.colCache[key]
	s.colMu.RUnlock()
	if ok {
		return v
	}
	found := false
	if allowedTables[table] {
		// 用 information_schema 而不是 `SHOW COLUMNS ... LIKE ?`：
		// MySQL 的 SHOW 语句不接受占位符（会报 1064 near '?'），
		// 一旦这里静默返回 false，实时事件里 video 系列字段就会整体消失。
		var n int
		err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.COLUMNS "+
				"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
			table, column).Scan(&n)
		if err != nil {
			s.log.Warn("查询列是否存在失败, 按无此列处理", "table", table, "column", column, "err", err)
		} else {
			found = n > 0
		}
	}
	s.colMu.Lock()
	s.colCache[key] = found
	s.colMu.Unlock()
	return found
}

// MaxID 对齐 sse_run 的游标初始化：`SELECT COALESCE(MAX(id), 0) FROM <表>`。
func (s *Store) MaxID(ctx context.Context, table string) (int64, error) {
	if !allowedTables[table] {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM `"+table+"`").Scan(&id)
	return id, err
}

// MutedGroupIDs 对齐 api.php:4157 my_muted_ids()：SELECT group_id FROM social_mutes WHERE user_id = ?
// 出错返回空集合（PHP 那边也是 catch 后返回 []）。
func (s *Store) MutedGroupIDs(ctx context.Context, uid int64) map[int64]bool {
	out := make(map[int64]bool)
	rows, err := s.db.QueryContext(ctx, "SELECT group_id FROM social_mutes WHERE user_id = ?", uid)
	if err != nil {
		s.log.Warn("读免打扰列表失败, 按空处理", "uid", uid, "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			continue
		}
		out[gid] = true
	}
	if err := rows.Err(); err != nil {
		s.log.Warn("读免打扰列表中断", "uid", uid, "err", err)
	}
	return out
}

// SettingValue 读 settings 表的原始 value（version action 用）。
// 第二个返回值 = 「有没有这一行」，对应 PHP 里 $row 是否为真。
func (s *Store) SettingValue(ctx context.Context, key string) (string, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT `value` FROM settings WHERE `key` = ?", key).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}
