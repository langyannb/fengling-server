package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5A 抽奖单测：全部走脚本化假 DB，不连真实 MySQL / Redis。
//
// 覆盖派单书点名的六项：`-1/0` 三态语义、weight<=0 当 100、配额与冷却、
// 每类拒绝的逐字文案与 HTTP/JSON 码、并发抽奖的事务顺序（BEGIN → SELECT FOR UPDATE →
// UPDATE → INSERT → COMMIT）、破坏性 action 未注册。

const (
	lotteryUserToken  = "tok-lottery-user"
	lotteryAdminToken = "tok-lottery-admin"
)

// ---------- 假数据访问层（带事务） ----------

type lotteryRule struct {
	match string
	val   any
	ok    bool
	err   error
}

type lotteryFake struct {
	mu    sync.Mutex
	me    store.Row
	meErr error

	// settings 行（key → value 原文）
	settings map[string]string

	nowTS      int64  // SELECT UNIX_TIMESTAMP(NOW())
	nowText    string // SELECT NOW()
	lastDrawTS int64  // SELECT UNIX_TIMESTAMP(MAX(created_at))
	totalDrawn int64  // d.created_at > u.lottery_reset_at
	dayDrawn   int64  // d.created_at > u.lottery_day_reset_at（今日与本周共用）
	dailyTotal int64  // 全站当日已发
	unusedCnt  int64  // lottery_codes user_id = 0
	usedCnt    int64  // lottery_codes user_id > 0
	rowTotal   int64  // lottery_records 的 COUNT(*)
	nowDBText  string // SELECT NOW()（quota_reset 用）

	valRules []lotteryRule

	valFn  func(q string, args []any) (any, bool, error)
	allFn  func(q string, args []any) ([]store.Row, error)
	rowFn  func(q string, args []any) (store.Row, error)
	execFn func(q string, args []any) (store.ExecResult, error)

	execErrOn   string
	beginErr    error
	commitErr   error
	nextExecID  int64
	execAffects int64

	calls     []fakeCall
	steps     []string
	commits   int
	rollbacks int
}

func (f *lotteryFake) log(c fakeCall) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func newLotteryFake() *lotteryFake {
	return &lotteryFake{
		me:          socialMe(),
		settings:    map[string]string{},
		nowTS:       shanghai(2026, 6, 1, 12, 0, 0),
		nowText:     "2026-06-01 12:00:00",
		nextExecID:  100,
		execAffects: 1,
	}
}

func (f *lotteryFake) CurrentUserRow(_ context.Context, token string) (store.Row, error) {
	f.log(fakeCall{Method: "CurrentUserRow", Args: []any{token}})
	if f.meErr != nil {
		return nil, f.meErr
	}
	switch token {
	case "":
		return nil, nil
	case lotteryAdminToken:
		return socialAdmin(), nil
	case lotteryUserToken:
		if f.me != nil {
			return f.me, nil
		}
		return socialMe(), nil
	}
	return nil, nil
}

func (f *lotteryFake) QueryValue(_ context.Context, q string, args ...any) (any, bool, error) {
	f.log(fakeCall{Method: "QueryValue", Query: q, Args: args})
	if f.valFn != nil {
		// valFn 命中（ok=true）或用例显式注入错误时才生效，否则继续走内置脚本，
		// 这样用例只写它关心的那几条 SQL 即可。
		if v, ok, err := f.valFn(q, args); ok || err != nil {
			return v, ok, err
		}
	}
	switch {
	case strings.Contains(q, "FROM `settings` WHERE `key` = ?"):
		v, ok := f.settings[phpStr(args[0])]
		return v, ok, nil
	case strings.Contains(q, "SELECT NOW()"):
		return f.nowText, true, nil
	case strings.Contains(q, "UNIX_TIMESTAMP(NOW())"):
		return f.nowTS, true, nil
	case strings.Contains(q, "UNIX_TIMESTAMP(MAX(created_at))"):
		return f.lastDrawTS, true, nil
	case strings.Contains(q, "u.lottery_day_reset_at IS NULL OR d.created_at > u.lottery_day_reset_at"):
		return f.dayDrawn, true, nil
	case strings.Contains(q, "u.lottery_reset_at IS NULL OR d.created_at > u.lottery_reset_at"):
		return f.totalDrawn, true, nil
	case strings.Contains(q, "SELECT COUNT(*) FROM lottery_draws WHERE created_at >= ? AND created_at < ?"):
		return f.dailyTotal, true, nil
	case strings.Contains(q, "FROM lottery_codes WHERE user_id = 0"):
		return f.unusedCnt, true, nil
	case strings.Contains(q, "FROM lottery_codes WHERE user_id > 0"):
		return f.usedCnt, true, nil
	case strings.Contains(q, "SELECT COUNT(*) FROM lottery_draws WHERE user_id = ?"):
		return f.rowTotal, true, nil
	}
	for _, r := range f.valRules {
		if strings.Contains(q, r.match) {
			return r.val, r.ok, r.err
		}
	}
	return nil, false, nil
}

func (f *lotteryFake) QueryRow(ctx context.Context, q string, args ...any) (store.Row, error) {
	f.log(fakeCall{Method: "QueryRow", Query: q, Args: args})
	if f.rowFn != nil {
		return f.rowFn(q, args)
	}
	return nil, nil
}

func (f *lotteryFake) QueryAll(ctx context.Context, q string, args ...any) ([]store.Row, error) {
	f.log(fakeCall{Method: "QueryAll", Query: q, Args: args})
	if f.allFn != nil {
		return f.allFn(q, args)
	}
	return nil, nil
}

func (f *lotteryFake) Exec(ctx context.Context, q string, args ...any) (store.ExecResult, error) {
	f.log(fakeCall{Method: "Exec", Query: q, Args: args})
	if f.execErrOn != "" && strings.Contains(q, f.execErrOn) {
		return store.ExecResult{}, errTestDB
	}
	if f.execFn != nil {
		return f.execFn(q, args)
	}
	// settings 的 UPSERT 内置进假 DB：这样 config_set 结束后回读到的就是刚写入的值。
	if strings.Contains(q, "INSERT INTO `settings`") && len(args) >= 2 {
		f.mu.Lock()
		f.settings[phpStr(args[0])] = phpStr(args[1])
		f.mu.Unlock()
		return store.ExecResult{LastInsertID: 1, RowsAffected: 1}, nil
	}
	id := f.nextExecID
	f.nextExecID++
	n := f.execAffects
	if n == 0 {
		n = 1
	}
	return store.ExecResult{LastInsertID: id, RowsAffected: n}, nil
}

var errTestDB = &testDBError{}

type testDBError struct{}

func (*testDBError) Error() string { return "假 DB 注入的故障" }

// BeginLotteryTx 让假 DB 支持显式事务（生产走 *store.Store.BeginLotteryTx）。
func (f *lotteryFake) BeginLotteryTx(context.Context) (lotteryTx, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	f.steps = append(f.steps, "BEGIN")
	return &lotteryFakeTx{f: f}, nil
}

type lotteryFakeTx struct {
	f    *lotteryFake
	done bool
}

func (t *lotteryFakeTx) step(q string) {
	t.f.mu.Lock()
	t.f.steps = append(t.f.steps, lotteryStepLabel(q))
	t.f.mu.Unlock()
}

func (t *lotteryFakeTx) QueryAll(ctx context.Context, q string, args ...any) ([]store.Row, error) {
	t.step(q)
	return t.f.QueryAll(ctx, q, args...)
}

func (t *lotteryFakeTx) QueryRow(ctx context.Context, q string, args ...any) (store.Row, error) {
	t.step(q)
	return t.f.QueryRow(ctx, q, args...)
}

func (t *lotteryFakeTx) QueryValue(ctx context.Context, q string, args ...any) (any, bool, error) {
	t.step(q)
	return t.f.QueryValue(ctx, q, args...)
}

func (t *lotteryFakeTx) Exec(ctx context.Context, q string, args ...any) (store.ExecResult, error) {
	t.step(q)
	return t.f.Exec(ctx, q, args...)
}

func (t *lotteryFakeTx) Commit() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	if t.f.commitErr != nil {
		return t.f.commitErr
	}
	t.f.commits++
	t.f.steps = append(t.f.steps, "COMMIT")
	t.done = true
	return nil
}

func (t *lotteryFakeTx) Rollback() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	if !t.done {
		t.f.rollbacks++
		t.f.steps = append(t.f.steps, "ROLLBACK")
		t.done = true
	}
	return nil
}

func lotteryStepLabel(q string) string {
	switch {
	case strings.Contains(q, "FROM lottery_prizes p WHERE p.is_active = 1") && !strings.Contains(q, "ORDER BY p.sort_order"):
		return "BEGIN_SELECT_POOL"
	case strings.Contains(q, "FOR UPDATE"):
		return "SELECT_FOR_UPDATE"
	case strings.HasPrefix(strings.TrimSpace(q), "UPDATE lottery_codes"):
		return "UPDATE_CODES"
	case strings.HasPrefix(strings.TrimSpace(q), "INSERT INTO lottery_draws"):
		return "INSERT_DRAWS"
	}
	return "SQL"
}

func (f *lotteryFake) txSteps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.steps...)
}

func (f *lotteryFake) execs(match string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == "Exec" && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

func shanghai(y int, mo time.Month, d, h, mi, s int) int64 {
	return time.Date(y, mo, d, h, mi, s, 0, config.LocalZone()).Unix()
}

func newLotteryRouter(db *lotteryFake) *Router {
	return New(Env{Cfg: config.Load(), Log: testLogger(), Accounts: db})
}

// lotteryTestScope 造一个带真实 Ctx 的 scope（纯函数用例需要 cfg() 走假 DB）。
func lotteryTestScope(t *testing.T, f *lotteryFake) *lotteryScope {
	t.Helper()
	rt := newLotteryRouter(f)
	req := httptest.NewRequest("GET", "/api.php?action=lottery_info", nil)
	c := rt.env.newCtx(httptest.NewRecorder(), req, "lottery_info")
	return newLotteryScope(rt, c)
}

func callLottery(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json",
		map[string]string{"Authorization": "Bearer " + lotteryUserToken})
}

func callLotteryAdmin(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json",
		map[string]string{"Authorization": "Bearer " + lotteryAdminToken})
}

// lotteryCfgJSON 生成 settings.lottery_config 的 JSON 文本（键序由 Go 的 map 排序决定，
// 但 18 个已知键在输出里始终按默认顺序排列，所以这里不影响逐字断言）。
func lotteryCfgJSON(over map[string]any) string {
	cfg := map[string]any{
		"enabled":           1,
		"title":             "免费抽卡密",
		"content":           "",
		"success_text":      "",
		"empty_text":        "奖品已抽完, 请稍后再来",
		"per_user_limit":    1,
		"daily_limit":       0,
		"week_limit":        0,
		"daily_reset_time":  "00:00",
		"cooldown_seconds":  0,
		"daily_total_limit": 0,
		"windows_enabled":   0,
		"windows":           []any{},
		"start_date":        "",
		"end_date":          "",
		"show_prizes":       1,
		"show_stock":        1,
		"notify_winner":     1,
	}
	for k, v := range over {
		cfg[k] = v
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

var lotteryPrizePool = []store.Row{
	srow("id", 1, "name", "月卡", "card_type", "月卡", "weight", 100, "stock", 3),
}

func lotteryPoolAllFn(rows []store.Row) func(string, []any) ([]store.Row, error) {
	return func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM lottery_prizes p WHERE p.is_active = 1") && !strings.Contains(q, "ORDER BY p.sort_order") {
			return rows, nil
		}
		if strings.Contains(q, "ORDER BY p.sort_order ASC, p.id ASC") {
			out := []store.Row{}
			for _, r := range rows {
				out = append(out, srow("id", r["id"], "name", r["name"], "card_type", r["card_type"],
					"weight", r["weight"], "left_cnt", r["stock"]))
			}
			return out, nil
		}
		return nil, nil
	}
}

// ---------- 三态 / weight<=0 / 逐字拒绝文案 ----------

func TestLotteryDrawRejections(t *testing.T) {
	base := func() *lotteryFake {
		f := newLotteryFake()
		f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
		f.allFn = lotteryPoolAllFn(lotteryPrizePool)
		f.rowFn = func(q string, args []any) (store.Row, error) {
			if strings.Contains(q, "FOR UPDATE") {
				return srow("code_id", 9, "code", "CARD-1", "prize_id", 1, "prize_name", "月卡", "card_type", "月卡"), nil
			}
			return nil, nil
		}
		return f
	}

	cases := []struct {
		name    string
		mutate  func(f *lotteryFake)
		wantMsg string
	}{
		{"活动关闭", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"enabled": 0, "per_user_limit": 5})
		}, "抽奖活动已关闭"},
		{"还没开始", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"start_date": "2099-01-01", "per_user_limit": 5})
		}, "抽奖活动还没开始"},
		{"已经结束", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"end_date": "2000-01-01", "per_user_limit": 5})
		}, "抽奖活动已经结束"},
		{"不在开放时段（追加下次开放）", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{
				"per_user_limit": 5, "windows_enabled": 1,
				"windows": []any{map[string]any{"start": "19:30", "end": "20:00", "days": []any{}}},
			})
			f.nowTS = shanghai(2026, 6, 1, 12, 0, 0)
		}, "现在不在抽奖时间内, 下次开放: 2026-06-01 19:30:00"},
		{"冷却中", func(f *lotteryFake) {
			f.nowTS = shanghai(2026, 6, 1, 12, 0, 0)
			f.lastDrawTS = f.nowTS - 10
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "cooldown_seconds": 60})
		}, "抽得太快啦, 请 50 秒后再试"},
		{"全站当日发完", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "daily_total_limit": 1})
			f.dailyTotal = 1
		}, "今天的奖品已经发完了, 明天再来"},
		{"今日次数用完", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "daily_limit": 1})
			f.dayDrawn = 1
		}, "今天的抽奖次数已用完, 明天再来"},
		{"本周次数用完", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "week_limit": 1})
			f.dayDrawn = 1
		}, "本周的抽奖次数已用完, 下周再来"},
		{"总次数用完", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 1})
			f.totalDrawn = 1
		}, "你的抽奖次数已用完"},
		{"池子为空用 empty_text", func(f *lotteryFake) {
			f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "empty_text": "奖池空了, 晚点再来"})
			f.allFn = lotteryPoolAllFn([]store.Row{srow("id", 1, "name", "月卡", "card_type", "月卡", "weight", 100, "stock", 0)})
		}, "奖池空了, 晚点再来"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.mutate(f)
			w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`)
			assertError(t, w, 400, 1, tc.wantMsg)
			if got := f.commits; got != 0 {
				t.Fatalf("被拒绝时不该提交事务, commits=%d", got)
			}
		})
	}

	t.Run("未登录 401", func(t *testing.T) {
		f := base()
		w := callAction(newLotteryRouter(f), "POST", "/api.php?action=lottery_draw", `{}`, "application/json", nil)
		assertError(t, w, 401, 401, "登录已失效")
	})
}

// TestLotteryDrawThreeStateMinusOne 钉死 `-1 = 不限` 的三态语义：
// 默认 daily_limit/week_limit/daily_total_limit 都是 0（= 不限），
// 哪怕今天的「已发/已抽」计数很大也**不能**被拦（用 <=0 判定就会误杀）。
func TestLotteryDrawThreeStateMinusOne(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
	f.allFn = lotteryPoolAllFn(lotteryPrizePool)
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FOR UPDATE") {
			return srow("code_id", 9, "code", "CARD-1", "prize_id", 1, "prize_name", "月卡", "card_type", "月卡"), nil
		}
		return nil, nil
	}
	f.dayDrawn = 999
	f.dailyTotal = 999
	w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("daily_limit/week_limit/daily_total_limit 全为 0 时应当放行, HTTP=%d body=%s", w.Code, w.Body.String())
	}

	// 反面：把 daily_limit 设成 1、今日已抽 1（= 0，不是 -1）→ 必须拦。
	f2 := newLotteryFake()
	f2.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "daily_limit": 1})
	f2.allFn = lotteryPoolAllFn(lotteryPrizePool)
	f2.dayDrawn = 1
	assertError(t, callLottery(t, newLotteryRouter(f2), "lottery_draw", `{}`),
		400, 1, "今天的抽奖次数已用完, 明天再来")
}

// TestLotteryDrawWeightZeroTreatedAs100 钉死 weight<=0 当 100（不是「不参与」）。
func TestLotteryDrawWeightZeroTreatedAs100(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
	f.allFn = lotteryPoolAllFn([]store.Row{
		srow("id", 1, "name", "零权重", "card_type", "通用", "weight", 0, "stock", 1),
	})
	var pickedPrizeID int64
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FOR UPDATE") {
			pickedPrizeID = phpInt(args[0])
			return srow("code_id", 9, "code", "CARD-Z", "prize_id", 1, "prize_name", "零权重", "card_type", "通用"), nil
		}
		return nil, nil
	}
	w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`)
	if w.Code != 200 {
		t.Fatalf("weight=0 的奖项必须被当成 100 参与抽奖, HTTP=%d body=%s", w.Code, w.Body.String())
	}
	if pickedPrizeID != 1 {
		t.Fatalf("选中的奖项 id=%d, 期望 1（weight<=0 当 100 后仍有 100 权重）", pickedPrizeID)
	}
}

// TestLotteryDrawNoStockSkipped stock<=0 的奖项直接不进池子。
func TestLotteryDrawNoStockSkipped(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
	f.allFn = lotteryPoolAllFn([]store.Row{
		srow("id", 1, "name", "没货", "card_type", "通用", "weight", 100, "stock", 0),
		srow("id", 2, "name", "有货", "card_type", "通用", "weight", 100, "stock", 2),
	})
	var asked []int64
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FOR UPDATE") {
			asked = append(asked, phpInt(args[0]))
			return srow("code_id", 9, "code", "CARD-2", "prize_id", 2, "prize_name", "有货", "card_type", "通用"), nil
		}
		return nil, nil
	}
	if w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`); w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	if len(asked) != 1 || asked[0] != 2 {
		t.Fatalf("只应该向「有货」的奖项取卡密, asked=%v", asked)
	}
}

// ---------- 事务顺序 ----------

func TestLotteryDrawTransactionOrder(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
	f.allFn = lotteryPoolAllFn(lotteryPrizePool)
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FOR UPDATE") {
			return srow("code_id", 9, "code", "CARD-1", "prize_id", 1, "prize_name", "月卡", "card_type", "月卡"), nil
		}
		return nil, nil
	}
	w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	if f.commits != 1 || f.rollbacks != 0 {
		t.Fatalf("commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
	want := []string{"BEGIN", "BEGIN_SELECT_POOL", "SELECT_FOR_UPDATE", "UPDATE_CODES", "INSERT_DRAWS", "COMMIT"}
	got := f.txSteps()
	if len(got) != len(want) {
		t.Fatalf("事务内 SQL 顺序不对\n got=%v\nwant=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("事务内 SQL 顺序不对\n got=%v\nwant=%v", got, want)
		}
	}
	// 通知必须在 commit 之后写（PHP api.php:1515 在事务外）。
	if len(f.execs("INSERT INTO notifications")) != 1 {
		t.Fatalf("应当写一条中奖通知, execs=%v", f.execs("INSERT INTO notifications"))
	}
	if n := len(f.execs("notification")); n == 0 {
		t.Fatalf("缺少通知写入")
	}
}

func TestLotteryDrawNotifyDisabled(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5, "notify_winner": 0})
	f.allFn = lotteryPoolAllFn(lotteryPrizePool)
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FOR UPDATE") {
			return srow("code_id", 9, "code", "CARD-1", "prize_id", 1, "prize_name", "月卡", "card_type", "月卡"), nil
		}
		return nil, nil
	}
	if w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`); w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	if len(f.execs("INSERT INTO notifications")) != 0 {
		t.Fatalf("notify_winner=0 时不该写通知")
	}
}

func TestLotteryDrawEmptyPoolRollsBack(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
	f.allFn = lotteryPoolAllFn(nil) // 池子空
	w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`)
	assertError(t, w, 400, 1, "奖品已抽完, 请稍后再来")
	if f.rollbacks != 1 || f.commits != 0 {
		t.Fatalf("池子空必须 rollback, commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
	if len(f.execs("INSERT INTO lottery_draws")) != 0 {
		t.Fatalf("回滚路径不该写中奖记录")
	}
}

func TestLotteryDrawCardGoneRollsBack(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 5})
	f.allFn = lotteryPoolAllFn(lotteryPrizePool)
	f.rowFn = func(q string, args []any) (store.Row, error) { return nil, nil } // 选中奖项刚好被抢空
	w := callLottery(t, newLotteryRouter(f), "lottery_draw", `{}`)
	assertError(t, w, 400, 1, "奖品已抽完, 请稍后再来")
	if f.rollbacks != 1 || f.commits != 0 {
		t.Fatalf("取不到卡密必须 rollback, commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
}

// ---------- 并发：同一张卡密不得发给两个人 ----------

func TestLotteryDrawConcurrentNoDoubleIssue(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 999})
	f.allFn = lotteryPoolAllFn(lotteryPrizePool)

	const n = 8
	var poolMu sync.Mutex
	pool := []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8"}
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if !strings.Contains(q, "FOR UPDATE") {
			return nil, nil
		}
		// 模拟 InnoDB 的 FOR UPDATE 串行化：取走即不再可见。
		poolMu.Lock()
		defer poolMu.Unlock()
		if len(pool) == 0 {
			return nil, nil
		}
		code := pool[0]
		pool = pool[1:]
		return srow("code_id", int64(len(pool)+1), "code", code, "prize_id", 1, "prize_name", "月卡", "card_type", "月卡"), nil
	}

	rt := newLotteryRouter(f)
	var wg sync.WaitGroup
	results := make([]string, n)
	codes := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := callAction(rt, "POST", "/api.php?action=lottery_draw", `{}`, "application/json",
				map[string]string{"Authorization": "Bearer " + lotteryUserToken})
			results[i] = w.Body.String()
			var shell struct {
				Code int `json:"code"`
				Data struct {
					Code string `json:"code"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &shell); err == nil && shell.Code == 0 {
				codes[i] = shell.Data.Code
			}
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for _, c := range codes {
		if c == "" {
			t.Fatalf("并发抽奖有失败响应: %v", results)
		}
		seen[c]++
	}
	if len(seen) != n {
		t.Fatalf("同一张卡密被重复发放: %v", seen)
	}
	if f.commits != n || f.rollbacks != 0 {
		t.Fatalf("commits=%d rollbacks=%d, 期望 %d/0", f.commits, f.rollbacks, n)
	}
}

// ---------- lottery_records ----------

func TestLotteryRecordsPaginationAndShape(t *testing.T) {
	f := newLotteryFake()
	f.rowTotal = 42
	var listSQL string
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		listSQL = q
		return []store.Row{
			srow("id", 7, "prize_id", 1, "code", "CARD-7", "created_at", "2026-06-01 10:00:00",
				"prize_name", "月卡", "card_type", "月卡"),
		}, nil
	}
	w := callLottery(t, newLotteryRouter(f), "lottery_records", `{"page":0,"page_size":999}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":7,"prize_id":1,"prize_name":"月卡","card_type":"月卡","code":"CARD-7","created_at":"2026-06-01 10:00:00"}],"total":42,"page":1,"page_size":50}}`
	if w.Body.String() != want {
		t.Fatalf("响应逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	if !strings.Contains(listSQL, "LIMIT 50 OFFSET 0") {
		t.Fatalf("LIMIT/OFFSET 拼接不对: %s", listSQL)
	}

	f.allFn = func(q string, args []any) ([]store.Row, error) {
		listSQL = q
		return nil, nil
	}
	w2 := callLottery(t, newLotteryRouter(f), "lottery_records", `{"page":2,"page_size":20}`)
	if !strings.Contains(listSQL, "LIMIT 20 OFFSET 20") {
		t.Fatalf("LIMIT/OFFSET 拼接不对: %s", listSQL)
	}
	if !strings.Contains(w2.Body.String(), `"list":[]`) {
		t.Fatalf("空列表必须输出 [], body=%s", w2.Body.String())
	}
}

// ---------- lottery_info ----------

func TestLotteryInfoFieldOrderAndTypes(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"per_user_limit": 3, "daily_limit": 2, "week_limit": 0})
	f.allFn = lotteryPoolAllFn([]store.Row{
		srow("id", 1, "name", "月卡", "card_type", "月卡", "weight", 100, "stock", 5),
		srow("id", 2, "name", "已空", "card_type", "通用", "weight", 0, "stock", 0),
	})
	f.dayDrawn = 1
	f.totalDrawn = 1
	w := callLottery(t, newLotteryRouter(f), "lottery_info", `{}`)
	var shell struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &shell); err != nil {
		t.Fatalf("不是合法 JSON: %s", w.Body.String())
	}
	// 键顺序逐字断言（29 键）。
	wantKeys := []string{
		"enabled", "title", "content", "per_user_limit", "my_quota", "my_drawn", "daily_limit",
		"my_today_drawn", "my_today_left", "server_time", "window", "daily_reset_time", "week_limit",
		"my_week_drawn", "my_week_left", "cooldown_seconds", "cooldown_left", "show_prizes", "show_stock",
		"daily_total_limit", "daily_total_left", "success_text", "empty_text", "start_date", "end_date",
		"windows_enabled", "windows", "prizes", "records",
	}
	body := w.Body.String()
	idx := -1
	for _, k := range wantKeys {
		p := strings.Index(body, `"`+k+`":`)
		if p < 0 {
			t.Fatalf("缺少字段 %s, body=%s", k, body)
		}
		if p < idx {
			t.Fatalf("字段顺序不对: %s 出现在位置 %d（前一个在 %d）\n%s", k, p, idx, body)
		}
		idx = p
	}
	if !strings.Contains(body, `"prizes":[{"id":1,"name":"月卡","card_type":"月卡","left":5,"weight":100}]`) {
		t.Fatalf("prizes 过滤/字段不对: %s", body)
	}
	if !strings.Contains(body, `"window":{"open":true,"reason":"open","reason_text":"","text":"","next_open_at":"","next_close_at":"","seconds_to_open":0,"seconds_to_close":0,"server_time":"2026-06-01 12:00:00","my_allowed":true}`) {
		t.Fatalf("window 10 键顺序/类型不对: %s", body)
	}
	if !strings.Contains(body, `"my_today_left":1`) || !strings.Contains(body, `"my_week_left":-1`) {
		t.Fatalf("每日/每周剩余（含 -1 不限）不对: %s", body)
	}
	if !strings.Contains(body, `"daily_total_left":-1`) {
		t.Fatalf("daily_total_limit=0 应当输出 -1: %s", body)
	}
	if !strings.Contains(body, `"records":[]`) {
		t.Fatalf("空记录必须是 []: %s", body)
	}
}

func TestLotteryInfoShowPrizesZero(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"show_prizes": 0})
	f.allFn = lotteryPoolAllFn(lotteryPrizePool)
	w := callLottery(t, newLotteryRouter(f), "lottery_info", `{}`)
	if !strings.Contains(w.Body.String(), `"prizes":[]`) {
		t.Fatalf("show_prizes=0 时必须输出空数组: %s", w.Body.String())
	}
}

func TestLotteryInfoWindowReasons(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		now     int64
		reason  string
		text    string
		allowed string
		open    string
	}{
		{"关闭", map[string]any{"enabled": 0}, shanghai(2026, 6, 1, 12, 0, 0), "disabled", "抽奖活动已关闭", "false", "false"},
		{"未开始", map[string]any{"enabled": 1, "start_date": "2099-01-01"}, shanghai(2026, 6, 1, 12, 0, 0), "before_start", "抽奖活动还没开始", "false", "false"},
		{"已结束", map[string]any{"enabled": 1, "end_date": "2000-01-01"}, shanghai(2026, 6, 1, 12, 0, 0), "after_end", "抽奖活动已经结束", "false", "false"},
		{"不在时段", map[string]any{"enabled": 1, "windows_enabled": 1,
			"windows": []any{map[string]any{"start": "19:30", "end": "20:00", "days": []any{}}}},
			shanghai(2026, 6, 1, 12, 0, 0), "outside_window", "现在不在抽奖时间内", "false", "false"},
		{"时段内", map[string]any{"enabled": 1, "windows_enabled": 1,
			"windows": []any{map[string]any{"start": "12:00", "end": "13:00", "days": []any{}}}},
			shanghai(2026, 6, 1, 12, 30, 0), "open", "", "true", "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLotteryFake()
			f.nowTS = tc.now
			f.settings["lottery_config"] = lotteryCfgJSON(tc.cfg)
			w := callLottery(t, newLotteryRouter(f), "lottery_info", `{}`)
			body := w.Body.String()
			if !strings.Contains(body, `"reason":"`+tc.reason+`"`) {
				t.Fatalf("reason 不对: %s", body)
			}
			if !strings.Contains(body, `"reason_text":"`+tc.text+`"`) {
				t.Fatalf("reason_text 不对: %s", body)
			}
			if !strings.Contains(body, `"my_allowed":`+tc.allowed) {
				t.Fatalf("my_allowed 不对: %s", body)
			}
			if !strings.Contains(body, `"open":`+tc.open) {
				t.Fatalf("open 不对: %s", body)
			}
		})
	}
}

// ---------- 时段纯函数 ----------

func TestLotteryWindowCrossMidnight(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"windows_enabled": 1,
		"windows": []any{map[string]any{"start": "23:30", "end": "00:30", "days": []any{1}}}})
	cfg := lotteryTestScope(t, f).cfg()
	// 2026-06-01 是周一。
	if !lotteryWindowTSOpen(cfg, shanghai(2026, 6, 1, 23, 45, 0)) {
		t.Fatalf("周一 23:45 应当命中跨午夜时段")
	}
	// 周二 00:15 属于「周一开始的段」。
	if !lotteryWindowTSOpen(cfg, shanghai(2026, 6, 2, 0, 15, 0)) {
		t.Fatalf("周二 00:15 应当命中周一开始的跨午夜时段")
	}
	if lotteryWindowTSOpen(cfg, shanghai(2026, 6, 2, 1, 0, 0)) {
		t.Fatalf("周二 01:00 不该命中")
	}
	if lotteryWindowTSOpen(cfg, shanghai(2026, 6, 3, 23, 45, 0)) {
		t.Fatalf("周三 23:45 不该命中（days=[1]）")
	}
}

func TestLotteryWindowsText(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"windows_enabled": 1, "windows": []any{
		map[string]any{"start": "19:30", "end": "20:00", "days": []any{3, 1}},
		map[string]any{"start": "10:00", "end": "11:00", "days": []any{6}},
	}})
	cfg := lotteryTestScope(t, f).cfg()
	if got := lotteryWindowsText(cfg); got != "周一、周三 19:30-20:00; 周六 10:00-11:00" {
		t.Fatalf("windows text=%q", got)
	}
}

func TestLotteryWindowsEnabledZeroHidesText(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"windows_enabled": 0, "windows": []any{
		map[string]any{"start": "19:30", "end": "20:00", "days": []any{}},
	}})
	cfg := lotteryTestScope(t, f).cfg()
	if got := lotteryWindowsText(cfg); got != "" {
		t.Fatalf("windows_enabled=0 时 text 必须为空, got=%q", got)
	}
	if !lotteryWindowTSOpen(cfg, shanghai(2026, 6, 1, 3, 0, 0)) {
		t.Fatalf("windows_enabled=0 时任何时候都算开放")
	}
}

func TestLotteryDayWindowResetTime(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"daily_reset_time": "04:00"})
	cfg := lotteryTestScope(t, f).cfg()
	// 03:59 属于「昨天 04:00 起」的今日。
	ws, we := lotteryDayWindow(cfg, shanghai(2026, 6, 1, 3, 59, 0))
	if phpDateTimeAt(ws) != "2026-05-31 04:00:00" || phpDateTimeAt(we) != "2026-06-01 04:00:00" {
		t.Fatalf("day window=%s..%s", phpDateTimeAt(ws), phpDateTimeAt(we))
	}
	ws2, _ := lotteryDayWindow(cfg, shanghai(2026, 6, 1, 4, 1, 0))
	if phpDateTimeAt(ws2) != "2026-06-01 04:00:00" {
		t.Fatalf("day window start=%s", phpDateTimeAt(ws2))
	}
}

// ---------- phpjson 保序助手 ----------

func TestPhpjsonPutKeepsOrder(t *testing.T) {
	o := phpjson.New().Set("a", 1).Set("b", 2).Put("a", 9).Set("c", 3)
	if got := string(phpjson.Marshal(o)); got != `{"a":9,"b":2,"c":3}` {
		t.Fatalf("Put 未原位改值: %s", got)
	}
}
