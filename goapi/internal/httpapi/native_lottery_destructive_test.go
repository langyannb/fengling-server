package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5F：6 个破坏性后台抽奖 action 的单测。
//
// 断言口径（删数据的代码，容不得"大概对"）：
//   - 发出的 **SQL 文本逐字** + 顺序（顺序错了就是语义错）；
//   - 绑定参数逐个比对；
//   - 响应体逐字节（键序 = PHP json_encode 的输出顺序）；
//   - 鉴权：未登录 401 / 已登录非管理员 HTTP 400 + code:403；
//   - param 缺省值与显式 JSON null（PHP 的 isset 语义：null 等于没传）；
//   - PHP 这几段**没有事务** ⇒ 中途失败时前面的语句已生效，不许出现 BEGIN/ROLLBACK。

// ---------- 假 DB 上的断言小工具 ----------

// reads 取全部 QueryValue 调用（保持顺序；CurrentUserRow 不算）。
func reads(f *lotteryFake) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []fakeCall{}
	for _, c := range f.calls {
		if c.Method == "QueryValue" {
			out = append(out, c)
		}
	}
	return out
}

// writes 取全部 Exec 调用（保持顺序）。
func writes(f *lotteryFake) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []fakeCall{}
	for _, c := range f.calls {
		if c.Method == "Exec" {
			out = append(out, c)
		}
	}
	return out
}

// assertSQLSeq 断言 SQL 条数与逐字文本（顺序敏感）。
func assertSQLSeq(t *testing.T, got []fakeCall, want []string) {
	t.Helper()
	gs := make([]string, len(got))
	for i, c := range got {
		gs[i] = c.Query
	}
	if len(gs) != len(want) {
		t.Fatalf("SQL 条数不符\n got=%#v\nwant=%#v", gs, want)
	}
	for i := range want {
		if gs[i] != want[i] {
			t.Fatalf("第 %d 条 SQL 逐字不一致\n got=%s\nwant=%s", i+1, gs[i], want[i])
		}
	}
}

// assertArgs 断言一条调用的绑定参数（PHP 的占位符顺序）。
func assertArgs(t *testing.T, c fakeCall, want ...any) {
	t.Helper()
	if len(c.Args) != len(want) {
		t.Fatalf("SQL `%s` 参数个数=%d, 期望 %d: %#v", c.Query, len(c.Args), len(want), c.Args)
	}
	for i := range want {
		if phpStr(c.Args[i]) != phpStr(want[i]) {
			t.Fatalf("SQL `%s` 第 %d 个参数=%q, 期望 %q", c.Query, i+1, phpStr(c.Args[i]), phpStr(want[i]))
		}
	}
}

// valFor 给精确 SQL 文本配返回值（键写错就会落到内核默认脚本 → 用例失败）。
func valFor(f *lotteryFake, m map[string]any) {
	f.valFn = func(q string, args []any) (any, bool, error) {
		if v, ok := m[q]; ok {
			return v, true, nil
		}
		return nil, false, nil
	}
}

// execAffectsFor 给精确 SQL 文本配影响行数。
func execAffectsFor(f *lotteryFake, m map[string]int64) {
	f.execFn = func(q string, args []any) (store.ExecResult, error) {
		n, ok := m[q]
		if !ok {
			n = 1
		}
		return store.ExecResult{LastInsertID: 1, RowsAffected: n}, nil
	}
}

// assertBody 逐字节断言成功响应。
func assertBody(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := w.Body.String(); got != want {
		t.Fatalf("响应体逐字节不一致\n got=%s\nwant=%s", got, want)
	}
}

// assertNoTransaction 断言这几个 action **没有**开事务（PHP 原文就是自动提交）。
func assertNoTransaction(t *testing.T, f *lotteryFake) {
	t.Helper()
	if steps := f.txSteps(); len(steps) != 0 {
		t.Fatalf("PHP 这几段没有事务, 不该出现 %v", steps)
	}
	if f.rollbacks != 0 {
		t.Fatalf("不该有 ROLLBACK, got=%d", f.rollbacks)
	}
}

// ---------- 鉴权：6 个 action 统一口径 ----------

func TestLotteryDestructiveAuthCodes(t *testing.T) {
	actions := []string{
		"admin_lottery_prize_delete",
		"admin_lottery_codes_import",
		"admin_lottery_codes_delete",
		"admin_lottery_quota_reset_all",
		"admin_lottery_activity_reset",
		"admin_lottery_quota_all",
	}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			f := newLotteryFake()
			rt := newLotteryRouter(f)

			// 未登录 → HTTP 401 + code:401
			w := callAction(rt, "POST", "/api.php?action="+action, `{}`, "application/json", nil)
			assertError(t, w, 401, 401, "未登录")
			assertCORS(t, w.Header())
			// 鉴权失败时**一条 SQL 都不许发**（删数据接口尤其重要）。
			if got := writes(f); len(got) != 0 {
				t.Fatalf("未登录就发起了写操作: %#v", got)
			}

			// token 不存在 → 401 登录已失效
			w2 := callAction(rt, "POST", "/api.php?action="+action, `{}`, "application/json",
				map[string]string{"Authorization": "Bearer no-such-token"})
			assertError(t, w2, 401, 401, "登录已失效")

			// 普通用户 → HTTP 400 + code:403（config.php 的 else 分支）
			w3 := callLottery(t, rt, action, `{}`)
			assertError(t, w3, 400, 403, "没有权限, 仅管理员可操作")
			if got := writes(f); len(got) != 0 {
				t.Fatalf("非管理员就发起了写操作: %#v", got)
			}
		})
	}
}

// ---------- admin_lottery_prize_delete ----------

func TestAdminLotteryPrizeDeletePath(t *testing.T) {
	f := newLotteryFake()
	valFor(f, map[string]any{
		"SELECT COUNT(*) FROM lottery_draws WHERE prize_id = ?": int64(0),
		"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?": int64(7),
	})
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_delete", `{"id":5}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	assertBody(t, w, `{"code":0,"msg":"ok","data":{"deleted_codes":7}}`)

	// 两条 COUNT 的 SQL 与参数
	r := reads(f)
	assertSQLSeq(t, r, []string{
		"SELECT COUNT(*) FROM lottery_draws WHERE prize_id = ?",
		"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?",
	})
	assertArgs(t, r[0], int64(5))
	assertArgs(t, r[1], int64(5))

	// 先删卡密再删奖项（顺序不能反），参数都是 id
	wr := writes(f)
	assertSQLSeq(t, wr, []string{
		"DELETE FROM lottery_codes WHERE prize_id = ?",
		"DELETE FROM lottery_prizes WHERE id = ?",
	})
	assertArgs(t, wr[0], int64(5))
	assertArgs(t, wr[1], int64(5))
	assertNoTransaction(t, f)
}

func TestAdminLotteryPrizeDeleteRejections(t *testing.T) {
	t.Run("id 缺省 / <=0", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"id":0}`, `{"id":-3}`, `{"id":null}`, `{"id":"abc"}`} {
			f := newLotteryFake()
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_delete", body)
			assertError(t, w, 400, 1, "参数错误")
			if got := writes(f); len(got) != 0 {
				t.Fatalf("body=%s 不该有写操作: %#v", body, got)
			}
		}
	})

	t.Run("已有人中奖必须拒绝", func(t *testing.T) {
		f := newLotteryFake()
		valFor(f, map[string]any{
			"SELECT COUNT(*) FROM lottery_draws WHERE prize_id = ?": int64(1),
		})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_delete", `{"id":5}`)
		assertError(t, w, 400, 1, "该奖项已有用户中奖, 不能删除")
		// 拒绝后连卡密计数都不该再查，更不能删
		if got := reads(f); len(got) != 1 {
			t.Fatalf("只该查一次 lottery_draws: %#v", got)
		}
		if got := writes(f); len(got) != 0 {
			t.Fatalf("拒绝后仍有写操作: %#v", got)
		}
	})

	t.Run("id 用 (int) 前缀解析", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_delete", `{"id":"5abc"}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		assertArgs(t, writes(f)[0], int64(5))
	})
}

// ---------- admin_lottery_codes_import ----------

func TestAdminLotteryCodesImportPath(t *testing.T) {
	f := newLotteryFake()
	valFor(f, map[string]any{
		"SELECT id FROM lottery_prizes WHERE id = ?":                            int64(3),
		"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ? AND user_id = 0": int64(9),
	})
	execAffectsFor(f, map[string]int64{})
	// 文本：两行有效、一行同批重复、一行库内重复、两行非法、一行空行。
	body := `{"prize_id":3,"text":"ABCDEF\r\nGHIJKL\nABCDEF\nDUPCOD\nbad\n12345\n\n  GHIJKL  "}`
	f.execFn = func(q string, args []any) (store.ExecResult, error) {
		if q == "INSERT IGNORE INTO lottery_codes (prize_id, code) VALUES (?, ?)" {
			if phpStr(args[1]) == "DUPCOD" {
				return store.ExecResult{RowsAffected: 0}, nil // 库内已存在
			}
			return store.ExecResult{LastInsertID: 1, RowsAffected: 1}, nil
		}
		return store.ExecResult{RowsAffected: 1}, nil
	}
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_import", body)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	// added: ABCDEF, GHIJKL = 2；duplicate: ABCDEF(同批重复) + DUPCOD(库内重复) + GHIJKL(末尾重复) = 3
	// invalid: bad(3位) + 12345(5位) = 2
	assertBody(t, w, `{"code":0,"msg":"ok","data":{"added":2,"duplicate":3,"invalid":2,"left":9}}`)

	r := reads(f)
	assertSQLSeq(t, r, []string{
		"SELECT id FROM lottery_prizes WHERE id = ?",
		"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ? AND user_id = 0",
	})
	assertArgs(t, r[0], int64(3))
	assertArgs(t, r[1], int64(3))

	// DUPCOD 是合法卡密（6 位），照样要 INSERT —— 由 rowCount()=0 判定为库内重复。
	wr := writes(f)
	if len(wr) != 3 {
		t.Fatalf("同批去重后应 INSERT 三行: %#v", wr)
	}
	for i, code := range []string{"ABCDEF", "GHIJKL", "DUPCOD"} {
		if wr[i].Query != "INSERT IGNORE INTO lottery_codes (prize_id, code) VALUES (?, ?)" {
			t.Fatalf("INSERT 文本不一致: %s", wr[i].Query)
		}
		assertArgs(t, wr[i], int64(3), code)
	}
	assertNoTransaction(t, f)
}

func TestAdminLotteryCodesImportBoundaries(t *testing.T) {
	t.Run("prize_id 缺省", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_import", `{"text":"ABCDEF"}`)
		assertError(t, w, 400, 1, "请选择奖项")
		if got := writes(f); len(got) != 0 {
			t.Fatalf("不该有写操作: %#v", got)
		}
	})

	t.Run("奖项不存在（fetchColumn 为空）", func(t *testing.T) {
		f := newLotteryFake()
		valFor(f, map[string]any{"SELECT id FROM lottery_prizes WHERE id = ?": nil})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_import", `{"prize_id":3,"text":"ABCDEF"}`)
		assertError(t, w, 400, 1, "奖项不存在")
		if got := writes(f); len(got) != 0 {
			t.Fatalf("不该有写操作: %#v", got)
		}
	})

	t.Run("text 缺省或显式 null → 空文本不算错", func(t *testing.T) {
		for _, body := range []string{`{"prize_id":3}`, `{"prize_id":3,"text":null}`, `{"prize_id":3,"text":""}`} {
			f := newLotteryFake()
			valFor(f, map[string]any{
				"SELECT id FROM lottery_prizes WHERE id = ?":                            int64(3),
				"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ? AND user_id = 0": int64(0),
			})
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_import", body)
			if w.Code != 200 {
				t.Fatalf("body=%s HTTP=%d %s", body, w.Code, w.Body.String())
			}
			assertBody(t, w, `{"code":0,"msg":"ok","data":{"added":0,"duplicate":0,"invalid":0,"left":0}}`)
			if got := writes(f); len(got) != 0 {
				t.Fatalf("body=%s 不该 INSERT: %#v", body, got)
			}
		}
	})

	t.Run("长度与字符集边界", func(t *testing.T) {
		// 5 位 → invalid；6 位 → 有效；64 位 → 有效；65 位 → invalid；含 -/_ 合法；含空格/中文非法
		f := newLotteryFake()
		valFor(f, map[string]any{
			"SELECT id FROM lottery_prizes WHERE id = ?":                            int64(3),
			"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ? AND user_id = 0": int64(0),
		})
		codes := []string{
			"ABCDE",                 // 5
			"ABCDEF",                // 6 ✓
			strings.Repeat("A", 64), // 64 ✓
			strings.Repeat("A", 65), // 65 ✗
			"AB-CD_",                // 合法
			"AB CDE",                // 空格 ✗（trim 只去两端）
			"卡密ABCDEF",              // 中文 ✗
		}
		body := `{"prize_id":3,"text":"` + strings.Join(codes, `\n`) + `"}`
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_import", body)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		// 有效 3 条（6位 / 64位 / AB-CD_），非法 4 条
		if !strings.Contains(w.Body.String(), `"added":3`) || !strings.Contains(w.Body.String(), `"invalid":4`) {
			t.Fatalf("body=%s", w.Body.String())
		}
		if got := writes(f); len(got) != 3 {
			t.Fatalf("应只 INSERT 三条: %#v", got)
		}
	})
}

// ---------- admin_lottery_codes_delete ----------

func TestAdminLotteryCodesDeleteAll(t *testing.T) {
	f := newLotteryFake()
	valFor(f, map[string]any{
		"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?": int64(12),
	})
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_delete", `{"prize_id":5,"status":"all"}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	// status=all 返回的是**删除前**数出来的条数
	assertBody(t, w, `{"code":0,"msg":"ok","data":{"deleted":12}}`)

	r := reads(f)
	assertSQLSeq(t, r, []string{"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?"})
	assertArgs(t, r[0], int64(5))

	wr := writes(f)
	assertSQLSeq(t, wr, []string{
		"DELETE FROM lottery_draws WHERE prize_id = ?",
		"DELETE FROM lottery_codes WHERE prize_id = ?",
	})
	assertArgs(t, wr[0], int64(5))
	assertArgs(t, wr[1], int64(5))
	assertNoTransaction(t, f)
}

func TestAdminLotteryCodesDeleteUnused(t *testing.T) {
	t.Run("prize_id>0 带奖项过滤", func(t *testing.T) {
		f := newLotteryFake()
		execAffectsFor(f, map[string]int64{"DELETE FROM lottery_codes WHERE prize_id = ? AND user_id = 0": 3})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_delete", `{"prize_id":5}`)
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"deleted":3}}`)
		wr := writes(f)
		assertSQLSeq(t, wr, []string{"DELETE FROM lottery_codes WHERE prize_id = ? AND user_id = 0"})
		assertArgs(t, wr[0], int64(5))
		if got := reads(f); len(got) != 0 {
			t.Fatalf("unused 分支不该 COUNT: %#v", got)
		}
	})

	t.Run("prize_id=0 清全部奖项的未使用卡密", func(t *testing.T) {
		f := newLotteryFake()
		execAffectsFor(f, map[string]int64{"DELETE FROM lottery_codes WHERE user_id = 0": 0})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_delete", `{"prize_id":0}`)
		// 影响行数为 0 时返回 deleted:0（不是错误）
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"deleted":0}}`)
		wr := writes(f)
		assertSQLSeq(t, wr, []string{"DELETE FROM lottery_codes WHERE user_id = 0"})
		if len(wr[0].Args) != 0 {
			t.Fatalf("这条 DELETE 没有占位符: %#v", wr[0].Args)
		}
	})

	t.Run("status 只认 'all'，其它值一律按未使用清理", func(t *testing.T) {
		for _, status := range []string{`"used"`, `"weird"`, `""`, `null`} {
			f := newLotteryFake()
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_delete",
				`{"prize_id":0,"status":`+status+`}`)
			if w.Code != 200 {
				t.Fatalf("status=%s HTTP=%d %s", status, w.Code, w.Body.String())
			}
			wr := writes(f)
			assertSQLSeq(t, wr, []string{"DELETE FROM lottery_codes WHERE user_id = 0"})
		}
	})

	t.Run("prize_id=0 + status=all 必须报错", func(t *testing.T) {
		for _, body := range []string{`{"status":"all"}`, `{"prize_id":0,"status":"all"}`, `{"prize_id":null,"status":"all"}`} {
			f := newLotteryFake()
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_delete", body)
			assertError(t, w, 400, 1, "请选择奖项")
			if got := writes(f); len(got) != 0 {
				t.Fatalf("body=%s 不该有删除: %#v", body, got)
			}
		}
	})

	t.Run("全部奖项 + status 缺省 → 清未使用", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_delete", `{}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		assertSQLSeq(t, writes(f), []string{"DELETE FROM lottery_codes WHERE user_id = 0"})
	})
}

// ---------- admin_lottery_quota_reset_all ----------

func TestAdminLotteryQuotaResetAll(t *testing.T) {
	t.Run("daily=1 同时写两个重置标记", func(t *testing.T) {
		f := newLotteryFake()
		f.nowText = "2026-06-01 12:00:00"
		execAffectsFor(f, map[string]int64{"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?": 8})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_reset_all", `{"daily":1}`)
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"updated":8,"daily":1}}`)

		r := reads(f)
		assertSQLSeq(t, r, []string{"SELECT NOW()"})
		if len(r[0].Args) != 0 {
			t.Fatalf("SELECT NOW() 无参: %#v", r[0].Args)
		}
		wr := writes(f)
		assertSQLSeq(t, wr, []string{"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?"})
		assertArgs(t, wr[0], "2026-06-01 12:00:00", "2026-06-01 12:00:00")
		// 全表 UPDATE：**没有 WHERE**
		if strings.Contains(wr[0].Query, "WHERE") {
			t.Fatalf("这是一键重置所有人, 不该带 WHERE: %s", wr[0].Query)
		}
		assertNoTransaction(t, f)
	})

	t.Run("daily 缺省/非 1 → 只写 lottery_reset_at", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"daily":0}`, `{"daily":2}`, `{"daily":null}`, `{"daily":"x"}`} {
			f := newLotteryFake()
			execAffectsFor(f, map[string]int64{"UPDATE users SET lottery_reset_at = ?": 0})
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_reset_all", body)
			// 影响行数 0 → updated:0
			assertBody(t, w, `{"code":0,"msg":"ok","data":{"updated":0,"daily":0}}`)
			wr := writes(f)
			assertSQLSeq(t, wr, []string{"UPDATE users SET lottery_reset_at = ?"})
		}
	})

	t.Run("daily=true 等价于 1（PHP (int)true === 1）", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_reset_all", `{"daily":true}`)
		if !strings.Contains(w.Body.String(), `"daily":1`) {
			t.Fatalf("body=%s", w.Body.String())
		}
		assertSQLSeq(t, writes(f), []string{"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?"})
	})
}

// ---------- admin_lottery_activity_reset ----------

func TestAdminLotteryActivityResetModes(t *testing.T) {
	t.Run("mode=activity 全部副作用按序执行", func(t *testing.T) {
		f := newLotteryFake()
		f.nowText = "2026-06-01 12:00:00"
		execAffectsFor(f, map[string]int64{
			"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?":         4,
			"DELETE FROM lottery_draws":                                               9,
			"UPDATE lottery_codes SET user_id = 0, used_at = NULL WHERE user_id <> 0": 5,
			"DELETE FROM notifications WHERE type = 'lottery'":                        2,
		})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_activity_reset", `{"mode":"activity"}`)
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"mode":"activity","users":4,"draws_deleted":9,"codes_released":5,"notices_deleted":2}}`)

		assertSQLSeq(t, reads(f), []string{"SELECT NOW()"})
		wr := writes(f)
		assertSQLSeq(t, wr, []string{
			"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?",
			"DELETE FROM lottery_draws",
			"UPDATE lottery_codes SET user_id = 0, used_at = NULL WHERE user_id <> 0",
			"DELETE FROM notifications WHERE type = 'lottery'",
		})
		assertArgs(t, wr[0], "2026-06-01 12:00:00", "2026-06-01 12:00:00")
		assertAgentlessDeletes(t, wr[1:])
		assertNoTransaction(t, f)
	})

	t.Run("mode=daily 提前返回，只清今日次数", func(t *testing.T) {
		f := newLotteryFake()
		execAffectsFor(f, map[string]int64{"UPDATE users SET lottery_day_reset_at = ?": 6})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_activity_reset", `{"mode":"daily"}`)
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"mode":"daily","users":6,"draws_deleted":0,"codes_released":0,"notices_deleted":0}}`)
		wr := writes(f)
		assertSQLSeq(t, wr, []string{"UPDATE users SET lottery_day_reset_at = ?"})
		assertArgs(t, wr[0], f.nowText)
		assertNoTransaction(t, f)
	})

	t.Run("mode=quota（默认）只清总次数", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"mode":"quota"}`, `{"mode":null}`, `{"mode":"  quota  "}`} {
			f := newLotteryFake()
			execAffectsFor(f, map[string]int64{"UPDATE users SET lottery_reset_at = ?": 2})
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_activity_reset", body)
			assertBody(t, w, `{"code":0,"msg":"ok","data":{"mode":"quota","users":2,"draws_deleted":0,"codes_released":0,"notices_deleted":0}}`)
			assertSQLSeq(t, writes(f), []string{"UPDATE users SET lottery_reset_at = ?"})
		}
	})

	t.Run("activity + release_codes=0/clear_notices=0 少做两步", func(t *testing.T) {
		f := newLotteryFake()
		execAffectsFor(f, map[string]int64{})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_activity_reset",
			`{"mode":"activity","release_codes":0,"clear_notices":0}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		assertSQLSeq(t, writes(f), []string{
			"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?",
			"DELETE FROM lottery_draws",
		})
		if !strings.Contains(w.Body.String(), `"codes_released":0`) || !strings.Contains(w.Body.String(), `"notices_deleted":0`) {
			t.Fatalf("body=%s", w.Body.String())
		}
	})

	t.Run("mode 非法", func(t *testing.T) {
		for _, body := range []string{`{"mode":"all"}`, `{"mode":""}`, `{"mode":"ACTIVITY"}`} {
			f := newLotteryFake()
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_activity_reset", body)
			assertError(t, w, 400, 1, "参数错误")
			if got := writes(f); len(got) != 0 {
				t.Fatalf("body=%s 不该写数据: %#v", body, got)
			}
		}
	})
}

// assertAgentlessDeletes 断言这几条 SQL 都没有占位符（全表操作）。
func assertAgentlessDeletes(t *testing.T, cs []fakeCall) {
	t.Helper()
	for _, c := range cs {
		if len(c.Args) != 0 {
			t.Fatalf("SQL `%s` 不该带参数: %#v", c.Query, c.Args)
		}
	}
}

// ---------- admin_lottery_quota_all ----------

func TestAdminLotteryQuotaAll(t *testing.T) {
	t.Run("正常设置", func(t *testing.T) {
		f := newLotteryFake()
		execAffectsFor(f, map[string]int64{"UPDATE users SET lottery_quota = ?": 8})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_all", `{"quota":3}`)
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"updated":8}}`)
		wr := writes(f)
		assertSQLSeq(t, wr, []string{"UPDATE users SET lottery_quota = ?"})
		assertArgs(t, wr[0], int64(3))
		if strings.Contains(wr[0].Query, "WHERE") {
			t.Fatalf("全表 UPDATE 不该带 WHERE: %s", wr[0].Query)
		}
		assertNoTransaction(t, f)
	})

	t.Run("quota 缺省 = -1（不限）", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"quota":null}`, `{"quota":-1}`} {
			f := newLotteryFake()
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_all", body)
			if w.Code != 200 {
				t.Fatalf("body=%s HTTP=%d %s", body, w.Code, w.Body.String())
			}
			assertArgs(t, writes(f)[0], int64(-1))
		}
	})

	t.Run("<-1 静默成 -1，9999 放行，10000 报错", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_all", `{"quota":-9}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		assertArgs(t, writes(f)[0], int64(-1))

		f2 := newLotteryFake()
		w2 := callLotteryAdmin(t, newLotteryRouter(f2), "admin_lottery_quota_all", `{"quota":9999}`)
		if w2.Code != 200 {
			t.Fatalf("9999 应放行: HTTP=%d body=%s", w2.Code, w2.Body.String())
		}
		assertArgs(t, writes(f2)[0], int64(9999))

		f3 := newLotteryFake()
		w3 := callLotteryAdmin(t, newLotteryRouter(f3), "admin_lottery_quota_all", `{"quota":10000}`)
		assertError(t, w3, 400, 1, "抽奖次数不能超过 9999")
		if got := writes(f3); len(got) != 0 {
			t.Fatalf("超限不该 UPDATE: %#v", got)
		}
	})

	t.Run("影响行数 0 时返回 updated:0", func(t *testing.T) {
		f := newLotteryFake()
		execAffectsFor(f, map[string]int64{"UPDATE users SET lottery_quota = ?": 0})
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_all", `{"quota":0}`)
		assertBody(t, w, `{"code":0,"msg":"ok","data":{"updated":0}}`)
	})
}

// ---------- 中途失败：没有事务 ⇒ 不 ROLLBACK、前面的语句已生效 ----------

func TestLotteryDestructiveFailureIsNotTransactional(t *testing.T) {
	t.Run("quota_reset_all：UPDATE 失败即结束", func(t *testing.T) {
		f := newLotteryFake()
		f.execErrOn = "UPDATE users SET lottery_reset_at"
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_reset_all", `{"daily":1}`)
		assertError(t, w, 500, 500, "服务器错误: 假 DB 注入的故障")
		assertNoTransaction(t, f)
		// SELECT NOW() 已经跑过了（PHP 同样是先取时间再 UPDATE）
		if got := reads(f); len(got) != 1 {
			t.Fatalf("应当先取 DB 时间: %#v", got)
		}
	})

	t.Run("activity_reset：DELETE FROM lottery_draws 失败时 UPDATE 不回滚", func(t *testing.T) {
		f := newLotteryFake()
		f.execErrOn = "DELETE FROM lottery_draws"
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_activity_reset", `{"mode":"activity"}`)
		assertError(t, w, 500, 500, "服务器错误: 假 DB 注入的故障")
		assertNoTransaction(t, f)
		// 失败点之前的 UPDATE users 已经发出去了（自动提交，不会被撤销）
		wr := writes(f)
		assertSQLSeq(t, wr, []string{
			"UPDATE users SET lottery_reset_at = ?, lottery_day_reset_at = ?",
			"DELETE FROM lottery_draws",
		})
	})

	t.Run("codes_import：第 2 行 INSERT 失败即中止，第 1 行已插入", func(t *testing.T) {
		f := newLotteryFake()
		valFor(f, map[string]any{"SELECT id FROM lottery_prizes WHERE id = ?": int64(3)})
		n := 0
		f.execFn = func(q string, args []any) (store.ExecResult, error) {
			n++
			if n == 2 {
				return store.ExecResult{}, errTestDB
			}
			return store.ExecResult{RowsAffected: 1}, nil
		}
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes_import",
			`{"prize_id":3,"text":"ABCDEF\nGHIJKL"}`)
		assertError(t, w, 500, 500, "服务器错误: 假 DB 注入的故障")
		assertNoTransaction(t, f)
		if got := writes(f); len(got) != 2 {
			t.Fatalf("第一条 INSERT 已生效, 第二条失败: %#v", got)
		}
	})
}
