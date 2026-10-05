package store

import (
	"context"
	"database/sql"
)

// Row 是一行查询结果的「列名 → 值」映射，语义等价于 PDO 的 FETCH_ASSOC。
//
// 为什么需要它（而不是给每张表写结构体）：阶段 2 的账号 action 复刻的是
// `SELECT * FROM users` 这类「列由线上库决定」的查询，而仓库里的 schema.sql 已经陈旧
// （没有 email / email_verified / avatar / bio / tags 这些线上真实存在的列）。
// 用 rows.Columns() 动态扫描，才能保证「线上加了列、PHP 取得到、Go 也取得到」，
// 不会因为模型结构与真实表不符而悄悄输出空字段。
//
// 值类型固定为：string / int64 / float64 / nil（[]byte 统一转成 string）。
// 注意 PDO 默认（模拟预处理）会把整数也返回成字符串，所以调用方**必须**用
// httpapi 里的 phpInt/phpStr 之类按 PHP 语义强转后再输出，不能直接塞进 JSON。
type Row map[string]any

// Get 取值并判断键是否存在。
func (r Row) Get(key string) (any, bool) {
	if r == nil {
		return nil, false
	}
	v, ok := r[key]
	return v, ok
}

// ExecResult 是一次写操作的结果。
type ExecResult struct {
	// LastInsertID 对齐 PDO::lastInsertId()（只对 INSERT 有意义，取不到时为 0）。
	LastInsertID int64
	RowsAffected int64
}

// QueryRow 跑一条查询并取第一行；没有行时返回 (nil, nil)（对齐 PDO fetch() 的 false）。
func (s *Store) QueryRow(ctx context.Context, query string, args ...any) (Row, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	row, err := scanRow(rows)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// QueryAll 跑一条查询并把所有行读进内存（对齐 PDO fetchAll()）。
func (s *Store) QueryAll(ctx context.Context, query string, args ...any) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Exec 执行一条写语句（INSERT / UPDATE / DELETE）。
func (s *Store) Exec(ctx context.Context, query string, args ...any) (ExecResult, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return ExecResult{}, err
	}
	var out ExecResult
	// 两个取不到都不算错误：DELETE 没有 lastInsertId，驱动也可能不支持。
	if id, err := res.LastInsertId(); err == nil {
		out.LastInsertID = id
	}
	if n, err := res.RowsAffected(); err == nil {
		out.RowsAffected = n
	}
	return out, nil
}

// CurrentUserRow 对齐 api.php:3348 current_user()，但取**整行**（SELECT u.*）。
//
// 与 Store.CurrentUser 的区别：那个只取 id/username/nickname/role/is_active，够
// realtime 与上传用；账号 action 还要 password（user_password）、email
// （email_verify_send）、created_at/tags（user_public），所以这里保留整行。
// 两条查询的顺序与 PHP 完全一致，未登录返回 (nil, nil)。
func (s *Store) CurrentUserRow(ctx context.Context, token string) (Row, error) {
	if token == "" {
		return nil, nil
	}
	row, err := s.QueryRow(ctx,
		`SELECT u.* FROM sessions s JOIN users u ON s.user_id = u.id
		  WHERE s.token = ? AND u.is_active = 1`, token)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return row, nil
	}
	return s.QueryRow(ctx, `SELECT * FROM users WHERE token = ? AND is_active = 1`, token)
}

// scanRow 按列名把当前行读成 Row。
func scanRow(rows *sql.Rows) (Row, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	row := make(Row, len(cols))
	for i, c := range cols {
		switch v := vals[i].(type) {
		case nil:
			row[c] = nil
		case []byte:
			// ParseTime=false，所以 DATETIME/DECIMAL 等也走这里；
			// 转成 string 后与 PDO 的取值形态一致。
			row[c] = string(v)
		default:
			row[c] = v
		}
	}
	return row, nil
}

// QueryValue 取「第一行第一列」，对齐 PDO 的 fetchColumn()。
//
// 为什么单独有这个原语：COUNT(*) / MAX(id) 这类查询，PHP 写的是
// `SELECT COUNT(*) FROM ...`（没有别名），用 Row 取会因为列名是 "COUNT(*)"
// 而被迫给 SQL 加别名 —— 那属于「顺手改 SQL」。这里按列序取值，
// 保证 Go 发给 MySQL 的语句与 PHP 逐字相同。
// 第二个返回值 = 有没有取到行（PHP 里 fetchColumn() 返回 false 的情形）。
func (s *Store) QueryValue(ctx context.Context, query string, args ...any) (any, bool, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	var v any
	if err := rows.Scan(&v); err != nil {
		return nil, false, err
	}
	switch x := v.(type) {
	case []byte:
		return string(x), true, nil
	case nil:
		return nil, true, nil
	default:
		return x, true, nil
	}
}
