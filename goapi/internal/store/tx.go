package store

import (
	"context"
	"database/sql"
)

// Tx 是一次显式事务。抽奖（lottery_draw）必须在事务里跑 `SELECT ... FOR UPDATE`：
// database/sql 的单条查询是隐式 autocommit，会退化成普通读，并发下会把同一张卡密
// 发给两个人（契约 §4.5 / §9.4）。
//
// 方法集与 Store 的同名方法逐字一致（值语义、扫描规则都复用 row.go 里的 queryRow
// 等包级函数），保证「事务内」与「事务外」取到的 Row 形态完全相同。
type Tx struct {
	tx *sql.Tx
}

// BeginLotteryTx 开一个抽奖事务（PHP: db()->beginTransaction()）。
// 语句与 PHP 逐字对齐：BEGIN → SELECT ... FOR UPDATE → UPDATE → INSERT → COMMIT。
func (s *Store) BeginLotteryTx(ctx context.Context) (*Tx, error) {
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: t}, nil
}

// QueryRow 同 Store.QueryRow，但跑在事务连接上。
func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) (Row, error) {
	return queryRow(ctx, t.tx, query, args...)
}

// QueryAll 同 Store.QueryAll，但跑在事务连接上。
func (t *Tx) QueryAll(ctx context.Context, query string, args ...any) ([]Row, error) {
	return queryAll(ctx, t.tx, query, args...)
}

// QueryValue 同 Store.QueryValue，但跑在事务连接上。
func (t *Tx) QueryValue(ctx context.Context, query string, args ...any) (any, bool, error) {
	return queryValue(ctx, t.tx, query, args...)
}

// Exec 同 Store.Exec，但跑在事务连接上。
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (ExecResult, error) {
	return execOne(ctx, t.tx, query, args...)
}

// Commit 提交（PHP: $pdo->commit()）。
func (t *Tx) Commit() error { return t.tx.Commit() }

// Rollback 回滚（PHP: $pdo->rollBack()）。已提交后再调用返回 sql.ErrTxDone，调用方忽略即可。
func (t *Tx) Rollback() error { return t.tx.Rollback() }
