package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// 这个文件用「极简假驱动」把 store 的通用原语跑起来：不连真实 MySQL，
// 但走的是 database/sql 的真实代码路径（Prepare/Query/Scan），
// 所以能验证 current_user 的**两条查询顺序与 SQL 文本**。

type fakeResult struct {
	cols []string
	rows [][]driver.Value
}

type fakeSQL struct {
	queries []string
	args    [][]driver.Value
	results []struct {
		match string
		res   *fakeResult
	}
	execErr  error
	affected int64
}

func (f *fakeSQL) lookup(q string) *fakeResult {
	for _, r := range f.results {
		if strings.Contains(q, r.match) {
			return r.res
		}
	}
	return &fakeResult{cols: []string{"x"}}
}

type fakeDriver struct{ srv *fakeSQL }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{srv: d.srv}, nil }

type fakeConn struct{ srv *fakeSQL }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{srv: c.srv, q: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return nil, errors.New("不支持事务") }

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.srv.queries = append(c.srv.queries, q)
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	c.srv.args = append(c.srv.args, vals)
	res := c.srv.lookup(q)
	return &fakeRows{cols: res.cols, rows: res.rows}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.srv.queries = append(c.srv.queries, q)
	if c.srv.execErr != nil {
		return nil, c.srv.execErr
	}
	return driver.RowsAffected(c.srv.affected), nil
}

type fakeStmt struct {
	srv *fakeSQL
	q   string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(s.srv.affected), s.srv.execErr
}
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	res := s.srv.lookup(s.q)
	return &fakeRows{cols: res.cols, rows: res.rows}, nil
}

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type fakeConnector struct{ srv *fakeSQL }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{srv: c.srv}, nil
}
func (c fakeConnector) Driver() driver.Driver { return fakeDriver{srv: c.srv} }

func testStore(t *testing.T, srv *fakeSQL) *Store {
	t.Helper()
	db := sql.OpenDB(fakeConnector{srv: srv})
	t.Cleanup(func() { _ = db.Close() })
	return &Store{
		db:       db,
		log:      slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1})),
		colCache: make(map[string]bool),
	}
}

func TestCurrentUserRowFirstQueryHit(t *testing.T) {
	srv := &fakeSQL{results: []struct {
		match string
		res   *fakeResult
	}{
		{"JOIN users u", &fakeResult{cols: []string{"id", "username"}, rows: [][]driver.Value{{int64(7), []byte("alice")}}}},
	}}
	row, err := testStore(t, srv).CurrentUserRow(context.Background(), "tok1")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row["username"] != "alice" { // []byte 必须被转成 string
		t.Fatalf("row=%#v", row)
	}
	if len(srv.queries) != 1 {
		t.Fatalf("命中第一条就不该再查第二条, 实际执行了 %d 条: %v", len(srv.queries), srv.queries)
	}
	if !strings.Contains(srv.queries[0], "FROM sessions s JOIN users u ON s.user_id = u.id") {
		t.Fatalf("第一条查询不对: %q", srv.queries[0])
	}
	if got := srv.args[0][0]; got != "tok1" {
		t.Fatalf("token 参数不对: %v", got)
	}
}

// 关键分支：第一条（sessions JOIN users）查不到时，必须再查 users.token。
func TestCurrentUserRowFallsBackToUsersToken(t *testing.T) {
	srv := &fakeSQL{results: []struct {
		match string
		res   *fakeResult
	}{
		{"FROM users WHERE token = ?", &fakeResult{cols: []string{"id"}, rows: [][]driver.Value{{int64(9)}}}},
	}}
	row, err := testStore(t, srv).CurrentUserRow(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row["id"] != int64(9) {
		t.Fatalf("row=%#v", row)
	}
	if len(srv.queries) != 2 {
		t.Fatalf("应执行两条查询, 实际 %d: %v", len(srv.queries), srv.queries)
	}
	// 逐字对齐 api.php:3362：第二条不能带 u. 别名。
	if want := "SELECT * FROM users WHERE token = ? AND is_active = 1"; !strings.Contains(srv.queries[1], want) {
		t.Fatalf("第二条查询不对: %q", srv.queries[1])
	}
	if strings.Contains(srv.queries[1], "u.is_active") {
		t.Fatalf("第二条查询带了 u. 别名（MySQL 会报 1054）: %q", srv.queries[1])
	}
}

func TestCurrentUserRowEmptyTokenSkipsDB(t *testing.T) {
	srv := &fakeSQL{}
	row, err := testStore(t, srv).CurrentUserRow(context.Background(), "")
	if err != nil || row != nil {
		t.Fatalf("row=%#v err=%v", row, err)
	}
	if len(srv.queries) != 0 {
		t.Fatalf("空 token 不该查库: %v", srv.queries)
	}
}

// is_active != 1 的用户：两条查询都查不到 -> nil（handler 据此回 401，而不是 403）。
func TestCurrentUserRowInactiveUserIsNil(t *testing.T) {
	srv := &fakeSQL{}
	row, err := testStore(t, srv).CurrentUserRow(context.Background(), "banned")
	if err != nil || row != nil {
		t.Fatalf("row=%#v err=%v", row, err)
	}
	if len(srv.queries) != 2 {
		t.Fatalf("应把两条查询都试过, 实际 %d", len(srv.queries))
	}
}

func TestQueryValueReturnsFirstColumn(t *testing.T) {
	srv := &fakeSQL{results: []struct {
		match string
		res   *fakeResult
	}{
		{"COUNT(*)", &fakeResult{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(42)}}}},
	}}
	v, ok, err := testStore(t, srv).QueryValue(context.Background(), "SELECT COUNT(*) FROM email_codes WHERE email = ?", "a@b.co")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if v != int64(42) {
		t.Fatalf("v=%#v", v)
	}
}

func TestQueryValueNoRows(t *testing.T) {
	v, ok, err := testStore(t, &fakeSQL{}).QueryValue(context.Background(), "SELECT created_at FROM email_codes LIMIT 1")
	if err != nil || ok || v != nil {
		t.Fatalf("v=%#v ok=%v err=%v", v, ok, err)
	}
}

func TestQueryRowNoRowsIsNilNil(t *testing.T) {
	row, err := testStore(t, &fakeSQL{}).QueryRow(context.Background(), "SELECT * FROM users WHERE id = ?", 1)
	if err != nil || row != nil {
		t.Fatalf("row=%#v err=%v", row, err)
	}
}

func TestQueryAllScansRows(t *testing.T) {
	srv := &fakeSQL{results: []struct {
		match string
		res   *fakeResult
	}{
		{"FROM social_pms", &fakeResult{cols: []string{"id", "user_a"}, rows: [][]driver.Value{
			{int64(1), []byte("10")},
			{int64(2), nil},
		}}},
	}}
	rows, err := testStore(t, srv).QueryAll(context.Background(), "SELECT id, user_a FROM social_pms")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["user_a"] != "10" || rows[1]["user_a"] != nil {
		t.Fatalf("rows=%#v", rows)
	}
}

func TestExecReturnsResultAndError(t *testing.T) {
	srv := &fakeSQL{affected: 3}
	res, err := testStore(t, srv).Exec(context.Background(), "UPDATE users SET nickname = ? WHERE id = ?", "n", 1)
	if err != nil || res.RowsAffected != 3 {
		t.Fatalf("res=%#v err=%v", res, err)
	}
	srv2 := &fakeSQL{execErr: errors.New("boom")}
	if _, err := testStore(t, srv2).Exec(context.Background(), "UPDATE users SET nickname = ? WHERE id = ?", "n", 1); err == nil {
		t.Fatal("应把驱动错误原样返回")
	}
}
