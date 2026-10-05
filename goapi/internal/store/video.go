package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// VideoConfig 对齐 api.php:3904-3939 video_config() 的四个可调项
// （enabled / auto_clean 已归一化为 0|1）。
type VideoConfig struct {
	Enabled      int
	MaxMB        int
	TotalLimitMB int
	KeepDays     int
	AutoClean    int
}

// VideoConfigDefault 对齐 api.php:3904 video_config_default()。
func VideoConfigDefault() VideoConfig {
	return VideoConfig{Enabled: 1, MaxMB: 100, TotalLimitMB: 600, KeepDays: 0, AutoClean: 1}
}

// VideoFlag 对齐 api.php:4400 lottery_flag（video_flag 直接复用它）。
func VideoFlag(v any) int {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0
		}
		return boolFlag(int64(f) == 1)
	case float64:
		return boolFlag(int64(x) == 1)
	case float32:
		return boolFlag(int64(x) == 1)
	case int:
		return boolFlag(x == 1)
	case int64:
		return boolFlag(x == 1)
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "on", "yes":
			return 1
		}
		return 0
	}
	return 0
}

func boolFlag(ok bool) int {
	if ok {
		return 1
	}
	return 0
}

// ParseVideoConfig 复刻 video_config() 的「默认值 → array_merge → clamp」三步。
//
// 与 PHP 的一致性细节：settings 里不是对象（或非法 JSON）时保持默认值；
// 某个键存在但值为 null 时也回落默认值（PHP 的 `?? ` 对 null 生效）。
func ParseVideoConfig(raw string) VideoConfig {
	cfg := VideoConfigDefault()
	merged := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
		dec.UseNumber()
		if err := dec.Decode(&merged); err != nil {
			merged = map[string]any{}
		}
	}
	pick := func(key string, def any) any {
		if v, ok := merged[key]; ok && v != nil {
			return v
		}
		return def
	}
	cfg.Enabled = VideoFlag(pick("enabled", 1))
	cfg.AutoClean = VideoFlag(pick("auto_clean", 1))
	cfg.MaxMB = clampInt(pick("max_mb", 100), 1, 500)
	cfg.TotalLimitMB = clampInt(pick("total_limit_mb", 600), 100, 100000)
	cfg.KeepDays = clampInt(pick("keep_days", 0), 0, 3650)
	return cfg
}

// clampInt 对齐 video_config() 里的 `(int)$v` + min/max 夹取。
func clampInt(v any, min, max int) int {
	n := int(toInt64(v))
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

// toInt64 复刻 PHP 的 (int) 强转（字符串走前缀解析，浮点截断）。
func toInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return int64(f)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		i := 0
		for i < len(x) && (x[i] == ' ' || x[i] == '\t') {
			i++
		}
		neg := false
		if i < len(x) && (x[i] == '+' || x[i] == '-') {
			neg = x[i] == '-'
			i++
		}
		j := i
		var n int64
		for j < len(x) && x[j] >= '0' && x[j] <= '9' {
			n = n*10 + int64(x[j]-'0')
			j++
		}
		if j == i {
			return 0
		}
		if neg {
			return -n
		}
		return n
	}
	return 0
}

// VideoConfig 读 settings.video_config（异常/DB 不可用时回落默认值并只记日志，
// 绝不因为配置读不到让上传或清理任务失败）。
func (s *Store) VideoConfig(ctx context.Context) VideoConfig {
	raw, ok, err := s.SettingValue(ctx, "video_config")
	if err != nil {
		s.log.Warn("读取 video_config 失败, 回落默认值", "err", err)
		return VideoConfigDefault()
	}
	if !ok {
		return VideoConfigDefault()
	}
	return ParseVideoConfig(raw)
}

// VideoRow 是清理时需要的一行（video 非空的消息）。
type VideoRow struct {
	Table     string
	ID        int64
	Video     string
	Size      int64
	CreatedAt string
}

// VideoTables 是参与视频清理的两张表（硬编码，绝不从外部拼表名）。
func VideoTables() []string { return []string{"social_messages", "social_pm_messages"} }

func isVideoTable(t string) bool {
	for _, x := range VideoTables() {
		if x == t {
			return true
		}
	}
	return false
}

// QueryVideoRows 对齐 api.php:4065 的两条查询：
//
//	SELECT id, video, video_size, created_at FROM t
//	 WHERE video IS NOT NULL AND video <> '' ORDER BY created_at ASC, id ASC
//
// 两张表合并成一个切片，顺序与 PHP 一致（先 social_messages 再 social_pm_messages，
// 表内按 created_at ASC, id ASC）。单表出错只记日志并跳过该表（与 PHP 的 try/catch 同构）。
func (s *Store) QueryVideoRows(ctx context.Context) ([]VideoRow, error) {
	out := make([]VideoRow, 0, 64)
	var firstErr error
	for _, tb := range VideoTables() {
		err := s.queryVideoRowsOne(ctx, tb, &out)
		if err != nil {
			s.log.Warn("读取待清理视频行失败, 跳过该表", "table", tb, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return out, firstErr
}

func (s *Store) queryVideoRowsOne(ctx context.Context, table string, out *[]VideoRow) error {
	if !isVideoTable(table) {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, video, video_size, created_at FROM `"+table+"` "+
			"WHERE video IS NOT NULL AND video <> '' ORDER BY created_at ASC, id ASC")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			vid  sql.NullString
			size sql.NullInt64
			ts   sql.NullString
		)
		if err := rows.Scan(&id, &vid, &size, &ts); err != nil {
			return err
		}
		*out = append(*out, VideoRow{
			Table:     table,
			ID:        id,
			Video:     vid.String,
			Size:      size.Int64,
			CreatedAt: ts.String,
		})
	}
	return rows.Err()
}

// MarkVideoCleaned 对齐 api.php:4123 video_cleanup_one 的 UPDATE：
// video 置空 + content 追加标记，video_size 保留做审计。
func (s *Store) MarkVideoCleaned(ctx context.Context, table string, id int64) error {
	if !isVideoTable(table) {
		return fmt.Errorf("非法表名: %s", table)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE `"+table+"` SET video = '', content = CONCAT(content, ?) WHERE id = ?",
		CleanupMarker, id)
	return err
}

// CleanupMarker 与 PHP 追加进 content 的标记逐字一致。
const CleanupMarker = "[视频已清理]"
