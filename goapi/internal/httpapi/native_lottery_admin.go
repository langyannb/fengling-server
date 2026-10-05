package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// 阶段 5A：后台抽奖管理的原生实现（api.php:1830-2227 里除破坏性 4 个之外的 11 个）。
//
// ⛔ 刻意不在此文件实现（继续透传 PHP）：admin_lottery_prize_delete(1878)、
// admin_lottery_codes_import(1934)、admin_lottery_codes_delete(1958)、
// admin_lottery_activity_reset(2189)。理由见 native_lottery.go 顶部注释。

// ---------- PHP 数组语义小工具 ----------

// listFromPHPArray 把一个「PHP 数组」（JSON 对象或 JSON 数组）展开成值列表。
// PHP 的 is_array() 对 object 与 list 都为真，foreach 取的是值。
func listFromPHPArray(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case *phpjson.O:
		out := make([]any, 0, x.Len())
		for _, k := range x.Keys() {
			vv, _ := x.Get(k)
			out = append(out, vv)
		}
		return out, true
	case map[string]any:
		// 请求体 JSON 走 encoding/json（ctx.Body 的既有范式），对象是 map[string]any。
		// map 无序，这里按 key 排序取一个确定顺序（关联数组当 windows 传是病态用法）。
		ks := make([]string, 0, len(x))
		for k := range x {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		out := make([]any, 0, len(ks))
		for _, k := range ks {
			out = append(out, x[k])
		}
		return out, true
	}
	return nil, false
}

// isPHPArray 等价 PHP 的 is_array()。
func isPHPArray(v any) bool {
	switch v.(type) {
	case []any, *phpjson.O, map[string]any:
		return true
	}
	return false
}

// objField 取「PHP 数组」里的键值（非对象形态或键缺失返回 nil）。
func objField(v any, key string) (any, bool) {
	switch x := v.(type) {
	case *phpjson.O:
		return x.Get(key)
	case map[string]any:
		vv, ok := x[key]
		return vv, ok
	}
	return nil, false
}

// phpCheckdate 复刻 PHP checkdate()：year 1-32767、month 1-12、day 1-当月天数（含闰年）。
func phpCheckdate(month, day, year int64) bool {
	if year < 1 || year > 32767 || month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	days := []int64{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[month-1]
	if month == 2 && year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		days = 29
	}
	return day <= days
}

// ---------- 配置校验（api.php:4716-4760） ----------

func (s *lotteryScope) cfgValidateTime(v any) (string, bool) {
	h, m, ok := lotteryCfgHM(phpTrim(phpStr(v)))
	if !ok {
		s.c.Error("每日重置时间格式不对, 应该是 HH:MM", 1)
		return "", false
	}
	return lotteryHMText(h, m), true
}

func (s *lotteryScope) cfgValidateText(field string, v any, max int64) (string, bool) {
	str := phpStr(v)
	if int64(mbStrlen(str)) > max {
		s.c.Error(field+"不能超过 "+phpNum(max)+" 个字", 1)
		return "", false
	}
	return str, true
}

func (s *lotteryScope) cfgValidateDate(field string, v any) (string, bool) {
	str := phpTrim(phpStr(v))
	if str == "" {
		return "", true
	}
	if !lotteryDateRe.MatchString(str) {
		s.c.Error(field+"格式不对, 应该是 YYYY-MM-DD", 1)
		return "", false
	}
	p := strings.Split(str, "-")
	if !phpCheckdate(atoiPrefix(p[1]), atoiPrefix(p[2]), atoiPrefix(p[0])) {
		s.c.Error(field+"格式不对, 应该是 YYYY-MM-DD", 1)
		return "", false
	}
	return str, true
}

// cfgValidateWindows 对齐 api.php:4732 lottery_config_validate_windows()。
func (s *lotteryScope) cfgValidateWindows(v any) ([]any, bool) {
	items, isArr := listFromPHPArray(v)
	if !isArr {
		s.c.Error("抽奖时段格式不对, 应该是 HH:MM-HH:MM", 1)
		return nil, false
	}
	if len(items) > 10 {
		s.c.Error("抽奖时段最多 10 条", 1)
		return nil, false
	}
	out := []any{}
	for _, it := range items {
		if !isPHPArray(it) {
			s.c.Error("抽奖时段格式不对, 应该是 HH:MM-HH:MM", 1)
			return nil, false
		}
		sv, _ := objField(it, "start")
		ev, _ := objField(it, "end")
		ah, am, okA := lotteryCfgHM(phpStr(sv))
		bh, bm, okB := lotteryCfgHM(phpStr(ev))
		if !okA || !okB {
			s.c.Error("抽奖时段格式不对, 应该是 HH:MM-HH:MM", 1)
			return nil, false
		}
		if ah*60+am == bh*60+bm {
			s.c.Error("抽奖时段开始和结束时间不能相同", 1)
			return nil, false
		}
		days := []any{}
		if dv, has := objField(it, "days"); has {
			// PHP: isset($w['days']) && $w['days'] !== '' && $w['days'] !== null
			provided := true
			if dv == nil {
				provided = false
			} else if str, isStr := dv.(string); isStr && str == "" {
				provided = false
			}
			if provided {
				darr, isDaysArr := listFromPHPArray(dv)
				if !isDaysArr {
					s.c.Error("星期只能是 1-7", 1)
					return nil, false
				}
				nums := []int64{}
				for _, d := range darr {
					n := phpInt(d)
					if n < 1 || n > 7 {
						s.c.Error("星期只能是 1-7", 1)
						return nil, false
					}
					nums = append(nums, n)
				}
				for _, n := range lotteryUniqueSorted(nums) {
					days = append(days, n)
				}
			}
		}
		out = append(out, phpjson.New().
			Set("start", lotteryHMText(ah, am)).
			Set("end", lotteryHMText(bh, bm)).
			Set("days", days))
	}
	return out, true
}

// ---------- admin_lottery_prizes (api.php:1830) ----------

func (rt *Router) handleAdminLotteryPrizes(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_prizes")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	rows, err := s.db().QueryAll(c.R.Context(), `SELECT p.*,
            (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id) AS total_cnt,
            (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id AND c.user_id > 0) AS used_cnt
        FROM lottery_prizes p ORDER BY p.sort_order ASC, p.id ASC`)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		weight := rowInt(row, "weight", 0)
		if weight <= 0 {
			weight = 100
		}
		total := rowInt(row, "total_cnt", 0)
		used := rowInt(row, "used_cnt", 0)
		list = append(list, phpjson.New().
			Set("id", rowInt(row, "id", 0)).
			Set("name", rowStr(row, "name", "")).
			Set("card_type", rowStr(row, "card_type", "")).
			Set("description", rowStr(row, "description", "")).
			Set("sort_order", rowInt(row, "sort_order", 0)).
			Set("weight", weight).
			Set("is_active", rowInt(row, "is_active", 0)).
			Set("total", total).
			Set("used", used).
			Set("left", total-used).
			Set("created_at", rowStr(row, "created_at", "")))
	}
	c.JSON(phpjson.New().Set("list", list), 0, "ok", 0)
}

// ---------- admin_lottery_prize_save (api.php:1849) ----------

func (rt *Router) handleAdminLotteryPrizeSave(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_prize_save")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	id := c.paramSetInt("id", 0)
	name := phpTrim(c.paramSetStr("name", ""))
	ctype := phpTrim(c.paramSetStr("card_type", "通用"))
	desc := phpTrim(c.paramSetStr("description", ""))
	sortOrder := c.paramSetInt("sort_order", 0)
	act := int64(0)
	if c.paramSetInt("is_active", 1) == 1 {
		act = 1
	}
	weight := c.paramSetInt("weight", 100)

	if weight < 0 {
		weight = 0
	}
	if weight > 1000 {
		c.Error("奖项权重不能超过 1000", 1)
		return
	}
	if name == "" {
		c.Error("奖项名称不能为空", 1)
		return
	}
	if mbStrlen(name) > 50 {
		c.Error("奖项名称不能超过 50 个字", 1)
		return
	}
	if ctype == "" {
		ctype = "通用"
	}
	if mbStrlen(ctype) > 20 {
		c.Error("卡密类型不能超过 20 个字", 1)
		return
	}
	if desc != "" && mbStrlen(desc) > 255 {
		c.Error("描述不能超过 255 个字", 1)
		return
	}
	dupV, _, err := s.db().QueryValue(c.R.Context(), "SELECT id FROM lottery_prizes WHERE name = ?", name)
	if err != nil {
		c.dbError(err)
		return
	}
	dup := phpInt(dupV)
	if dup > 0 && dup != id {
		c.Error("奖项名称已存在", 1)
		return
	}
	if id > 0 {
		if _, err := s.db().Exec(c.R.Context(),
			"UPDATE lottery_prizes SET name = ?, card_type = ?, description = ?, sort_order = ?, is_active = ?, weight = ? WHERE id = ?",
			name, ctype, desc, sortOrder, act, weight, id); err != nil {
			c.dbError(err)
			return
		}
		c.JSON(phpjson.New().Set("id", id).Set("weight", weight), 0, "ok", 0)
		return
	}
	res, err := s.db().Exec(c.R.Context(),
		"INSERT INTO lottery_prizes (name, card_type, description, sort_order, is_active, weight) VALUES (?, ?, ?, ?, ?, ?)",
		name, ctype, desc, sortOrder, act, weight)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("id", res.LastInsertID), 0, "ok", 0)
}

// ---------- admin_lottery_codes (api.php:1892) ----------

func (rt *Router) handleAdminLotteryCodes(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_codes")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 100 {
		ps = 100
	}
	pid := c.paramSetInt("prize_id", 0)
	status := c.paramSetStr("status", "all")
	kw := phpTrim(c.paramSetStr("keyword", ""))

	where := []string{}
	args := []any{}
	if pid > 0 {
		where = append(where, "c.prize_id = ?")
		args = append(args, pid)
	}
	// status 白名单只有 unused / used；其它（含空串、'all'）都不过滤（契约 §9.15）。
	if status == "unused" {
		where = append(where, "c.user_id = 0")
	} else if status == "used" {
		where = append(where, "c.user_id > 0")
	}
	if kw != "" {
		where = append(where, "c.code LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}

	totalV, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_codes c "+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	rows, err := s.db().QueryAll(c.R.Context(), `SELECT c.*, p.name AS prize_name, p.card_type, u.nickname, u.username
        FROM lottery_codes c
        LEFT JOIN lottery_prizes p ON p.id = c.prize_id
        LEFT JOIN users u ON u.id = c.user_id
        `+cond+" ORDER BY c.id DESC LIMIT "+phpNum(ps)+" OFFSET "+phpNum((page-1)*ps), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		uid := rowInt(row, "user_id", 0)
		nn := rowStr(row, "nickname", "")
		userNick := ""
		if uid > 0 {
			if nn != "" {
				userNick = nn
			} else {
				userNick = rowStr(row, "username", "")
			}
		}
		statusText := "unused"
		if uid > 0 {
			statusText = "used"
		}
		list = append(list, phpjson.New().
			Set("id", rowInt(row, "id", 0)).
			Set("prize_id", rowInt(row, "prize_id", 0)).
			Set("prize_name", rowStr(row, "prize_name", "")).
			Set("card_type", rowStr(row, "card_type", "")).
			Set("code", rowStr(row, "code", "")).
			Set("status", statusText).
			Set("user_id", uid).
			Set("user_nickname", userNick).
			Set("used_at", rowStr(row, "used_at", "")).
			Set("created_at", rowStr(row, "created_at", "")))
	}
	// 这两个是**全表**计数，不受筛选影响（api.php:1927-1928）。
	unusedV, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_codes WHERE user_id = 0")
	if err != nil {
		c.dbError(err)
		return
	}
	usedV, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_codes WHERE user_id > 0")
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", phpInt(totalV)).
		Set("page", page).
		Set("page_size", ps).
		Set("unused", phpInt(unusedV)).
		Set("used", phpInt(usedV)), 0, "ok", 0)
}

// ---------- admin_lottery_config_get (api.php:1981) ----------

func (rt *Router) handleAdminLotteryConfigGet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_config_get")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	c.JSON(s.cfg().JSON(), 0, "ok", 0)
}

// ---------- admin_lottery_config_set (api.php:1985) ----------

func (rt *Router) handleAdminLotteryConfigSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_config_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	old := s.cfg()
	keys := append([]string(nil), old.keys...)
	vals := map[string]any{}
	for k, v := range old.vals {
		vals[k] = v
	}

	// 校验顺序逐字照抄 api.php:1991-2029（顺序即用户看到的第一个错）。
	title := phpTrim(c.paramSetStr("title", old.Str("title")))
	if mbStrlen(title) > 50 {
		c.Error("抽奖标题不能超过 50 个字", 1)
		return
	}
	content := c.paramSetStr("content", old.Str("content"))
	if mbStrlen(content) > 2000 {
		c.Error("抽奖内容不能超过 2000 个字", 1)
		return
	}
	limit := c.paramSetInt("per_user_limit", old.Int("per_user_limit"))
	if limit < 0 {
		limit = 0
	}
	if limit > 9999 {
		c.Error("每人抽奖次数不能超过 9999", 1)
		return
	}
	daily := c.paramSetInt("daily_limit", old.Int("daily_limit"))
	if daily < 0 {
		daily = 0
	}
	if daily > 999 {
		c.Error("每日抽奖次数不能超过 999", 1)
		return
	}
	week := c.paramSetInt("week_limit", old.Int("week_limit"))
	if week < 0 {
		week = 0
	}
	if week > 999 {
		c.Error("每周抽奖次数不能超过 999", 1)
		return
	}
	cool := c.paramSetInt("cooldown_seconds", old.Int("cooldown_seconds"))
	if cool < 0 {
		cool = 0
	}
	if cool > 86400 {
		c.Error("冷却时间不能超过 86400 秒", 1)
		return
	}
	dtl := c.paramSetInt("daily_total_limit", old.Int("daily_total_limit"))
	if dtl < 0 {
		dtl = 0
	}
	if dtl > 999999 {
		c.Error("今天发放上限不能超过 999999", 1)
		return
	}

	vals["enabled"] = lotteryFlag(c.paramSetAny("enabled", old.Int("enabled")))
	vals["title"] = title
	vals["content"] = content
	vals["per_user_limit"] = limit
	vals["daily_limit"] = daily
	vals["week_limit"] = week
	vals["cooldown_seconds"] = cool
	vals["daily_total_limit"] = dtl

	resetTime, ok := s.cfgValidateTime(c.paramSetAny("daily_reset_time", old.Str("daily_reset_time")))
	if !ok {
		return
	}
	vals["daily_reset_time"] = resetTime

	successText, ok := s.cfgValidateText("提示文案", c.paramSetAny("success_text", old.Str("success_text")), 200)
	if !ok {
		return
	}
	vals["success_text"] = successText
	emptyText, ok := s.cfgValidateText("提示文案", c.paramSetAny("empty_text", old.Str("empty_text")), 200)
	if !ok {
		return
	}
	vals["empty_text"] = emptyText

	vals["show_prizes"] = lotteryFlag(c.paramSetAny("show_prizes", old.Int("show_prizes")))
	vals["show_stock"] = lotteryFlag(c.paramSetAny("show_stock", old.Int("show_stock")))
	vals["notify_winner"] = lotteryFlag(c.paramSetAny("notify_winner", old.Int("notify_winner")))
	// windows_enabled=0 只让时段失效，不动 windows 配置（后台关开关后仍会原样提交 windows）。
	vals["windows_enabled"] = lotteryFlag(c.paramSetAny("windows_enabled", old.Int("windows_enabled")))

	startDate, ok := s.cfgValidateDate("活动开始日期", c.paramSetAny("start_date", old.Str("start_date")))
	if !ok {
		return
	}
	endDate, ok := s.cfgValidateDate("活动结束日期", c.paramSetAny("end_date", old.Str("end_date")))
	if !ok {
		return
	}
	if startDate != "" && endDate != "" && endDate < startDate {
		c.Error("结束日期不能早于开始日期", 1)
		return
	}
	vals["start_date"] = startDate
	vals["end_date"] = endDate

	windows, ok := s.cfgValidateWindows(c.paramSetAny("windows", old.vals["windows"]))
	if !ok {
		return
	}
	vals["windows"] = windows

	// lottery_config_save($cfg)：整份 JSON 覆写（含 $old 里残留的未知键）。
	saved := phpjson.New()
	for _, k := range keys {
		saved.Set(k, vals[k])
	}
	if _, err := s.db().Exec(c.R.Context(),
		"INSERT INTO `settings` (`key`, `value`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)",
		"lottery_config", string(phpjson.Marshal(saved))); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(s.cfg().JSON(), 0, "ok", 0)
}

// ---------- admin_lottery_window_preview (api.php:2033) ----------

func (rt *Router) handleAdminLotteryWindowPreview(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_window_preview")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	pcfg := s.cfg()
	pwin := s.windowState(pcfg, s.nowTS())
	// 比 lottery_info.window 多两个键，追加在末尾（别复用同一个 DTO，契约 §9.12）。
	pwin.Put("daily_total_left", s.dailyTotalLeft(pcfg))
	pwin.Put("daily_total_limit", pcfg.Int("daily_total_limit"))
	c.JSON(pwin, 0, "ok", 0)
}

// ---------- admin_lottery_draws (api.php:2099) ----------

func (rt *Router) handleAdminLotteryDraws(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_draws")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 100 {
		ps = 100
	}
	pid := c.paramSetInt("prize_id", 0)
	kw := phpTrim(c.paramSetStr("keyword", ""))

	where := []string{}
	args := []any{}
	if pid > 0 {
		where = append(where, "d.prize_id = ?")
		args = append(args, pid)
	}
	if kw != "" {
		where = append(where, "(d.code LIKE ? OR u.username LIKE ? OR u.nickname LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like)
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}

	totalV, _, err := s.db().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM lottery_draws d LEFT JOIN users u ON u.id = d.user_id "+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	rows, err := s.db().QueryAll(c.R.Context(), `SELECT d.*, u.nickname, u.username, p.name AS prize_name, p.card_type
        FROM lottery_draws d LEFT JOIN users u ON u.id = d.user_id
        LEFT JOIN lottery_prizes p ON p.id = d.prize_id
        `+cond+" ORDER BY d.id DESC LIMIT "+phpNum(ps)+" OFFSET "+phpNum((page-1)*ps), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		nn := rowStr(row, "nickname", "")
		nickname := nn
		if nickname == "" {
			nickname = rowStr(row, "username", "")
		}
		list = append(list, phpjson.New().
			Set("id", rowInt(row, "id", 0)).
			Set("user_id", rowInt(row, "user_id", 0)).
			Set("nickname", nickname).
			Set("username", rowStr(row, "username", "")).
			Set("prize_name", rowStr(row, "prize_name", "")).
			Set("card_type", rowStr(row, "card_type", "")).
			Set("code", rowStr(row, "code", "")).
			Set("created_at", rowStr(row, "created_at", "")))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", phpInt(totalV)).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- admin_lottery_quota_set (api.php:2134) ----------

func (rt *Router) handleAdminLotteryQuotaSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_quota_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	uid := c.paramSetInt("user_id", 0)
	quota := c.paramSetInt("quota", -1)
	if uid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if quota < -1 {
		quota = -1
	}
	if quota > 9999 {
		c.Error("抽奖次数不能超过 9999", 1)
		return
	}
	u, err := s.db().QueryRow(c.R.Context(), "SELECT * FROM users WHERE id = ?", uid)
	if err != nil {
		c.dbError(err)
		return
	}
	if u == nil {
		c.Error("用户不存在", 1)
		return
	}
	if _, err := s.db().Exec(c.R.Context(), "UPDATE users SET lottery_quota = ? WHERE id = ?", quota, uid); err != nil {
		c.dbError(err)
		return
	}
	u["lottery_quota"] = quota // left 用「已改过内存值」的 $u 现算
	left, err := s.leftFor(u)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("user_id", uid).Set("quota", quota).Set("left", left), 0, "ok", 0)
}

// ---------- admin_lottery_quota_reset (api.php:2150) ----------

func (rt *Router) handleAdminLotteryQuotaReset(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_lottery_quota_reset")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := newLotteryScope(rt, c)
	uid := c.paramSetInt("user_id", 0)
	if uid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	u, err := s.db().QueryRow(c.R.Context(), "SELECT * FROM users WHERE id = ?", uid)
	if err != nil {
		c.dbError(err)
		return
	}
	if u == nil {
		c.Error("用户不存在", 1)
		return
	}
	daily := c.paramSetInt("daily", 0) == 1
	// 统一用数据库的 NOW() 作为重置标记，避免 PHP 与 MySQL 时区/秒差不一致。
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
	sql += " WHERE id = ?"
	args = append(args, uid)
	if _, err := s.db().Exec(c.R.Context(), sql, args...); err != nil {
		c.dbError(err)
		return
	}
	u["lottery_reset_at"] = nowDb
	if daily {
		u["lottery_day_reset_at"] = nowDb
	}
	drawn, err := s.drawnCount(uid)
	if err != nil {
		c.dbError(err)
		return
	}
	todayDrawn, err := s.drawnCountToday(uid)
	if err != nil {
		c.dbError(err)
		return
	}
	left, err := s.leftFor(u)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("user_id", uid).
		Set("drawn", drawn).
		Set("today_drawn", todayDrawn).
		Set("left", left), 0, "ok", 0)
}

// ---------- admin_lottery_quota_reset_all (api.php:2173) ----------

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
	dailyFlag := int64(0)
	if daily {
		dailyFlag = 1
	}
	c.JSON(phpjson.New().Set("updated", res.RowsAffected).Set("daily", dailyFlag), 0, "ok", 0)
}

// ---------- admin_lottery_quota_all (api.php:2220) ----------

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
