package httpapi

import (
	"net/http"
	"regexp"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// 阶段 5F：6 个**破坏性**后台抽奖 action 的原生实现。
//
//   admin_lottery_prize_delete     api.php:1878
//   admin_lottery_codes_import     api.php:1934
//   admin_lottery_codes_delete     api.php:1958
//   admin_lottery_quota_reset_all  api.php:2173
//   admin_lottery_activity_reset   api.php:2189
//   admin_lottery_quota_all        api.php:2220
//
// 逐字纪律（这是删数据的代码）：
//   - SQL 文本、占位符顺序、绑定参数、影响行数的用法全部照抄 PHP，一个字都不改；
//   - PHP 这几段**都没有用事务**（没有 beginTransaction/commit/rollback），
//     所以这里也只用自动提交：中途某条语句失败时前面的语句已经生效，
//     这与 PHP 的 PDO 默认 autocommit 行为一致 —— 不要"好心"套事务，
//     否则失败现场与 PHP 不再等价；
//   - `db()->exec()` / `$st->rowCount()` 全部映射成 ExecResult.RowsAffected；
//   - 鉴权先于参数解析（require_admin() 在每段的第一行）。

// lotteryCodeRe 对齐 api.php:1943 的 preg_match('/^[A-Za-z0-9_\-]{6,64}$/', $code)。
// PCRE 未加 /u ⇒ 按字节匹配；Go 的字符类同样只含 ASCII，语义一致。
var lotteryCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

// lotteryLineBreakRe 对齐 api.php:1942 的 preg_split('/\r\n|\r|\n/', $text)。
var lotteryLineBreakRe = regexp.MustCompile(`\r\n|\r|\n`)

// btoi 对应 PHP 的 `$cond ? 1 : 0`（json_encode 输出的整数）。
func btoi(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ---------- admin_lottery_prize_delete (api.php:1878) ----------

// ⚠️ 破坏性操作：会删除/重置数据，仅允许管理员在后台面板手工触发。
//
// 顺序逐字照抄 PHP：先看有没有人中过该奖（有则拒绝），再数卡密条数，
// 然后 DELETE lottery_codes → DELETE lottery_prizes，最后返回被删的卡密数。
func (rt *Router) handleAdminLotteryPrizeDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_prize_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	id := c.paramSetInt("id", 0)
	if id <= 0 {
		c.Error("参数错误", 1)
		return
	}
	drawsV, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_draws WHERE prize_id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	if phpInt(drawsV) > 0 {
		c.Error("该奖项已有用户中奖, 不能删除", 1)
		return
	}
	nV, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	n := phpInt(nV)
	if _, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_codes WHERE prize_id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	if _, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_prizes WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("deleted_codes", n), 0, "ok", 0)
}

// ---------- admin_lottery_codes_import (api.php:1934) ----------

// ⚠️ 破坏性操作：会删除/重置数据，仅允许管理员在后台面板手工触发。
//
// 逐行 INSERT IGNORE，用 rowCount() 区分「新增」与「重复」（重复/唯一键冲突时为 0）。
// 同一批文本里的重复只算一次（$seen 去重），格式不合法的计入 invalid。
func (rt *Router) handleAdminLotteryCodesImport(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_codes_import")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	pid := c.paramSetInt("prize_id", 0)
	text := c.paramSetStr("text", "")
	if pid <= 0 {
		c.Error("请选择奖项", 1)
		return
	}
	// PHP: if (!$st->fetchColumn()) —— falsy 判断（无行/id=0 都算不存在）。
	existsV, _, err := s.db().QueryValue(c.R.Context(), "SELECT id FROM lottery_prizes WHERE id = ?", pid)
	if err != nil {
		c.dbError(err)
		return
	}
	if phpFalsy(phpStr(existsV)) {
		c.Error("奖项不存在", 1)
		return
	}
	added, dup, invalid := int64(0), int64(0), int64(0)
	seen := map[string]bool{}
	for _, line := range lotteryLineBreakRe.Split(text, -1) {
		code := phpTrim(line)
		if code == "" {
			continue
		}
		if !lotteryCodeRe.MatchString(code) {
			invalid++
			continue
		}
		// PHP 的 $seen 是数组键去重；纯数字串在 PHP 会被归一化成整数键，
		// 这里用原字符串等价（"0123" 与 "123" 本来就是不同的卡密）。
		if seen[code] {
			dup++
			continue
		}
		seen[code] = true
		res, err := s.db().Exec(c.R.Context(),
			"INSERT IGNORE INTO lottery_codes (prize_id, code) VALUES (?, ?)", pid, code)
		if err != nil {
			c.dbError(err)
			return
		}
		if res.RowsAffected > 0 {
			added++
		} else {
			dup++
		}
	}
	leftV, _, err := s.db().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ? AND user_id = 0", pid)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("added", added).
		Set("duplicate", dup).
		Set("invalid", invalid).
		Set("left", phpInt(leftV)), 0, "ok", 0)
}

// ---------- admin_lottery_codes_delete (api.php:1958) ----------

// ⚠️ 破坏性操作：会删除/重置数据，仅允许管理员在后台面板手工触发。
//
// status='all' 是「连已抽出的记录一起清掉」：先数总数，再删 lottery_draws，
// 再删 lottery_codes，返回的是**删除前**数出来的条数（不是受影响行数）。
// 其它 status 一律按「清未使用卡密」处理；prize_id=0 表示全部奖项。
func (rt *Router) handleAdminLotteryCodesDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_codes_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	pid := c.paramSetInt("prize_id", 0)
	status := c.paramSetStr("status", "unused")
	// 清空未使用卡密时, prize_id = 0 表示「全部奖项」(后台在"全部奖项"筛选下会这样传)
	if pid <= 0 && status == "all" {
		c.Error("请选择奖项", 1)
		return
	}
	if status == "all" {
		nV, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_codes WHERE prize_id = ?", pid)
		if err != nil {
			c.dbError(err)
			return
		}
		n := phpInt(nV)
		if _, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_draws WHERE prize_id = ?", pid); err != nil {
			c.dbError(err)
			return
		}
		if _, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_codes WHERE prize_id = ?", pid); err != nil {
			c.dbError(err)
			return
		}
		c.JSON(phpjson.New().Set("deleted", n), 0, "ok", 0)
		return
	}
	var affected int64
	if pid > 0 {
		r1, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_codes WHERE prize_id = ? AND user_id = 0", pid)
		if err != nil {
			c.dbError(err)
			return
		}
		affected = r1.RowsAffected
	} else {
		// PHP 这里用的是 db()->query(...)->rowCount()，SQL 文本完全相同。
		r1, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_codes WHERE user_id = 0")
		if err != nil {
			c.dbError(err)
			return
		}
		affected = r1.RowsAffected
	}
	c.JSON(phpjson.New().Set("deleted", affected), 0, "ok", 0)
}

// ---------- admin_lottery_quota_reset_all (api.php:2173) ----------

// ⚠️ 破坏性操作：会删除/重置数据，仅允许管理员在后台面板手工触发。
//
// 全表 UPDATE（没有任何 WHERE）：把所有人的 lottery_reset_at（daily=1 时连
// lottery_day_reset_at）改成数据库的 NOW()。时间取 DB 时钟，不用进程时钟。
func (rt *Router) handleAdminLotteryQuotaResetAll(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_quota_reset_all")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	daily := c.paramSetInt("daily", 0) == 1
	nowV, _, err := s.db().QueryValue(c.R.Context(), "SELECT NOW()")
	if err != nil {
		c.dbError(err)
		return
	}
	nowDb := phpStr(nowV)
	sql := "UPDATE users SET lottery_reset_at = ?"
	args := []any{nowDb}
	if daily {
		sql += ", lottery_day_reset_at = ?"
		args = append(args, nowDb)
	}
	res, err := s.db().Exec(c.R.Context(), sql, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("updated", res.RowsAffected).
		Set("daily", btoi(daily)), 0, "ok", 0)
}

// ---------- admin_lottery_activity_reset (api.php:2189) ----------

// ⚠️ 破坏性操作：会删除/重置数据，仅允许管理员在后台面板手工触发。
//
// mode = quota    只清所有人「总抽奖次数」(中奖记录与已用卡密都保留)
// mode = daily    只清所有人「今日已抽次数」(总次数不变, 今天能再抽)
// mode = activity 整个活动重开: 删中奖记录 + 所有人总次数与今日次数归零
//                 + release_codes=1 把已经发出去的卡密退回卡密池(user_id=0)
//                 + clear_notices=1 删掉含卡密的通知, 避免旧卡密还能被看到
//
// daily 分支在 PHP 里是 json_out() 后 exit（提前返回），这里用 return 等价实现；
// 返回键序固定 mode/users/draws_deleted/codes_released/notices_deleted。
func (rt *Router) handleAdminLotteryActivityReset(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_activity_reset")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	mode := phpTrim(c.paramSetStr("mode", "quota"))
	if mode != "quota" && mode != "daily" && mode != "activity" {
		c.Error("参数错误", 1)
		return
	}
	release := c.paramSetInt("release_codes", 1) == 1
	clearNotices := c.paramSetInt("clear_notices", 1) == 1
	nowV, _, err := s.db().QueryValue(c.R.Context(), "SELECT NOW()")
	if err != nil {
		c.dbError(err)
		return
	}
	nowDb := phpStr(nowV)
	res := phpjson.New().
		Set("mode", mode).
		Set("users", int64(0)).
		Set("draws_deleted", int64(0)).
		Set("codes_released", int64(0)).
		Set("notices_deleted", int64(0))

	if mode == "daily" {
		u1, err := s.db().Exec(c.R.Context(), "UPDATE users SET lottery_day_reset_at = ?", nowDb)
		if err != nil {
			c.dbError(err)
			return
		}
		res.Put("users", u1.RowsAffected)
		c.JSON(res, 0, "ok", 0)
		return
	}

	sql := "UPDATE users SET lottery_reset_at = ?"
	args := []any{nowDb}
	if mode == "activity" {
		sql += ", lottery_day_reset_at = ?"
		args = append(args, nowDb)
	}
	u2, err := s.db().Exec(c.R.Context(), sql, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	res.Put("users", u2.RowsAffected)

	if mode == "activity" {
		d1, err := s.db().Exec(c.R.Context(), "DELETE FROM lottery_draws")
		if err != nil {
			c.dbError(err)
			return
		}
		res.Put("draws_deleted", d1.RowsAffected)
		if release {
			c1, err := s.db().Exec(c.R.Context(), "UPDATE lottery_codes SET user_id = 0, used_at = NULL WHERE user_id <> 0")
			if err != nil {
				c.dbError(err)
				return
			}
			res.Put("codes_released", c1.RowsAffected)
		}
		if clearNotices {
			n1, err := s.db().Exec(c.R.Context(), "DELETE FROM notifications WHERE type = 'lottery'")
			if err != nil {
				c.dbError(err)
				return
			}
			res.Put("notices_deleted", n1.RowsAffected)
		}
	}
	c.JSON(res, 0, "ok", 0)
}

// ---------- admin_lottery_quota_all (api.php:2220) ----------

// ⚠️ 破坏性操作：会删除/重置数据，仅允许管理员在后台面板手工触发。
//
// 全表 UPDATE（没有 WHERE）：把所有人的 lottery_quota 改成同一个值。
// -1 = 不限、0 = 用完了。quota < -1 静默成 -1，> 9999 报错。
func (rt *Router) handleAdminLotteryQuotaAll(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_quota_all")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	quota := c.paramSetInt("quota", -1)
	if quota < -1 {
		quota = -1
	}
	if quota > 9999 {
		c.Error("抽奖次数不能超过 9999", 1)
		return
	}
	res, err := s.db().Exec(c.R.Context(), "UPDATE users SET lottery_quota = ?", quota)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("updated", res.RowsAffected), 0, "ok", 0)
}
