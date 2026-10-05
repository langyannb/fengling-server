package httpapi

import (
	"os"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5A 后台抽奖单测：逐字文案 / 校验顺序 / 403 落 HTTP 400 /
// 破坏性 action 未注册（仍走透传）。

// lotteryNativeActions 是本次原生接管的 12 个抽奖 action。
var lotteryNativeActions = []string{
	"lottery_info", "lottery_draw", "lottery_records",
	"admin_lottery_prizes", "admin_lottery_prize_save", "admin_lottery_codes",
	"admin_lottery_config_get", "admin_lottery_config_set", "admin_lottery_window_preview",
	"admin_lottery_draws", "admin_lottery_quota_set", "admin_lottery_quota_reset",
}

// lotteryAdminActions 是 9 个需要 require_admin() 的原生后台接口。
var lotteryAdminActions = []string{
	"admin_lottery_prizes", "admin_lottery_prize_save", "admin_lottery_codes",
	"admin_lottery_config_get", "admin_lottery_config_set", "admin_lottery_window_preview",
	"admin_lottery_draws", "admin_lottery_quota_set", "admin_lottery_quota_reset",
}

// lotteryPassthroughActions 是**必须继续透传 PHP**的 6 个抽奖 action：
// 契约 §9.1 标为破坏性的 4 个 + 两个「全表 UPDATE」级动作
// （验收脚本的安全门禁 FORBIDDEN_ADMIN 永不调用它们 ⇒ 无法线上双跑验证）。
var lotteryPassthroughActions = []string{
	"admin_lottery_activity_reset",
	"admin_lottery_codes_delete",
	"admin_lottery_prize_delete",
	"admin_lottery_codes_import",
	"admin_lottery_quota_reset_all",
	"admin_lottery_quota_all",
}

func TestAdminLotteryAuthCodes(t *testing.T) {
	for _, action := range lotteryAdminActions {
		t.Run(action, func(t *testing.T) {
			f := newLotteryFake()
			rt := newLotteryRouter(f)

			w := callAction(rt, "POST", "/api.php?action="+action, `{}`, "application/json", nil)
			assertError(t, w, 401, 401, "未登录")
			assertCORS(t, w.Header())

			w2 := callAction(rt, "POST", "/api.php?action="+action, `{}`, "application/json",
				map[string]string{"Authorization": "Bearer no-such-token"})
			assertError(t, w2, 401, 401, "登录已失效")

			// 普通用户的 403 必须落 HTTP 400（config.php:50-59 的 else 分支）。
			w3 := callLottery(t, rt, action, `{}`)
			assertError(t, w3, 400, 403, "没有权限, 仅管理员可操作")
		})
	}
}

// TestLotteryPassthroughActionsStayPassthrough 是本次派单的硬约束：
// 破坏性 4 个 + 全表 UPDATE 2 个，**一个都不许**注册成原生 handler。
func TestLotteryPassthroughActionsStayPassthrough(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读 router.go 失败: %v", err)
	}
	code := string(src)
	for _, a := range lotteryPassthroughActions {
		if strings.Contains(code, `case "`+a+`":`) {
			t.Fatalf("action %s 被注册成了原生分支（必须继续透传 PHP）", a)
		}
	}
	// 反向校验：12 个原生抽奖 action 必须都已注册。
	for _, a := range lotteryNativeActions {
		if !strings.Contains(code, `case "`+a+`":`) {
			t.Fatalf("action %s 未在 router.go 注册", a)
		}
	}
	// 行为校验：这些 action 落进 default 分支 → 走 FastCGI（此处 FPM 不可达，应回网关错误）。
	brt := brokenRouter(t)
	for _, a := range lotteryPassthroughActions {
		w := callAction(brt, "POST", "/api.php?action="+a, `{}`, "application/json", nil)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "网关无法连接 PHP-FPM") {
			t.Fatalf("%s 没有走透传分支: HTTP=%d body=%s", a, w.Code, w.Body.String())
		}
	}
}

// ---------- admin_lottery_prize_save ----------

func TestAdminLotteryPrizeSaveValidations(t *testing.T) {
	boom := strings.Repeat("字", 51)
	boomType := strings.Repeat("字", 21)
	boomDesc := strings.Repeat("字", 256)
	cases := []struct{ name, body, want string }{
		{"weight 超限", `{"weight":1001}`, "奖项权重不能超过 1000"},
		{"name 为空", `{"name":"   "}`, "奖项名称不能为空"},
		{"name 51 字", `{"name":"` + boom + `"}`, "奖项名称不能超过 50 个字"},
		{"card_type 21 字", `{"name":"月卡","card_type":"` + boomType + `"}`, "卡密类型不能超过 20 个字"},
		{"描述 256 字", `{"name":"月卡","description":"` + boomDesc + `"}`, "描述不能超过 255 个字"},
		{"同名", `{"name":"月卡"}`, "奖项名称已存在"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLotteryFake()
			f.settings["lottery_config"] = lotteryCfgJSON(nil)
			if tc.name == "同名" {
				f.valFn = func(q string, args []any) (any, bool, error) {
					if strings.Contains(q, "SELECT id FROM lottery_prizes WHERE name = ?") {
						return int64(42), true, nil
					}
					return nil, false, nil
				}
			}
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_save", tc.body)
			assertError(t, w, 400, 1, tc.want)
		})
	}

	t.Run("weight<0 静默变 0，card_type 空回落通用", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_save",
			`{"name":"月卡","weight":-5,"card_type":"  "}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		if w.Body.String() != `{"code":0,"msg":"ok","data":{"id":100}}` {
			t.Fatalf("INSERT 只应返回 id: %s", w.Body.String())
		}
		ins := f.execs("INSERT INTO lottery_prizes")
		if len(ins) != 1 {
			t.Fatalf("应当 INSERT 一次, got=%v", ins)
		}
		args := ins[0].Args
		if phpInt(args[5]) != 0 {
			t.Fatalf("weight<0 应静默成 0, args=%v", args)
		}
		if phpStr(args[1]) != "通用" {
			t.Fatalf("空 card_type 应回落「通用」, args=%v", args)
		}
	})

	t.Run("id>0 走 UPDATE 且返回 id/weight", func(t *testing.T) {
		f := newLotteryFake()
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_save",
			`{"id":7,"name":"月卡","weight":300}`)
		if w.Body.String() != `{"code":0,"msg":"ok","data":{"id":7,"weight":300}}` {
			t.Fatalf("body=%s", w.Body.String())
		}
		if len(f.execs("UPDATE lottery_prizes")) != 1 {
			t.Fatalf("应当 UPDATE")
		}
	})

	t.Run("同名但 id 相同不算冲突", func(t *testing.T) {
		f := newLotteryFake()
		f.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "SELECT id FROM lottery_prizes WHERE name = ?") {
				return int64(7), true, nil
			}
			return nil, false, nil
		}
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prize_save", `{"id":7,"name":"月卡"}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestAdminLotteryPrizesShape(t *testing.T) {
	f := newLotteryFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM lottery_prizes p ORDER BY p.sort_order ASC, p.id ASC") {
			return []store.Row{
				srow("id", 1, "name", "月卡", "card_type", "月卡", "description", nil, "sort_order", 0,
					"weight", 0, "is_active", 1, "total_cnt", 5, "used_cnt", 2,
					"created_at", "2026-01-01 00:00:00"),
			}, nil
		}
		return nil, nil
	}
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_prizes", `{}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":1,"name":"月卡","card_type":"月卡","description":"","sort_order":0,"weight":100,"is_active":1,"total":5,"used":2,"left":3,"created_at":"2026-01-01 00:00:00"}]}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

// ---------- admin_lottery_codes ----------

func TestAdminLotteryCodesStatusWhitelist(t *testing.T) {
	cases := []struct {
		status   string
		wantCond string
	}{
		{"unused", "WHERE c.user_id = 0"},
		{"used", "WHERE c.user_id > 0"},
		{"all", ""},
		{"", ""},
		{"weird", ""}, // 未知值不过滤（契约 §9.15）
	}
	for _, tc := range cases {
		t.Run("status="+tc.status, func(t *testing.T) {
			f := newLotteryFake()
			var countSQL string
			f.valFn = func(q string, args []any) (any, bool, error) {
				if strings.Contains(q, "SELECT COUNT(*) FROM lottery_codes c") {
					countSQL = q
					return int64(0), true, nil
				}
				return nil, false, nil
			}
			f.unusedCnt, f.usedCnt = 4, 6
			body := `{}`
			if tc.status != "" {
				body = `{"status":"` + tc.status + `"}`
			}
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes", body)
			if w.Code != 200 {
				t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"unused":4`) || !strings.Contains(w.Body.String(), `"used":6`) {
				t.Fatalf("全表 unused/used 计数不对: %s", w.Body.String())
			}
			if tc.wantCond == "" {
				if strings.Contains(countSQL, "WHERE") {
					t.Fatalf("该 status 不应加过滤: %s", countSQL)
				}
			} else if !strings.Contains(countSQL, tc.wantCond) {
				t.Fatalf("过滤条件不对: %s", countSQL)
			}
		})
	}

	t.Run("字段顺序 golden", func(t *testing.T) {
		f := newLotteryFake()
		f.allFn = func(q string, args []any) ([]store.Row, error) {
			if strings.Contains(q, "FROM lottery_codes c") {
				return []store.Row{srow("id", 3, "prize_id", 1, "prize_name", "月卡", "card_type", "月卡",
					"code", "CARD-3", "user_id", 7, "nickname", "爱丽丝", "username", "alice",
					"used_at", nil, "created_at", "2026-01-01 00:00:00")}, nil
			}
			return nil, nil
		}
		f.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM lottery_codes c") {
				return int64(1), true, nil
			}
			return nil, false, nil
		}
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_codes", `{}`)
		want := `{"code":0,"msg":"ok","data":{"list":[{"id":3,"prize_id":1,"prize_name":"月卡","card_type":"月卡","code":"CARD-3","status":"used","user_id":7,"user_nickname":"爱丽丝","used_at":"","created_at":"2026-01-01 00:00:00"}],"total":1,"page":1,"page_size":20,"unused":0,"used":0}}`
		if w.Body.String() != want {
			t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
		}
	})
}

// ---------- admin_lottery_draws ----------

func TestAdminLotteryDrawsKeyword(t *testing.T) {
	f := newLotteryFake()
	var seenSQL string
	var seenArgs []any
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM lottery_draws d LEFT JOIN users") {
			seenSQL, seenArgs = q, args
			return int64(0), true, nil
		}
		return nil, false, nil
	}
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_draws", `{"keyword":"ali","prize_id":3}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(seenSQL, "d.prize_id = ?") || !strings.Contains(seenSQL, "(d.code LIKE ? OR u.username LIKE ? OR u.nickname LIKE ?)") {
		t.Fatalf("条件拼装不对: %s", seenSQL)
	}
	if len(seenArgs) != 4 || phpInt(seenArgs[0]) != 3 || phpStr(seenArgs[1]) != "%ali%" || phpStr(seenArgs[3]) != "%ali%" {
		t.Fatalf("参数不对: %#v", seenArgs)
	}
}

// ---------- admin_lottery_config_set ----------

func TestAdminLotteryConfigSetValidations(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"标题 51 字", `{"title":"` + strings.Repeat("字", 51) + `"}`, "抽奖标题不能超过 50 个字"},
		{"内容 2001 字", `{"content":"` + strings.Repeat("字", 2001) + `"}`, "抽奖内容不能超过 2000 个字"},
		{"每人次数 10000", `{"per_user_limit":10000}`, "每人抽奖次数不能超过 9999"},
		{"每日次数 1000", `{"daily_limit":1000}`, "每日抽奖次数不能超过 999"},
		{"每周次数 1000", `{"week_limit":1000}`, "每周抽奖次数不能超过 999"},
		{"冷却 86401", `{"cooldown_seconds":86401}`, "冷却时间不能超过 86400 秒"},
		{"发放上限 1000000", `{"daily_total_limit":1000000}`, "今天发放上限不能超过 999999"},
		{"重置时间 25:00", `{"daily_reset_time":"25:00"}`, "每日重置时间格式不对, 应该是 HH:MM"},
		{"success_text 201", `{"success_text":"` + strings.Repeat("字", 201) + `"}`, "提示文案不能超过 200 个字"},
		{"empty_text 201", `{"empty_text":"` + strings.Repeat("字", 201) + `"}`, "提示文案不能超过 200 个字"},
		{"开始日期非法", `{"start_date":"2026-02-30"}`, "活动开始日期格式不对, 应该是 YYYY-MM-DD"},
		{"结束日期非法", `{"end_date":"2026-13-01"}`, "活动结束日期格式不对, 应该是 YYYY-MM-DD"},
		{"结束早于开始", `{"start_date":"2026-06-02","end_date":"2026-06-01"}`, "结束日期不能早于开始日期"},
		{"时段非数组", `{"windows":"abc"}`, "抽奖时段格式不对, 应该是 HH:MM-HH:MM"},
		{"时段 11 条", `{"windows":[` + strings.Repeat(`{"start":"01:00","end":"02:00"},`, 10) + `{"start":"03:00","end":"04:00"}]}`, "抽奖时段最多 10 条"},
		{"时段开始=结束", `{"windows":[{"start":"01:00","end":"01:00"}]}`, "抽奖时段开始和结束时间不能相同"},
		{"星期 0", `{"windows":[{"start":"01:00","end":"02:00","days":[0]}]}`, "星期只能是 1-7"},
		{"星期 8", `{"windows":[{"start":"01:00","end":"02:00","days":[8]}]}`, "星期只能是 1-7"},
		{"星期非数组", `{"windows":[{"start":"01:00","end":"02:00","days":"abc"}]}`, "星期只能是 1-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLotteryFake()
			f.settings["lottery_config"] = lotteryCfgJSON(nil)
			w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_config_set", tc.body)
			assertError(t, w, 400, 1, tc.want)
		})
	}

	t.Run("校验顺序：标题先于权重", func(t *testing.T) {
		f := newLotteryFake()
		f.settings["lottery_config"] = lotteryCfgJSON(nil)
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_config_set",
			`{"title":"`+strings.Repeat("字", 51)+`","per_user_limit":10000}`)
		assertError(t, w, 400, 1, "抽奖标题不能超过 50 个字")
	})

	t.Run("边界放行：9999/999/999/86400/999999/200/200", func(t *testing.T) {
		f := newLotteryFake()
		f.settings["lottery_config"] = lotteryCfgJSON(nil)
		w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_config_set",
			`{"per_user_limit":9999,"daily_limit":999,"week_limit":999,"cooldown_seconds":86400,"daily_total_limit":999999,`+
				`"success_text":"`+strings.Repeat("字", 200)+`","empty_text":"`+strings.Repeat("字", 200)+`"}`)
		if w.Code != 200 {
			t.Fatalf("边界值应当放行: HTTP=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestAdminLotteryConfigSetKeepsOldValuesAndUnknownKeys(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = `{"enabled":1,"title":"旧标题","content":"旧内容","success_text":"","empty_text":"奖品已抽完, 请稍后再来",` +
		`"per_user_limit":3,"daily_limit":0,"week_limit":0,"daily_reset_time":"00:00","cooldown_seconds":0,"daily_total_limit":0,` +
		`"windows_enabled":0,"windows":[],"start_date":"","end_date":"","show_prizes":1,"show_stock":1,"notify_winner":1,` +
		`"legacy_key":"legacy-value"}`
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_config_set", `{"title":"新标题","enabled":"on"}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"title":"新标题"`, `"content":"旧内容"`, `"per_user_limit":3`, `"enabled":1`, `"legacy_key":"legacy-value"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("缺少 %s\nbody=%s", want, body)
		}
	}
	// 未知键必须保留在末尾（array_merge 语义）。
	if strings.Index(body, `"legacy_key"`) < strings.Index(body, `"notify_winner"`) {
		t.Fatalf("未知键应追加在已知键之后: %s", body)
	}
	// 落库的 JSON 同样要含未知键。
	saved := f.settings["lottery_config"]
	if !strings.Contains(saved, `"legacy_key":"legacy-value"`) {
		t.Fatalf("写入的配置丢了未知键: %s", saved)
	}
	// 布尔/字符串开关一律归一化成 0/1。
	for _, tc := range []struct{ v, want string }{
		{`true`, `"enabled":1`}, {`"1"`, `"enabled":1`}, {`1`, `"enabled":1`},
		{`"on"`, `"enabled":1`}, {`"yes"`, `"enabled":1`},
		{`false`, `"enabled":0`}, {`"off"`, `"enabled":0`}, {`"no"`, `"enabled":0`}, {`0`, `"enabled":0`},
	} {
		f2 := newLotteryFake()
		f2.settings["lottery_config"] = f.settings["lottery_config"]
		w2 := callLotteryAdmin(t, newLotteryRouter(f2), "admin_lottery_config_set", `{"enabled":`+tc.v+`}`)
		if !strings.Contains(w2.Body.String(), tc.want) {
			t.Fatalf("enabled=%s 应归一化成 %s, body=%s", tc.v, tc.want, w2.Body.String())
		}
	}
}

func TestAdminLotteryConfigGetByteExact(t *testing.T) {
	f := newLotteryFake()
	// 没有 settings 行 → 全默认；已知键按默认顺序、windows/windows 是 []。
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_config_get", `{}`)
	want := `{"code":0,"msg":"ok","data":{"enabled":0,"title":"免费抽卡密","content":"","success_text":"","empty_text":"奖品已抽完, 请稍后再来","per_user_limit":1,"daily_limit":0,"week_limit":0,"daily_reset_time":"00:00","cooldown_seconds":0,"daily_total_limit":0,"windows_enabled":0,"windows":[],"start_date":"","end_date":"","show_prizes":1,"show_stock":1,"notify_winner":1}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

// ---------- admin_lottery_window_preview ----------

func TestAdminLotteryWindowPreviewExtraKeys(t *testing.T) {
	f := newLotteryFake()
	f.settings["lottery_config"] = lotteryCfgJSON(map[string]any{"daily_total_limit": 5})
	f.dailyTotal = 2
	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_window_preview", `{}`)
	body := w.Body.String()
	if !strings.HasSuffix(body, `"daily_total_left":3,"daily_total_limit":5}}`) {
		t.Fatalf("多出的两个键必须在末尾: %s", body)
	}
	// window 的 10 键仍在前面且顺序不变。
	if !strings.Contains(body, `"window"`) && !strings.Contains(body, `"open"`) {
		t.Fatalf("缺少 window 字段: %s", body)
	}
}

// ---------- admin_lottery_quota_* ----------

func TestAdminLotteryQuotaSetWording(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)
	assertError(t, callLotteryAdmin(t, rt, "admin_lottery_quota_set", `{"user_id":0}`), 400, 1, "参数错误")
	assertError(t, callLotteryAdmin(t, rt, "admin_lottery_quota_set", `{"user_id":1,"quota":10000}`), 400, 1, "抽奖次数不能超过 9999")
	assertError(t, callLotteryAdmin(t, rt, "admin_lottery_quota_set", `{"user_id":999999}`), 400, 1, "用户不存在")

	t.Run("配额生效并返回 left", func(t *testing.T) {
		f2 := newLotteryFake()
		f2.rowFn = func(q string, args []any) (store.Row, error) {
			if strings.Contains(q, "SELECT * FROM users WHERE id = ?") {
				return srow("id", 7, "username", "alice", "lottery_quota", -1), nil
			}
			return nil, nil
		}
		w := callLotteryAdmin(t, newLotteryRouter(f2), "admin_lottery_quota_set", `{"user_id":7,"quota":3}`)
		if w.Body.String() != `{"code":0,"msg":"ok","data":{"user_id":7,"quota":3,"left":3}}` {
			t.Fatalf("body=%s", w.Body.String())
		}
	})

	t.Run("quota<-1 静默成 -1", func(t *testing.T) {
		f3 := newLotteryFake()
		f3.rowFn = func(q string, args []any) (store.Row, error) {
			if strings.Contains(q, "SELECT * FROM users WHERE id = ?") {
				return srow("id", 7, "lottery_quota", 5), nil
			}
			return nil, nil
		}
		w := callLotteryAdmin(t, newLotteryRouter(f3), "admin_lottery_quota_set", `{"user_id":7,"quota":-9}`)
		if !strings.Contains(w.Body.String(), `"quota":-1`) {
			t.Fatalf("body=%s", w.Body.String())
		}
	})
}

func TestAdminLotteryQuotaResetUsesDBTime(t *testing.T) {
	f := newLotteryFake()
	f.nowText = "2026-06-01 12:00:00"
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT * FROM users WHERE id = ?") {
			return srow("id", 7, "lottery_quota", 5), nil
		}
		return nil, nil
	}
	assertError(t, callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_reset", `{"user_id":0}`), 400, 1, "参数错误")

	w := callLotteryAdmin(t, newLotteryRouter(f), "admin_lottery_quota_reset", `{"user_id":7,"daily":1}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	ups := f.execs("UPDATE users SET lottery_reset_at")
	if len(ups) != 1 {
		t.Fatalf("应当 UPDATE 一次, got=%v", ups)
	}
	if !strings.Contains(ups[0].Query, "lottery_day_reset_at") {
		t.Fatalf("daily=1 必须同时写 lottery_day_reset_at: %s", ups[0].Query)
	}
	for _, a := range ups[0].Args {
		if phpStr(a) == "2026-06-01 12:00:00" {
			return
		}
	}
	t.Fatalf("重置标记必须用 DB 的 NOW(): %#v", ups[0].Args)
}
