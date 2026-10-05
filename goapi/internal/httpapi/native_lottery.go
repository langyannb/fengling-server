package httpapi

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5A：抽奖（客户端 3 个 action + 后台 14 个）的原生实现，逐字对齐 api.php:1431-1560
// 与 api.php:1830-2227，共用 api.php:4385-4885 的辅助函数。
//
// 改这里之前先读 api.php 原文（顺序错了用户看到的第一条错误就变了）：
//   - lottery_draw 的九步顺序是硬约束（api.php:1496 注释明写）；
//   - `-1` = 不限、`0` = 用完了，两者**不能混**（daily_total_left / daily_left_for /
//     weekly_left_for 用 `=== 0` 判定，left_for 用 `<= 0`）；
//   - weight <= 0 当 100（不是「不参与」）；周计数误用 users.lottery_day_reset_at
//     是现网行为，照抄不许修（契约 §9.6）；
//   - 抽奖事务内 UPDATE lottery_codes 的 rowCount **不检查**（契约 §4.4）；
//   - 所有「现在」走数据库时钟 `SELECT UNIX_TIMESTAMP(NOW())`，不是进程时钟。
//
// ⛔ 本文件**刻意不实现**契约 §9.1 标为破坏性的 4 个 action：
//   admin_lottery_activity_reset / admin_lottery_codes_delete /
//   admin_lottery_prize_delete / admin_lottery_codes_import
// 它们继续由 router.go 的 default 分支 FastCGI 透传给 PHP。

// ---------- 事务原语 ----------

// lotteryQ 是抽奖用到的数据访问原语：事务外由 rt.accounts() 提供，事务内由 lotteryTx 提供。
type lotteryQ interface {
	QueryRow(ctx context.Context, query string, args ...any) (store.Row, error)
	QueryAll(ctx context.Context, query string, args ...any) ([]store.Row, error)
	QueryValue(ctx context.Context, query string, args ...any) (any, bool, error)
	Exec(ctx context.Context, query string, args ...any) (store.ExecResult, error)
}

// lotteryTx 是抽奖事务（额外带 Commit / Rollback）。
type lotteryTx interface {
	lotteryQ
	Commit() error
	Rollback() error
}

// lotteryTxStarter 由单测的脚本化假 DB 实现；生产走 *store.Store（见 beginLotteryTx）。
type lotteryTxStarter interface {
	BeginLotteryTx(ctx context.Context) (lotteryTx, error)
}

// beginLotteryTx 取一个显式事务。抽奖必须用真事务：database/sql 的单条查询是隐式
// autocommit，`SELECT ... FOR UPDATE` 会退化成普通读（契约 §9.4）。
func (rt *Router) beginLotteryTx(c *Ctx) (lotteryTx, error) {
	if s, ok := rt.env.Accounts.(lotteryTxStarter); ok {
		return s.BeginLotteryTx(c.R.Context())
	}
	if rt.env.Store != nil {
		return rt.env.Store.BeginLotteryTx(c.R.Context())
	}
	// 兜底：万一调用方把 *store.Store 只塞进了 Accounts（没塞 Store）。
	if st, ok := rt.env.Accounts.(interface {
		BeginLotteryTx(context.Context) (*store.Tx, error)
	}); ok {
		return st.BeginLotteryTx(c.R.Context())
	}
	return nil, errAccountsUnavailable
}

// ---------- 作用域：把「数据库 + 请求上下文」捆在一起，供各 helper 使用 ----------

type lotteryScope struct {
	rt *Router
	c  *Ctx
	q  lotteryQ // 非空时用它（保留给事务内调用；当前事务内代码直接持有 tx）
}

func newLotteryScope(rt *Router, c *Ctx) *lotteryScope { return &lotteryScope{rt: rt, c: c} }

func (s *lotteryScope) db() lotteryQ {
	if s.q != nil {
		return s.q
	}
	return s.rt.accounts()
}

func (s *lotteryScope) ctx() context.Context { return s.c.R.Context() }

// ---------- PHP 标量/PHP 语义小工具 ----------

// lotteryFlag 对齐 api.php:4400 lottery_flag()。
// bool→1/0；int/float→(int)==1?1:0；字符串小写 trim 属于 {1,true,on,yes}→1，否则 0。
func lotteryFlag(v any) int64 {
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
			if i == 1 {
				return 1
			}
			return 0
		}
		if f, err := x.Float64(); err == nil {
			if int64(f) == 1 {
				return 1
			}
			return 0
		}
		return 0
	case float64:
		if int64(x) == 1 {
			return 1
		}
		return 0
	case float32:
		if int64(x) == 1 {
			return 1
		}
		return 0
	case int:
		if int64(x) == 1 {
			return 1
		}
		return 0
	case int64:
		if x == 1 {
			return 1
		}
		return 0
	}
	s := strings.ToLower(phpTrim(phpStr(v)))
	if s == "1" || s == "true" || s == "on" || s == "yes" {
		return 1
	}
	return 0
}

var lotteryHMRe = regexp.MustCompile(`^(\d{1,2}):(\d{1,2})$`)

// lotteryCfgHM 对齐 api.php:4409 lottery_cfg_hm()：返回 (时, 分, 是否合法)。
func lotteryCfgHM(v string) (int, int, bool) {
	m := lotteryHMRe.FindStringSubmatch(phpTrim(v))
	if m == nil {
		return 0, 0, false
	}
	h := int(atoiPrefix(m[1]))
	i := int(atoiPrefix(m[2]))
	if h >= 0 && h <= 23 && i >= 0 && i <= 59 {
		return h, i, true
	}
	return 0, 0, false
}

// lotteryHMText 对齐 sprintf('%02d:%02d', $h, $m)。
func lotteryHMText(h, m int) string { return fmt.Sprintf("%02d:%02d", h, m) }

// lotteryT 把时间戳按 PHP 进程时区（Asia/Shanghai）还原成日历时间。
// api.php:25 有 date_default_timezone_set('Asia/Shanghai')，mktime/date 全按它算。
func lotteryT(ts int64) time.Time { return time.Unix(ts, 0).In(config.LocalZone()) }

// lotteryWeekday 对齐 date('N')：1=周一 … 7=周日（Go 的 Sunday=0）。
func lotteryWeekday(ts int64) int {
	wd := int(lotteryT(ts).Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// cfgInt 复刻 PHP 的 (int) 强转（含 PHP「非空数组/对象算 1」的语义，配置值来自 DB JSON）。
func cfgInt(v any) int64 {
	if o, ok := v.(*phpjson.O); ok {
		if o.Len() > 0 {
			return 1
		}
		return 0
	}
	return intvalAny(v)
}

// ---------- 有序 JSON 解析（PHP json_decode($raw, true) 保序） ----------

// decodeOrderedJSON 把 JSON 解析成「对象用 *phpjson.O 保序、数组用 []any」的形态，
// 标量保留 string / json.Number / bool / nil。第二个返回值 = 是否解析成功。
func decodeOrderedJSON(raw string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	v, err := decodeOrderedValue(dec)
	if err != nil {
		return nil, false
	}
	return v, true
}

func decodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedFrom(dec, tok)
}

func decodeOrderedFrom(dec *json.Decoder, tok json.Token) (any, error) {
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			o := phpjson.New()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				v, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(key, v)
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return arr, nil
		}
	}
	return tok, nil
}

// ---------- 配置 ----------

// lotteryDefaultKeys 是 lottery_config() 的默认键顺序（api.php:4632-4651）。
// 这个顺序就是 admin_lottery_config_get 的 JSON 输出顺序（array_merge 保首数组顺序，
// DB 里的未知键追加在末尾）。
var lotteryDefaultKeys = []string{
	"enabled", "title", "content", "success_text", "empty_text", "per_user_limit",
	"daily_limit", "week_limit", "daily_reset_time", "cooldown_seconds", "daily_total_limit",
	"windows_enabled", "windows", "start_date", "end_date", "show_prizes", "show_stock",
	"notify_winner",
}

type lotteryCfg struct {
	keys []string
	vals map[string]any
}

func (c *lotteryCfg) Get(k string) (any, bool) {
	v, ok := c.vals[k]
	return v, ok
}

// Int 取整型配置（对齐 `(int)$cfg['x']`）。
func (c *lotteryCfg) Int(k string) int64 {
	v, ok := c.vals[k]
	if !ok || v == nil {
		return 0
	}
	return cfgInt(v)
}

// Str 取字符串配置（对齐 `(string)$cfg['x']`）。
func (c *lotteryCfg) Str(k string) string {
	v, ok := c.vals[k]
	if !ok || v == nil {
		return ""
	}
	return phpStr(v)
}

// windows 返回归一化后的时段数组（每项是 *phpjson.O{start,end,days}）。
func (c *lotteryCfg) windows() []any {
	v, ok := c.vals["windows"]
	if !ok {
		return nil
	}
	arr, _ := v.([]any)
	return arr
}

// JSON 按 PHP 数组顺序输出（admin_lottery_config_get / config_set 回读用）。
func (c *lotteryCfg) JSON() *phpjson.O {
	o := phpjson.New()
	for _, k := range c.keys {
		o.Set(k, c.vals[k])
	}
	return o
}

// cfg 对齐 api.php:4630 lottery_config()：读 settings.lottery_config 并归一化。
//
// 每次调用都**重新读库**（不是缓存）——PHP 的 helper 各自再读一次配置，同一请求内
// 若后台改了配置可能混用新旧值（契约 §9.5 要求照抄）。读库异常被吞掉，回落默认配置。
func (s *lotteryScope) cfg() *lotteryCfg {
	decoded, err := s.settingOrderedJSON("lottery_config")
	if err != nil {
		s.c.Log().Error("[lottery_config] 读配置失败, 按默认配置处理（PHP 此处 try/catch 吞掉）", "err", err)
		decoded = nil
	}
	vals := map[string]any{}
	keys := append([]string(nil), lotteryDefaultKeys...)
	defaults := []struct {
		k string
		v any
	}{
		{"enabled", int64(0)},
		{"title", "免费抽卡密"},
		{"content", ""},
		{"success_text", ""},
		{"empty_text", "奖品已抽完, 请稍后再来"},
		{"per_user_limit", int64(1)},
		{"daily_limit", int64(0)},
		{"week_limit", int64(0)},
		{"daily_reset_time", "00:00"},
		{"cooldown_seconds", int64(0)},
		{"daily_total_limit", int64(0)},
		{"windows_enabled", int64(0)},
		{"windows", []any{}},
		{"start_date", ""},
		{"end_date", ""},
		{"show_prizes", int64(1)},
		{"show_stock", int64(1)},
		{"notify_winner", int64(1)},
	}
	for _, d := range defaults {
		vals[d.k] = d.v
	}
	// PHP: array_merge($cfg, $j) —— 已知键原位覆盖，未知键追加末尾。
	if obj, ok := decoded.(*phpjson.O); ok {
		seen := map[string]bool{}
		for _, k := range lotteryDefaultKeys {
			seen[k] = true
		}
		for _, k := range obj.Keys() {
			v, _ := obj.Get(k)
			vals[k] = v
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	// 以下逐项归一化（api.php:4662-4703）。
	vals["enabled"] = lotteryFlag(vals["enabled"])
	title := phpStr(vals["title"])
	if title == "" {
		title = "免费抽卡密"
	}
	vals["title"] = title
	vals["content"] = phpStr(vals["content"])
	vals["success_text"] = phpStr(vals["success_text"])
	emptyText := phpStr(vals["empty_text"])
	if emptyText == "" {
		emptyText = "奖品已抽完, 请稍后再来"
	}
	vals["empty_text"] = emptyText
	clamp := func(v any, min, max int64) int64 {
		n := cfgInt(v)
		if n < min {
			n = min
		}
		if n > max {
			n = max
		}
		return n
	}
	vals["per_user_limit"] = clamp(vals["per_user_limit"], 0, 9999)
	vals["daily_limit"] = clamp(vals["daily_limit"], 0, 999)
	vals["week_limit"] = clamp(vals["week_limit"], 0, 999)
	vals["cooldown_seconds"] = clamp(vals["cooldown_seconds"], 0, 86400)
	vals["daily_total_limit"] = clamp(vals["daily_total_limit"], 0, 999999)

	rt := phpStr(vals["daily_reset_time"])
	h, m, okHM := lotteryCfgHM(rt)
	if !okHM {
		h, m = 0, 0
	}
	vals["daily_reset_time"] = lotteryHMText(h, m)
	vals["windows_enabled"] = lotteryFlag(vals["windows_enabled"])

	wins := []any{}
	if arr, isArr := vals["windows"].([]any); isArr {
		for _, w := range arr {
			wo, okW := w.(*phpjson.O)
			if !okW {
				continue
			}
			sv, _ := wo.Get("start")
			ev, _ := wo.Get("end")
			ah, am, okA := lotteryCfgHM(phpStr(sv))
			bh, bm, okB := lotteryCfgHM(phpStr(ev))
			if !okA || !okB {
				continue
			}
			if ah*60+am == bh*60+bm {
				continue // 归一化会丢掉开始=结束的段（api.php:4686）
			}
			days := []any{}
			if dv, has := wo.Get("days"); has {
				if da, isArrDays := dv.([]any); isArrDays {
					nums := []int64{}
					for _, d := range da {
						n := phpInt(d)
						if n >= 1 && n <= 7 {
							nums = append(nums, n)
						}
					}
					nums = lotteryUniqueSorted(nums)
					for _, n := range nums {
						days = append(days, n)
					}
				}
			}
			wins = append(wins, phpjson.New().
				Set("start", lotteryHMText(ah, am)).
				Set("end", lotteryHMText(bh, bm)).
				Set("days", days))
			if len(wins) >= 10 {
				break
			}
		}
	}
	vals["windows"] = wins

	vals["start_date"] = lotteryDateText(vals["start_date"])
	vals["end_date"] = lotteryDateText(vals["end_date"])
	vals["show_prizes"] = lotteryFlag(vals["show_prizes"])
	vals["show_stock"] = lotteryFlag(vals["show_stock"])
	vals["notify_winner"] = lotteryFlag(vals["notify_winner"])
	return &lotteryCfg{keys: keys, vals: vals}
}

var lotteryDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// lotteryDateText 对齐 api.php:4698 的 start_date/end_date 归一化：只做正则，不 checkdate。
func lotteryDateText(v any) string {
	s := phpStr(v)
	if lotteryDateRe.MatchString(s) {
		return s
	}
	return ""
}

// lotteryUniqueSorted 复刻 array_values(array_unique(...)) + sort($days)（升序数值）。
func lotteryUniqueSorted(in []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// settingOrderedJSON 读 settings 表某一行的 value 并保序解析。
// 对齐 `(string)($st->fetchColumn() ?: ”)`：PHP 的 `?:` 把 "0" 也当空。
func (s *lotteryScope) settingOrderedJSON(key string) (any, error) {
	v, found, err := s.db().QueryValue(s.ctx(), "SELECT `value` FROM `settings` WHERE `key` = ?", key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	raw := phpStr(v)
	if phpFalsy(raw) {
		return nil, nil
	}
	dec, ok := decodeOrderedJSON(raw)
	if !ok {
		return nil, nil
	}
	return dec, nil
}

// ---------- 时间 / 时段（api.php:4388-4574） ----------

// nowTS 对齐 api.php:4388 lottery_cfg_ts()：优先数据库时钟，失败回落进程时钟。
func (s *lotteryScope) nowTS() int64 {
	v, _, err := s.db().QueryValue(s.ctx(), "SELECT UNIX_TIMESTAMP(NOW())")
	if err != nil {
		s.c.Log().Error("[lottery_cfg_ts] 读数据库时钟失败, 回落进程时钟（PHP error_log 后 time()）", "err", err)
		return nowUnix()
	}
	if n := phpInt(v); n > 0 {
		return n
	}
	return nowUnix()
}

// lotteryDayWindow 对齐 api.php:4419 lottery_day_window()：按 daily_reset_time 切「今日」。
func lotteryDayWindow(cfg *lotteryCfg, ts int64) (int64, int64) {
	h, m := 0, 0
	if hh, mm, ok := lotteryCfgHM(cfg.Str("daily_reset_time")); ok {
		h, m = hh, mm
	}
	t := lotteryT(ts)
	b := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, config.LocalZone()).Unix()
	if b > ts {
		b = time.Date(t.Year(), t.Month(), t.Day()-1, h, m, 0, 0, config.LocalZone()).Unix()
	}
	return b, b + 86400
}

// lotteryWeekWindow 对齐 api.php:4432 lottery_week_window()：周一（按 daily_reset_time 时刻）起算。
func lotteryWeekWindow(cfg *lotteryCfg, ts int64) (int64, int64) {
	h, m := 0, 0
	if hh, mm, ok := lotteryCfgHM(cfg.Str("daily_reset_time")); ok {
		h, m = hh, mm
	}
	t := lotteryT(ts)
	monday := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, config.LocalZone()).Unix()
	if monday > ts {
		monday -= 86400
	}
	dow := lotteryWeekday(monday)
	start := monday - int64(dow-1)*86400
	return start, start + 7*86400
}

// lotteryRawDays 取某段的 days 原值（不做 1-7 过滤，对齐 window_ts_open 的 $has 闭包）。
func lotteryRawDays(wo *phpjson.O) []int64 {
	v, ok := wo.Get("days")
	if !ok {
		return nil
	}
	arr, isArr := v.([]any)
	if !isArr {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, d := range arr {
		out = append(out, phpInt(d))
	}
	return out
}

// lotteryWindowTSOpen 对齐 api.php:4445 lottery_window_ts_open()：某时刻是否命中开放时段。
func lotteryWindowTSOpen(cfg *lotteryCfg, ts int64) bool {
	if cfg.Int("windows_enabled") != 1 {
		return true
	}
	ws := cfg.windows()
	if len(ws) == 0 {
		return true
	}
	t := lotteryT(ts)
	cur := t.Hour()*60 + t.Minute()
	dow := lotteryWeekday(ts)
	prevDow := dow - 1
	if dow == 1 {
		prevDow = 7
	}
	for _, w := range ws {
		wo, ok := w.(*phpjson.O)
		if !ok {
			continue
		}
		sv, _ := wo.Get("start")
		ev, _ := wo.Get("end")
		ah, am, okA := lotteryCfgHM(phpStr(sv))
		bh, bm, okB := lotteryCfgHM(phpStr(ev))
		if !okA || !okB {
			continue
		}
		days := lotteryRawDays(wo)
		has := func(d int) bool {
			if len(days) == 0 {
				return true
			}
			for _, x := range days {
				if x == int64(d) {
					return true
				}
			}
			return false
		}
		s := ah*60 + am
		e := bh*60 + bm
		if s == e {
			if has(dow) {
				return true
			}
			continue
		}
		if s < e {
			if has(dow) && cur >= s && cur < e {
				return true
			}
		} else {
			if has(dow) && cur >= s { // 今天的段跨到明天凌晨
				return true
			}
			if has(prevDow) && cur < e { // 今天凌晨属于昨天开始的段
				return true
			}
		}
	}
	return false
}

// lotteryDateRangeState 对齐 api.php:4478：” / -1 可抽, 1 未开始, 2 已结束（字符串比较）。
func lotteryDateRangeState(cfg *lotteryCfg, ts int64) int {
	today := lotteryT(ts).Format("2006-01-02")
	sd := cfg.Str("start_date")
	ed := cfg.Str("end_date")
	if sd != "" && today < sd {
		return 1
	}
	if ed != "" && today > ed {
		return 2
	}
	return -1
}

// nextOpenSeconds 对齐 api.php:4489 lottery_next_open_seconds()：扫 8 天，-1 = 8 天内无开放。
func (s *lotteryScope) nextOpenSeconds(cfg *lotteryCfg, ts int64) int64 {
	if cfg.Int("windows_enabled") != 1 {
		return 0
	}
	if len(cfg.windows()) == 0 {
		return 0
	}
	if lotteryWindowTSOpen(cfg, ts) {
		return 0
	}
	base := ts - (ts % 60)
	for k := int64(1); k <= 11520; k++ {
		if lotteryWindowTSOpen(cfg, base+k*60) {
			return base + k*60 - ts
		}
	}
	return -1
}

// lotteryWindowsText 对齐 api.php:4504 lottery_windows_text()。
func lotteryWindowsText(cfg *lotteryCfg) string {
	if cfg.Int("windows_enabled") != 1 {
		return ""
	}
	ws := cfg.windows()
	if len(ws) == 0 {
		return ""
	}
	names := map[int64]string{1: "周一", 2: "周二", 3: "周三", 4: "周四", 5: "周五", 6: "周六", 7: "周日"}
	out := []string{}
	for _, w := range ws {
		wo, ok := w.(*phpjson.O)
		if !ok {
			continue
		}
		sv, _ := wo.Get("start")
		ev, _ := wo.Get("end")
		ah, am, okA := lotteryCfgHM(phpStr(sv))
		bh, bm, okB := lotteryCfgHM(phpStr(ev))
		if !okA || !okB {
			continue
		}
		nums := lotteryUniqueSorted(lotteryRawDays(wo))
		ds := []string{}
		for _, d := range nums {
			if n, ok := names[d]; ok {
				ds = append(ds, n)
			}
		}
		prefix := "每天"
		if len(ds) > 0 {
			prefix = strings.Join(ds, "、")
		}
		out = append(out, prefix+" "+lotteryHMText(ah, am)+"-"+lotteryHMText(bh, bm))
	}
	return strings.Join(out, "; ")
}

// windowState 对齐 api.php:4527 lottery_window_state()（10 键，顺序固定）。
func (s *lotteryScope) windowState(cfg *lotteryCfg, now int64) *phpjson.O {
	res := phpjson.New().
		Set("open", false).
		Set("reason", "open").
		Set("reason_text", "").
		Set("text", "").
		Set("next_open_at", "").
		Set("next_close_at", "").
		Set("seconds_to_open", int64(0)).
		Set("seconds_to_close", int64(0)).
		Set("server_time", phpDateTimeAt(now)).
		Set("my_allowed", true)
	if cfg.Int("enabled") != 1 {
		res.Put("reason", "disabled").Put("reason_text", "抽奖活动已关闭").Put("my_allowed", false)
		return res
	}
	switch lotteryDateRangeState(cfg, now) {
	case 1:
		res.Put("reason", "before_start").Put("reason_text", "抽奖活动还没开始").Put("my_allowed", false)
		return res
	case 2:
		res.Put("reason", "after_end").Put("reason_text", "抽奖活动已经结束").Put("my_allowed", false)
		return res
	}
	if cfg.Int("windows_enabled") != 1 || len(cfg.windows()) == 0 {
		res.Put("open", true).Put("reason", "open").Put("reason_text", "").Put("text", "")
		return res
	}
	res.Put("text", lotteryWindowsText(cfg))
	if !lotteryWindowTSOpen(cfg, now) {
		left := s.nextOpenSeconds(cfg, now)
		if left > 0 {
			res.Put("seconds_to_open", left)
			res.Put("next_open_at", phpDateTimeAt(now+left))
		}
		res.Put("reason", "outside_window").Put("reason_text", "现在不在抽奖时间内").Put("my_allowed", false)
		return res
	}
	res.Put("open", true).Put("reason", "open").Put("reason_text", "")
	k := int64(1)
	for k <= 11520 && lotteryWindowTSOpen(cfg, now+k*60) {
		k++
	}
	if k <= 11520 {
		stc := k*60 - (now % 60)
		res.Put("seconds_to_close", stc)
		res.Put("next_close_at", phpDateTimeAt(now+stc))
	}
	return res
}

// ---------- 计数 / 冷却（api.php:4577-4627、4783-4828） ----------

// cooldownLeft 对齐 api.php:4577 lottery_cooldown_left()：查询异常按「可以抽」处理。
func (s *lotteryScope) cooldownLeft(me store.Row) int64 {
	cd := s.cfg().Int("cooldown_seconds")
	if cd <= 0 {
		return 0
	}
	v, _, err := s.db().QueryValue(s.ctx(),
		"SELECT UNIX_TIMESTAMP(MAX(created_at)) FROM lottery_draws WHERE user_id = ?", rowInt(me, "id", 0))
	if err != nil {
		s.c.Log().Error("[lottery_cooldown_left] 查询上次抽奖时间失败, 按可抽处理（PHP error_log 后 return 0）", "err", err)
		return 0
	}
	last := phpInt(v)
	if last <= 0 {
		return 0
	}
	left := last + cd - s.nowTS()
	if left > 0 {
		return left
	}
	return 0
}

// dailyTotalLeft 对齐 api.php:4593 lottery_daily_total_left()：-1 = 不限；查询异常放宽到上限。
func (s *lotteryScope) dailyTotalLeft(cfg *lotteryCfg) int64 {
	lim := cfg.Int("daily_total_limit")
	if lim <= 0 {
		return -1
	}
	ws, we := lotteryDayWindow(cfg, s.nowTS())
	v, _, err := s.db().QueryValue(s.ctx(),
		"SELECT COUNT(*) FROM lottery_draws WHERE created_at >= ? AND created_at < ?",
		phpDateTimeAt(ws), phpDateTimeAt(we))
	if err != nil {
		s.c.Log().Error("[lottery_daily_total_left] 统计今日全站发放失败, 放宽到上限（PHP error_log 后 return $lim）", "err", err)
		return lim
	}
	left := lim - phpInt(v)
	if left > 0 {
		return left
	}
	return 0
}

// drawnCount 对齐 api.php:4783 lottery_drawn_count()（严格 `>`；无 catch，异常上抛成 500）。
func (s *lotteryScope) drawnCount(uid int64) (int64, error) {
	v, _, err := s.db().QueryValue(s.ctx(), `SELECT COUNT(*) FROM lottery_draws d
            LEFT JOIN users u ON u.id = d.user_id
            WHERE d.user_id = ? AND (u.lottery_reset_at IS NULL OR d.created_at > u.lottery_reset_at)`, uid)
	if err != nil {
		return 0, err
	}
	return phpInt(v), nil
}

// drawnCountToday 对齐 api.php:4793 lottery_drawn_count_today()。
func (s *lotteryScope) drawnCountToday(uid int64) (int64, error) {
	cfg := s.cfg()
	ws, we := lotteryDayWindow(cfg, s.nowTS())
	v, _, err := s.db().QueryValue(s.ctx(), `SELECT COUNT(*) FROM lottery_draws d
            LEFT JOIN users u ON u.id = d.user_id
            WHERE d.user_id = ? AND d.created_at >= ? AND d.created_at < ?
              AND (u.lottery_day_reset_at IS NULL OR d.created_at > u.lottery_day_reset_at)`,
		uid, phpDateTimeAt(ws), phpDateTimeAt(we))
	if err != nil {
		return 0, err
	}
	return phpInt(v), nil
}

// drawnCountWeek 对齐 api.php:4609 lottery_drawn_count_week()。
// 注意这里用的是 **u.lottery_day_reset_at**（不是 lottery_reset_at）—— 疑似 bug 但照抄（契约 §9.6）。
func (s *lotteryScope) drawnCountWeek(uid int64) (int64, error) {
	cfg := s.cfg()
	ws, we := lotteryWeekWindow(cfg, s.nowTS())
	v, _, err := s.db().QueryValue(s.ctx(), `SELECT COUNT(*) FROM lottery_draws d
            LEFT JOIN users u ON u.id = d.user_id
            WHERE d.user_id = ? AND d.created_at >= ? AND d.created_at < ?
              AND (u.lottery_day_reset_at IS NULL OR d.created_at > u.lottery_day_reset_at)`,
		uid, phpDateTimeAt(ws), phpDateTimeAt(we))
	if err != nil {
		return 0, err
	}
	return phpInt(v), nil
}

// dailyLeftFor 对齐 api.php:4806 lottery_daily_left_for()：-1 = 不限。
func (s *lotteryScope) dailyLeftFor(user store.Row) (int64, error) {
	daily := s.cfg().Int("daily_limit")
	if daily <= 0 {
		return -1, nil
	}
	drawn, err := s.drawnCountToday(rowInt(user, "id", 0))
	if err != nil {
		return 0, err
	}
	left := daily - drawn
	if left < 0 {
		left = 0
	}
	return left, nil
}

// weeklyLeftFor 对齐 api.php:4622 lottery_weekly_left_for()：-1 = 不限。
func (s *lotteryScope) weeklyLeftFor(user store.Row) (int64, error) {
	wk := s.cfg().Int("week_limit")
	if wk <= 0 {
		return -1, nil
	}
	drawn, err := s.drawnCountWeek(rowInt(user, "id", 0))
	if err != nil {
		return 0, err
	}
	left := wk - drawn
	if left < 0 {
		left = 0
	}
	return left, nil
}

// leftFor 对齐 api.php:4818 lottery_left_for()：恒 >= 0；quota<0 时跟随活动 per_user_limit。
func (s *lotteryScope) leftFor(user store.Row) (int64, error) {
	// PHP: array_key_exists('lottery_quota', $user) ? (int)$user['lottery_quota'] : -1
	// （键存在但为 NULL 时 (int)null = 0，不是 -1）。
	quota := int64(-1)
	if v, has := user.Get("lottery_quota"); has {
		quota = phpInt(v)
	}
	if quota < 0 {
		quota = s.cfg().Int("per_user_limit")
	}
	drawn, err := s.drawnCount(rowInt(user, "id", 0))
	if err != nil {
		return 0, err
	}
	left := quota - drawn
	if left < 0 {
		left = 0
	}
	dailyLeft, err := s.dailyLeftFor(user)
	if err != nil {
		return 0, err
	}
	if dailyLeft >= 0 && dailyLeft < left {
		left = dailyLeft
	}
	weekLeft, err := s.weeklyLeftFor(user)
	if err != nil {
		return 0, err
	}
	if weekLeft >= 0 && weekLeft < left {
		left = weekLeft
	}
	return left, nil
}

// ---------- 抽奖事务（api.php:4834 lottery_draw_once） ----------

type lotteryDrawResult struct {
	id        int64
	prizeID   int64
	prizeName string
	cardType  string
	code      string
}

// lotteryRandInt 复刻 PHP random_int($min, $max)：CSPRNG、闭区间、均匀整数。
// 不用 math/rand 默认源（契约 §4.2）。
func lotteryRandInt(lo, hi int64) (int64, error) {
	if hi < lo {
		return 0, fmt.Errorf("random_int(): Argument #2 ($max) must be greater than or equal to argument #1 ($min)")
	}
	n, err := crand.Int(crand.Reader, big.NewInt(hi-lo+1))
	if err != nil {
		return 0, err
	}
	return n.Int64() + lo, nil
}

// drawOnce 对齐 api.php:4834 lottery_draw_once()：全程一个事务。
// 失败时已经自己写过响应（rollback + empty_text 或 500），第二个返回值为 false。
func (s *lotteryScope) drawOnce(uid int64) (lotteryDrawResult, bool) {
	tx, err := s.rt.beginLotteryTx(s.c)
	if err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	poolRows, err := tx.QueryAll(s.ctx(), `SELECT p.id,p.name,p.card_type,p.weight,
            (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id AND c.user_id = 0) AS stock
        FROM lottery_prizes p WHERE p.is_active = 1`)
	if err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	type poolItem struct {
		id int64
		w  int64
	}
	pool := []poolItem{}
	var total int64
	for _, p := range poolRows {
		if rowInt(p, "stock", 0) <= 0 {
			continue // 没库存的奖项不进池
		}
		w := rowInt(p, "weight", 0)
		if w <= 0 {
			w = 100 // 权重 <=0 当 100（照抄，不是「不参与」）
		}
		pool = append(pool, poolItem{id: rowInt(p, "id", 0), w: w})
		total += w
	}
	if total <= 0 {
		_ = tx.Rollback()
		s.c.Error(s.cfg().Str("empty_text"), 1)
		return lotteryDrawResult{}, false
	}
	r, err := lotteryRandInt(1, total)
	if err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	pick := pool[0]
	for _, p := range pool {
		r -= p.w
		if r <= 0 {
			pick = p
			break
		}
	}
	row, err := tx.QueryRow(s.ctx(), `SELECT c.id AS code_id, c.code, p.id AS prize_id, p.name AS prize_name, p.card_type
            FROM lottery_codes c INNER JOIN lottery_prizes p ON p.id = c.prize_id
            WHERE c.user_id = 0 AND p.id = ?
            ORDER BY RAND() LIMIT 1 FOR UPDATE`, pick.id)
	if err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	if row == nil {
		_ = tx.Rollback()
		s.c.Error(s.cfg().Str("empty_text"), 1)
		return lotteryDrawResult{}, false
	}
	codeID := rowInt(row, "code_id", 0)
	// 刻意不检查 rowCount（PHP api.php:4867 也没检查，契约 §4.4）。
	if _, err := tx.Exec(s.ctx(),
		"UPDATE lottery_codes SET user_id = ?, used_at = NOW() WHERE id = ? AND user_id = 0", uid, codeID); err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	res, err := tx.Exec(s.ctx(),
		"INSERT INTO lottery_draws (user_id, prize_id, code_id, code, created_at) VALUES (?, ?, ?, ?, NOW())",
		uid, rowInt(row, "prize_id", 0), codeID, rowStr(row, "code", ""))
	if err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	if err := tx.Commit(); err != nil {
		s.c.dbError(err)
		return lotteryDrawResult{}, false
	}
	committed = true
	return lotteryDrawResult{
		id:        res.LastInsertID,
		prizeID:   rowInt(row, "prize_id", 0),
		prizeName: rowStr(row, "prize_name", ""),
		cardType:  rowStr(row, "card_type", ""),
		code:      rowStr(row, "code", ""),
	}, true
}

// ---------- 记录条目（lottery_info.records 与 lottery_records.list 共用） ----------

func lotteryRecordItem(r store.Row) *phpjson.O {
	return phpjson.New().
		Set("id", rowInt(r, "id", 0)).
		Set("prize_id", rowInt(r, "prize_id", 0)).
		Set("prize_name", rowStr(r, "prize_name", "")).
		Set("card_type", rowStr(r, "card_type", "")).
		Set("code", rowStr(r, "code", "")).
		Set("created_at", rowStr(r, "created_at", ""))
}

// ---------- lottery_info (api.php:1431) ----------

func (rt *Router) handleLotteryInfo(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "lottery_info")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	s := newLotteryScope(rt, c)
	cfg := s.cfg()
	meID := rowInt(me, "id", 0)

	prizeRows, err := s.db().QueryAll(c.R.Context(), `SELECT p.id, p.name, p.card_type, p.weight,
            (SELECT COUNT(*) FROM lottery_codes c WHERE c.prize_id = p.id AND c.user_id = 0) AS left_cnt
        FROM lottery_prizes p WHERE p.is_active = 1 ORDER BY p.sort_order ASC, p.id ASC`)
	if err != nil {
		c.dbError(err)
		return
	}
	prizes := []any{}
	for _, p := range prizeRows {
		leftCnt := rowInt(p, "left_cnt", 0)
		if leftCnt <= 0 {
			continue
		}
		weight := rowInt(p, "weight", 0)
		if weight <= 0 {
			weight = 100
		}
		prizes = append(prizes, phpjson.New().
			Set("id", rowInt(p, "id", 0)).
			Set("name", rowStr(p, "name", "")).
			Set("card_type", rowStr(p, "card_type", "")).
			Set("left", leftCnt).
			Set("weight", weight))
	}
	if cfg.Int("show_prizes") != 1 {
		prizes = []any{}
	}

	recRows, err := s.db().QueryAll(c.R.Context(), `SELECT d.id, d.prize_id, d.code, d.created_at, p.name AS prize_name, p.card_type
        FROM lottery_draws d LEFT JOIN lottery_prizes p ON p.id = d.prize_id
        WHERE d.user_id = ? ORDER BY d.id DESC LIMIT 20`, meID)
	if err != nil {
		c.dbError(err)
		return
	}
	records := []any{}
	for _, row := range recRows {
		records = append(records, lotteryRecordItem(row))
	}

	// 求值顺序照抄 PHP 的数组字面量（顺序错了，第一处 DB 异常就会变成不同的响应）。
	myQuota, err := s.leftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	myDrawn, err := s.drawnCount(meID)
	if err != nil {
		c.dbError(err)
		return
	}
	myTodayDrawn, err := s.drawnCountToday(meID)
	if err != nil {
		c.dbError(err)
		return
	}
	myTodayLeft, err := s.dailyLeftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	serverTime := phpDateTimeAt(s.nowTS())
	window := s.windowState(cfg, s.nowTS())
	myWeekDrawn, err := s.drawnCountWeek(meID)
	if err != nil {
		c.dbError(err)
		return
	}
	myWeekLeft, err := s.weeklyLeftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	cooldownLeft := s.cooldownLeft(me)
	dailyTotalLeft := s.dailyTotalLeft(cfg)
	windows := []any{}
	if ws, isArr := cfg.vals["windows"].([]any); isArr {
		windows = ws
	}

	c.JSON(phpjson.New().
		Set("enabled", cfg.Int("enabled")).
		Set("title", cfg.Str("title")).
		Set("content", cfg.Str("content")).
		Set("per_user_limit", cfg.Int("per_user_limit")).
		Set("my_quota", myQuota).
		Set("my_drawn", myDrawn).
		Set("daily_limit", cfg.Int("daily_limit")).
		Set("my_today_drawn", myTodayDrawn).
		Set("my_today_left", myTodayLeft).
		Set("server_time", serverTime).
		Set("window", window).
		Set("daily_reset_time", cfg.Str("daily_reset_time")).
		Set("week_limit", cfg.Int("week_limit")).
		Set("my_week_drawn", myWeekDrawn).
		Set("my_week_left", myWeekLeft).
		Set("cooldown_seconds", cfg.Int("cooldown_seconds")).
		Set("cooldown_left", cooldownLeft).
		Set("show_prizes", cfg.Int("show_prizes")).
		Set("show_stock", cfg.Int("show_stock")).
		Set("daily_total_limit", cfg.Int("daily_total_limit")).
		Set("daily_total_left", dailyTotalLeft).
		Set("success_text", cfg.Str("success_text")).
		Set("empty_text", cfg.Str("empty_text")).
		Set("start_date", cfg.Str("start_date")).
		Set("end_date", cfg.Str("end_date")).
		Set("windows_enabled", cfg.Int("windows_enabled")).
		Set("windows", windows).
		Set("prizes", prizes).
		Set("records", records), 0, "ok", 0)
}

// ---------- lottery_draw (api.php:1493) ----------

func (rt *Router) handleLotteryDraw(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "lottery_draw")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	s := newLotteryScope(rt, c)
	cfg := s.cfg()
	meID := rowInt(me, "id", 0)

	// 校验顺序固定: 开关 -> 日期范围 -> 开放时段 -> 冷却 -> 全站每日总量 -> 每日 -> 每周 -> 总次数 -> 池子
	if cfg.Int("enabled") != 1 {
		c.Error("抽奖活动已关闭", 1)
		return
	}
	switch lotteryDateRangeState(cfg, s.nowTS()) {
	case 1:
		c.Error("抽奖活动还没开始", 1)
		return
	case 2:
		c.Error("抽奖活动已经结束", 1)
		return
	}
	if !lotteryWindowTSOpen(cfg, s.nowTS()) {
		win := s.windowState(cfg, s.nowTS())
		msg := "现在不在抽奖时间内"
		if v, has := win.Get("next_open_at"); has {
			if na := phpStr(v); na != "" {
				msg += ", 下次开放: " + na
			}
		}
		c.Error(msg, 1)
		return
	}
	if cdLeft := s.cooldownLeft(me); cdLeft > 0 {
		c.Error("抽得太快啦, 请 "+phpNum(cdLeft)+" 秒后再试", 1)
		return
	}
	if s.dailyTotalLeft(cfg) == 0 {
		c.Error("今天的奖品已经发完了, 明天再来", 1)
		return
	}
	todayLeft, err := s.dailyLeftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	if todayLeft == 0 {
		c.Error("今天的抽奖次数已用完, 明天再来", 1)
		return
	}
	weekLeft, err := s.weeklyLeftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	if weekLeft == 0 {
		c.Error("本周的抽奖次数已用完, 下周再来", 1)
		return
	}
	left, err := s.leftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	if left <= 0 {
		c.Error("你的抽奖次数已用完", 1)
		return
	}

	got, ok := s.drawOnce(meID)
	if !ok {
		return
	}

	// 中奖通知：commit 之后才写；失败不影响抽奖结果（PHP try/catch 吞掉）。
	if cfg.Int("notify_winner") == 1 {
		content := "你的卡密: " + got.code + "\n(长按可复制, 也可以随时在「我的 → 消息中心」查看)"
		if _, err := s.db().Exec(c.R.Context(),
			"INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)",
			meID, "恭喜抽中 "+got.prizeName, content, "lottery", ""); err != nil {
			c.Log().Error("[lottery notify] 写中奖通知失败, 不影响抽奖结果（PHP error_log）", "err", err)
		}
	}

	// 响应里的计数是「抽之后」查库；left 用的 $me 是事务前的快照（契约 §9.14）。
	leftAfter, err := s.leftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	todayLeftAfter, err := s.dailyLeftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	weekLeftAfter, err := s.weeklyLeftFor(me)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("draw_id", got.id).
		Set("prize_id", got.prizeID).
		Set("prize_name", got.prizeName).
		Set("card_type", got.cardType).
		Set("code", got.code).
		Set("left", leftAfter).
		Set("my_today_left", todayLeftAfter).
		Set("my_week_left", weekLeftAfter).
		Set("daily_total_left", s.dailyTotalLeft(cfg)).
		Set("cooldown_seconds", cfg.Int("cooldown_seconds")).
		Set("cooldown_left", s.cooldownLeft(me)), 0, "ok", 0)
}

// ---------- lottery_records (api.php:1541) ----------

func (rt *Router) handleLotteryRecords(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "lottery_records")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	s := newLotteryScope(rt, c)
	meID := rowInt(me, "id", 0)
	page := c.ParamInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.ParamInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 50 {
		ps = 50
	}
	// total 不带 reset 条件（历史全量，api.php:1545）。
	total, _, err := s.db().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM lottery_draws WHERE user_id = ?", meID)
	if err != nil {
		c.dbError(err)
		return
	}
	// LIMIT / OFFSET 是十进制字符串拼接（已整数化，无注入面），与 PHP 逐字一致。
	sql := `SELECT d.id, d.prize_id, d.code, d.created_at, p.name AS prize_name, p.card_type
        FROM lottery_draws d LEFT JOIN lottery_prizes p ON p.id = d.prize_id
        WHERE d.user_id = ? ORDER BY d.id DESC LIMIT ` + phpNum(ps) + ` OFFSET ` + phpNum((page-1)*ps)
	rows, err := s.db().QueryAll(c.R.Context(), sql, meID)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		list = append(list, lotteryRecordItem(row))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", phpInt(total)).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- PHP param() 的 isset 语义（显式 null 视为未提交） ----------

// paramSet 复刻 config.php:37 param() 的 **isset** 语义：
// JSON 里显式给 null 的值不算命中，继续回落 $_POST/$_GET，最后才是默认值。
// ctx.Param 用的是「键存在即命中」（对应 PHP 的 array_key_exists），
// admin_lottery_config_set 这种「未传=保持旧值」的接口必须用本方法（契约 §1.1、§9.11）。
func (c *Ctx) paramSet(key string) (any, bool) {
	if b := c.Body(); b != nil {
		if v, ok := b[key]; ok && v != nil {
			return v, true
		}
	}
	if f := c.PostForm(); f != nil {
		if vs, ok := f[key]; ok && len(vs) > 0 {
			return vs[0], true
		}
	}
	if c.mp != nil {
		if vs, ok := c.mp[key]; ok && len(vs) > 0 {
			return vs[0], true
		}
	}
	if vs, ok := c.Query[key]; ok && len(vs) > 0 {
		return vs[0], true
	}
	return nil, false
}

// paramSetAny 取原始值（缺省/显式为 null 时返回 def）。
func (c *Ctx) paramSetAny(key string, def any) any {
	if v, ok := c.paramSet(key); ok {
		return v
	}
	return def
}

// paramSetStr 复刻 `(string)param($key, $def)`。
func (c *Ctx) paramSetStr(key, def string) string {
	if v, ok := c.paramSet(key); ok {
		return phpStr(v)
	}
	return def
}

// paramSetInt 复刻 `(int)param($key, $def)`。
func (c *Ctx) paramSetInt(key string, def int64) int64 {
	if v, ok := c.paramSet(key); ok {
		return phpInt(v)
	}
	return def
}
