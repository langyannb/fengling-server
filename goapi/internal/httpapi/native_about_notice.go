package httpapi

import (
	"net/http"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// 阶段 5A 附带：about_config_get / notice_get 两个**只读公开**接口的原生实现。
//
// 说明（冲突点，已写进回报）：派单书 `.go006-dispatch.md` 与抽奖契约 `.go006-contract.md`
// 都没有提这两个 action（契约 §0 只列 18 个抽奖 action）。用户任务要求「about/notice 的
// 读取按派单书要求一并处理」，而派单书无此要求 ⇒ 按契约「不猜」的精神，这里只搬
// **读取**（写入侧 about_config_set / notice_set 继续透传），语义逐字对齐 api.php:2855-2871
// 与 api.php:3012-3021。
//
// 两者都是「默认值数组 + DB 里 JSON 对象」的 array_merge 形态：
// 已知键保持默认顺序、值以 DB 为准；DB 里的未知键追加在末尾；空集合仍是对象（不是 []）。

// settingJSONArray 读 settings 某行的 value，并保序解析成「PHP 数组」。
// 对齐 `$row ? json_decode($row['value'], true) : []`：没有行 / 解不出来都当空数组。
func (rt *Router) settingJSONArray(c *Ctx, key string) (any, bool) {
	// PHP 这里是裸 db()->query()，异常会冒到外层变成 500。
	row, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM settings WHERE `key` = ?", key)
	if err != nil {
		c.dbError(err)
		return nil, false
	}
	if row == nil {
		return nil, true
	}
	v, _ := row.Get("value")
	raw := phpStr(v)
	if phpFalsy(raw) {
		return nil, true
	}
	dec, ok := decodeOrderedJSON(raw)
	if !ok {
		return nil, true
	}
	return dec, true
}

// mergeDefaults 复刻 array_merge($defaults, $cfg)：
// 字符串键原位覆盖（顺序保持默认），未知字符串键追加末尾；
// $cfg 若是列表，其值以 "0","1",… 追加（PHP 会给数字键重排）。
func mergeDefaults(defaults []struct {
	k string
	v any
}, cfg any) *phpjson.O {
	out := phpjson.New()
	for _, d := range defaults {
		out.Set(d.k, d.v)
	}
	if obj, ok := cfg.(*phpjson.O); ok {
		for _, k := range obj.Keys() {
			v, _ := obj.Get(k)
			out.Put(k, v)
		}
		return out
	}
	if arr, ok := cfg.([]any); ok {
		for i, v := range arr {
			out.Set(phpNum(int64(i)), v)
		}
	}
	return out
}

// handleAboutConfigGet 对齐 api.php:2855 about_config_get（公开）。
func (rt *Router) handleAboutConfigGet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "about_config_get")
	cfg, ok := rt.settingJSONArray(c, "about_config")
	if !ok {
		return
	}
	out := mergeDefaults([]struct {
		k string
		v any
	}{
		{"qq_group", ""},
		{"qq_key", ""},
		{"qq_url", ""},
		{"qq_channel", ""},
		{"website", ""},
		{"github", ""},
		{"feedback", ""},
		{"donate", ""},
		{"banner_text", "风铃分享库 · 官方频道"},
		{"banner_sub", "最新软件 · 更新通知 · 交流反馈"},
	}, cfg)
	c.JSON(out, 0, "ok", 0)
}

// handleNoticeGet 对齐 api.php:3012 notice_get（公开）。
func (rt *Router) handleNoticeGet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "notice_get")
	cfg, ok := rt.settingJSONArray(c, "notice")
	if !ok {
		return
	}
	out := mergeDefaults([]struct {
		k string
		v any
	}{
		{"content", ""},
		{"mode", "daily"},
		{"enabled", int64(0)},
	}, cfg)
	c.JSON(out, 0, "ok", 0)
}
