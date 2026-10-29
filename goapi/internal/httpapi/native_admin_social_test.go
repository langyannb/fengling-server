package httpapi

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5B-1 的 22 个 admin action 单测：全部走 lotteryFake（脚本化假 DB），
// 不连真实 MySQL / Redis，不调用任何真实接口。

// adminCall 用管理员 token 调一个 admin action（JSON body）。
func adminCall(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json",
		map[string]string{"Authorization": "Bearer " + lotteryAdminToken})
}

// callWithToken 用任意 token 调 action。
func callWithToken(rt *Router, action, token string) *httptest.ResponseRecorder {
	h := map[string]string{}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return callAction(rt, "GET", "/api.php?action="+action, "", "", h)
}

// ---------- ① 鉴权四态 ----------

func TestAdminAuthFourStates(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)

	// 无 token
	w := callAction(rt, "GET", "/api.php?action=admin_users", "", "", nil)
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"未登录","data":null}` {
		t.Fatalf("无 token 应 401 未登录: code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Origin"), "*") {
		t.Fatalf("admin action 必须 setCORS")
	}

	// 坏 token
	w = callWithToken(rt, "admin_users", "bad-token")
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"登录已失效","data":null}` {
		t.Fatalf("坏 token 应 401 登录已失效: code=%d body=%s", w.Code, w.Body.String())
	}

	// 普通用户：JSON code=403，但 HTTP 状态码是 400（statusForCode）
	w = callWithToken(rt, "admin_users", lotteryUserToken)
	if w.Code != 400 || w.Body.String() != `{"code":403,"msg":"没有权限, 仅管理员可操作","data":null}` {
		t.Fatalf("普通用户应 HTTP400+code403: code=%d body=%s", w.Code, w.Body.String())
	}

	// 管理员：进入业务逻辑（假 DB 无数据 → 空 list）
	w = callWithToken(rt, "admin_users", lotteryAdminToken)
	want := `{"code":0,"msg":"ok","data":{"list":[],"total":0,"page":1,"page_size":20}}`
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("管理员应成功且空 list 为 []: code=%d\n got=%s\nwant=%s", w.Code, w.Body.String(), want)
	}
}

// ---------- ② 分页边界与钳制 ----------

func TestAdminUsersPaginationClamp(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_users", `{"page":0,"page_size":999}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	want := `{"code":0,"msg":"ok","data":{"list":[],"total":0,"page":1,"page_size":100}}`
	if w.Body.String() != want {
		t.Fatalf("page 应钳到 1、page_size 钳到 100:\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// LIMIT 0, 100（PHP `LIMIT off, size`）
	found := false
	for _, c := range f.calls {
		if c.Method == "QueryAll" && strings.Contains(c.Query, "FROM users") &&
			strings.Contains(c.Query, "ORDER BY id ASC LIMIT 0, 100") {
			found = true
		}
	}
	if !found {
		t.Fatalf("未看到 SELECT * FROM users ORDER BY id ASC LIMIT 0, 100")
	}
}

func TestAdminUsersFiltersAndNegativePage(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)
	_ = adminCall(t, rt, "admin_users", `{"page":-5,"page_size":0,"keyword":"bob","role":"admin","is_active":"1"}`)
	var cntQ, listQ string
	for _, c := range f.calls {
		if c.Method == "QueryValue" && strings.Contains(c.Query, "SELECT COUNT(*) FROM users") {
			cntQ = c.Query
		}
		if c.Method == "QueryAll" && strings.Contains(c.Query, "FROM users") {
			listQ = c.Query
		}
	}
	wantWhere := " WHERE (username LIKE ? OR nickname LIKE ? OR email LIKE ? OR tags LIKE ?) AND role = ? AND is_active = ?"
	if !strings.Contains(cntQ, wantWhere) {
		t.Fatalf("COUNT 的 WHERE 不对: %s", cntQ)
	}
	// page=-5 → max(1,-5)=1；page_size=0 → max(1,0)=1 → LIMIT 0, 1
	if !strings.Contains(listQ, "LIMIT 0, 1") {
		t.Fatalf("负 page / 0 size 未钳制: %s", listQ)
	}
}

func TestAdminGroupMembersClampSixty(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT * FROM social_groups WHERE id = ?") {
			return socialGroupRow(), nil
		}
		return nil, nil
	}
	rt := newLotteryRouter(f)
	_ = adminCall(t, rt, "admin_group_members", `{"group_id":5,"page_size":999}`)
	var q string
	for _, c := range f.calls {
		if c.Method == "QueryAll" && strings.Contains(c.Query, "social_group_members m JOIN users u") {
			q = c.Query
		}
	}
	if q == "" || !strings.Contains(q, "LIMIT 60 OFFSET 0") {
		t.Fatalf("admin_group_members page_size 上限应为 60: %q", q)
	}
	// FIELD 方言必须换成 CASE
	if !strings.Contains(q, "ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END") {
		t.Fatalf("FIELD() 未改成 CASE: %s", q)
	}
}

// ---------- ③ 空集合形态 ----------

func TestAdminGroupsEmptyListIsArray(t *testing.T) {
	f := newLotteryFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) { return nil, nil }
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_groups", "")
	want := `{"code":0,"msg":"ok","data":{"list":[]}}`
	if w.Body.String() != want {
		t.Fatalf("空群组列表必须是 []（不是 null/{}）:\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

// ---------- ④ 逐字文案与结构 ----------

func TestAdminGroupsLiteral(t *testing.T) {
	f := newLotteryFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM social_groups g ORDER BY") {
			return []store.Row{socialGroupRow()}, nil
		}
		return nil, nil
	}
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_group_members") {
			return int64(3), true, nil
		}
		return nil, false, nil
	}
	f.rowFn = func(q string, args []any) (store.Row, error) { return nil, nil }
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_groups", "")
	want := `{"code":0,"msg":"ok","data":{"list":[{"muted":0,"unread":0,"first_unread_id":0,"last_message":null,"last_time":"","at_me":0,"at_me_first":0,"at_all":0,"at_all_first":0,"id":5,"name":"测试群","icon":"","description":"","notice":"","member_count":3,"is_member":0,"message_count":0,"sort_order":0,"is_active":1,"all_muted":0,"created_at":"2026-01-01 10:00:00"}]}}`
	if w.Body.String() != want {
		t.Fatalf("admin_groups 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

func TestAdminNotificationListRawRowStrings(t *testing.T) {
	f := newLotteryFake()
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM notifications n") {
			return int64(2), true, nil
		}
		return nil, false, nil
	}
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		return []store.Row{srow(
			"id", "9", "user_id", "3", "title", "标题", "content", "正文", "type", "admin",
			"link", "", "is_read", "0", "created_at", "2026-01-01 10:00:00",
			"nickname", "昵称", "username", "u")}, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_notification_list", "")
	// PDO 模拟预处理把整数返回成字符串；n.* 列序固定，随后 nickname/username/user_name。
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":"9","user_id":"3","title":"标题","content":"正文","type":"admin","link":"","is_read":"0","created_at":"2026-01-01 10:00:00","nickname":"昵称","username":"u","user_name":"昵称"}],"total":2,"page":1,"page_size":20}}`
	if w.Body.String() != want {
		t.Fatalf("admin_notification_list 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

func TestAdminNotificationDeleteClear(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_notification_delete", `{"id":0}`)
	want := `{"code":0,"msg":"ok","data":{"ok":true,"cleared":true}}`
	if w.Body.String() != want {
		t.Fatalf("id<=0 应全表清空: %s", w.Body.String())
	}
	clears := f.execs("DELETE FROM notifications")
	if len(clears) != 1 || strings.Contains(clears[0].Query, "WHERE") {
		t.Fatalf("应当只有一条无 WHERE 的 DELETE FROM notifications: %+v", clears)
	}
	// id>0 只删一条
	f2 := newLotteryFake()
	rt2 := newLotteryRouter(f2)
	w = adminCall(t, rt2, "admin_notification_delete", `{"id":42}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true}}` {
		t.Fatalf("id>0 单条删除: %s", w.Body.String())
	}
}

func TestAdminGroupMemberRemoveNotFound(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT * FROM social_groups WHERE id = ?") {
			return socialGroupRow(), nil
		}
		return nil, nil
	}
	f.execFn = func(q string, args []any) (store.ExecResult, error) {
		if strings.Contains(q, "DELETE FROM social_group_members") {
			return store.ExecResult{RowsAffected: 0}, nil
		}
		return store.ExecResult{LastInsertID: 1, RowsAffected: 1}, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_group_member_remove", `{"group_id":5,"user_id":7}`)
	if w.Body.String() != `{"code":1,"msg":"成员不存在","data":null}` {
		t.Fatalf("删除 0 行应报成员不存在: %s", w.Body.String())
	}
}

func TestAdminGroupDeleteDeletesMessagesFirst(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_group_delete", `{"id":5}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true}}` {
		t.Fatalf("admin_group_delete: %s", w.Body.String())
	}
	msgs := f.execs("DELETE FROM social_messages WHERE group_id = ?")
	groups := f.execs("DELETE FROM social_groups WHERE id = ?")
	if len(msgs) != 1 || len(groups) != 1 {
		t.Fatalf("应先删群消息再删群: msgs=%d groups=%d", len(msgs), len(groups))
	}
}

func TestAdminSocialMessageClearCountsFirst(t *testing.T) {
	f := newLotteryFake()
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_messages") {
			return int64(4), true, nil
		}
		return nil, false, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_social_message_clear", `{"group_id":0,"keyword":""}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"deleted":4}}` {
		t.Fatalf("admin_social_message_clear: %s", w.Body.String())
	}
	// 无过滤时 DELETE 无 WHERE（照抄 PHP 的全表语义）
	dels := f.execs("DELETE FROM social_messages")
	if len(dels) != 1 || strings.Contains(dels[0].Query, "WHERE") {
		t.Fatalf("无过滤应为全表 DELETE: %+v", dels)
	}
}

// ---------- ⑤ admin_notify_send 的收件人/范围语义 ----------

func TestAdminNotifySendAll(t *testing.T) {
	f := newLotteryFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "SELECT id FROM users WHERE is_active = 1") {
			return []store.Row{srow("id", 1), srow("id", 2), srow("id", 3)}, nil
		}
		return nil, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_notify_send", `{"title":"维护","content":"今晚维护","type":"system","target":"all"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true,"count":3}}` {
		t.Fatalf("target=all 应收件人条数=活跃用户数: %s", w.Body.String())
	}
	ins := f.execs("INSERT INTO notifications")
	if len(ins) != 3 {
		t.Fatalf("应为每个活跃用户插一条: %d", len(ins))
	}
	if phpStr(ins[0].Args[3]) != "system" {
		t.Fatalf("type=system 未透传: %+v", ins[0].Args)
	}
}

func TestAdminNotifySendSingleAndErrors(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT id FROM users WHERE id = ?") {
			if phpInt(args[0]) == 5 {
				return srow("id", 5), nil
			}
			return nil, nil
		}
		return nil, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_notify_send", `{"title":"t","content":"c","target":"5"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true,"count":1}}` {
		t.Fatalf("target=uid 应只发一人: %s", w.Body.String())
	}
	// target 缺失 → all 分支（假 DB 无活跃用户 → 0）
	w = adminCall(t, rt, "admin_notify_send", `{"title":"t","content":"c"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true,"count":0}}` {
		t.Fatalf("缺省 target=all: %s", w.Body.String())
	}
	// 用户不存在
	w = adminCall(t, rt, "admin_notify_send", `{"title":"t","content":"c","target":"99"}`)
	if w.Body.String() != `{"code":1,"msg":"用户不存在","data":null}` {
		t.Fatalf("单发不存在用户: %s", w.Body.String())
	}
	// 标题空
	w = adminCall(t, rt, "admin_notify_send", `{"title":"","content":"c"}`)
	if w.Body.String() != `{"code":1,"msg":"标题不能为空","data":null}` {
		t.Fatalf("空标题: %s", w.Body.String())
	}
	// 内容空
	w = adminCall(t, rt, "admin_notify_send", `{"title":"t","content":""}`)
	if w.Body.String() != `{"code":1,"msg":"内容不能为空","data":null}` {
		t.Fatalf("空内容: %s", w.Body.String())
	}
	// 标题超 50
	long := strings.Repeat("字", 51)
	w = adminCall(t, rt, "admin_notify_send", `{"title":"`+long+`","content":"c"}`)
	if w.Body.String() != `{"code":1,"msg":"标题不能超过 50 个字","data":null}` {
		t.Fatalf("标题超长: %s", w.Body.String())
	}
}

// ---------- ⑥ admin_user_save 的 0 / "" / null 三态 ----------

func TestAdminUserSaveTriState(t *testing.T) {
	old := srow("id", 7, "username", "alice", "nickname", "爱丽丝", "email", "a@b.com",
		"bio", "", "avatar", "", "role", "admin", "is_active", 1, "created_at", "2026-01-01 10:00:00")

	newFake := func() (*lotteryFake, *Router) {
		f := newLotteryFake()
		f.rowFn = func(q string, args []any) (store.Row, error) {
			if strings.Contains(q, "SELECT * FROM users WHERE id = ?") {
				return old, nil
			}
			return nil, nil
		}
		return f, newLotteryRouter(f)
	}
	lastUpdate := func(f *lotteryFake) fakeCall {
		var u fakeCall
		for _, c := range f.calls {
			if c.Method == "Exec" && strings.Contains(c.Query, "UPDATE users SET") {
				u = c
			}
		}
		return u
	}

	// ① is_active=0 显式（封禁）：active 写 0；role 未提交 → 沿用旧值 admin
	f, rt := newFake()
	if w := adminCall(t, rt, "admin_user_save", `{"id":7,"is_active":0}`); w.Code != 200 {
		t.Fatalf("封禁应成功: %s", w.Body.String())
	}
	u := lastUpdate(f)
	if phpInt(u.Args[4]) != 0 || phpStr(u.Args[3]) != "admin" {
		t.Fatalf("is_active=0 应写 0 且 role 沿用 admin: %+v", u.Args)
	}

	// ② role=null 显式提交：param() 的 isset 语义让它回落默认 'user'，且 has_param=true 不沿用旧值
	f, rt = newFake()
	_ = adminCall(t, rt, "admin_user_save", `{"id":7,"role":null}`)
	u = lastUpdate(f)
	if phpStr(u.Args[3]) != "user" {
		t.Fatalf("role:null 应被当成显式提交并取默认 user（PHP isset 语义）: %+v", u.Args)
	}

	// ③ username="" 显式提交：仍落回旧用户名（PHP `if ($username === '')`）
	f, rt = newFake()
	_ = adminCall(t, rt, "admin_user_save", `{"id":7,"username":""}`)
	u = lastUpdate(f)
	if phpStr(u.Args[0]) != "alice" {
		t.Fatalf("username='' 应沿用旧值 alice: %+v", u.Args)
	}

	// ④ 新建用户密码太短
	f, rt = newFake()
	w := adminCall(t, rt, "admin_user_save", `{"username":"newuser","password":"123"}`)
	if w.Body.String() != `{"code":1,"msg":"密码至少 6 位","data":null}` {
		t.Fatalf("新建短密码: %s", w.Body.String())
	}

	// ⑤ 改自己的角色/禁用自己
	f, rt = newFake()
	// self 是 admin id=1；old 返回 id=7，把 old.id 改成 1 触发自身保护
	oldSelf := srow("id", 1, "username", "root", "nickname", "管理员", "email", "",
		"bio", "", "avatar", "", "role", "admin", "is_active", 1)
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT * FROM users WHERE id = ?") {
			return oldSelf, nil
		}
		return nil, nil
	}
	w = adminCall(t, rt, "admin_user_save", `{"id":1,"is_active":0}`)
	if w.Body.String() != `{"code":1,"msg":"不能修改自己的角色, 也不能禁用自己的账号","data":null}` {
		t.Fatalf("自我禁用保护: %s", w.Body.String())
	}
}

func TestAdminUserDeleteGuards(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT role FROM users WHERE id = ?") {
			return srow("role", "admin"), nil
		}
		return nil, nil
	}
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "role = 'admin' AND is_active = 1") {
			return int64(1), true, nil
		}
		return nil, false, nil
	}
	rt := newLotteryRouter(f)
	// 删除自己
	w := adminCall(t, rt, "admin_user_delete", `{"id":1}`)
	if w.Body.String() != `{"code":1,"msg":"不能删除自己的账号","data":null}` {
		t.Fatalf("不能删自己: %s", w.Body.String())
	}
	// 至少保留一个管理员
	w = adminCall(t, rt, "admin_user_delete", `{"id":9}`)
	if w.Body.String() != `{"code":1,"msg":"至少保留一个管理员","data":null}` {
		t.Fatalf("至少保留一个管理员: %s", w.Body.String())
	}
}

// ---------- 标签：user_tags_str 与预设 ----------

func TestAdminUserTagsSetNormalizes(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT id FROM users WHERE id = ?") {
			return srow("id", 5), nil
		}
		return nil, nil
	}
	rt := newLotteryRouter(f)
	// 中文逗号/顿号分隔 + 去重 + 超 10 字丢弃
	w := adminCall(t, rt, "admin_user_tags_set", `{"user_id":5,"tags":"vip，活跃、vip、这个标签名字实在是太长了"}`)
	want := `{"code":0,"msg":"ok","data":{"user_id":5,"tags":["vip","活跃"]}}`
	if w.Body.String() != want {
		t.Fatalf("标签归一化不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// 空串 = 清空 → [] 且 UPDATE tags=''
	f2 := newLotteryFake()
	f2.rowFn = f.rowFn
	rt2 := newLotteryRouter(f2)
	w = adminCall(t, rt2, "admin_user_tags_set", `{"user_id":5,"tags":""}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"user_id":5,"tags":[]}}` {
		t.Fatalf("空标签应为 []: %s", w.Body.String())
	}
	upd := f2.execs("UPDATE users SET tags")
	if len(upd) != 1 || phpStr(upd[0].Args[0]) != "" {
		t.Fatalf("清空应写空串: %+v", upd)
	}
	// 用户不存在
	f3 := newLotteryFake()
	rt3 := newLotteryRouter(f3)
	w = adminCall(t, rt3, "admin_user_tags_set", `{"user_id":5,"tags":"a"}`)
	if w.Body.String() != `{"code":1,"msg":"用户不存在","data":null}` {
		t.Fatalf("不存在用户: %s", w.Body.String())
	}
}

func TestAdminTagsPresetsAndUsed(t *testing.T) {
	f := newLotteryFake()
	f.settings["user_tag_presets"] = `["vip","活跃"]`
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "FROM settings WHERE") {
			return f.settings[phpStr(args[0])], true, nil
		}
		return nil, false, nil
	}
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "SELECT tags FROM users") {
			return []store.Row{srow("tags", "vip,新人"), srow("tags", "vip")}, nil
		}
		return nil, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_tags", "")
	want := `{"code":0,"msg":"ok","data":{"presets":["vip","活跃"],"used":["vip","新人"]}}`
	if w.Body.String() != want {
		t.Fatalf("admin_tags 不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

func TestAdminTagPresetSaveAndDelete(t *testing.T) {
	f := newLotteryFake()
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "FROM settings WHERE") {
			v, ok := f.settings[phpStr(args[0])]
			return v, ok, nil
		}
		return nil, false, nil
	}
	rt := newLotteryRouter(f)
	// 假 DB 的 settings UPSERT：PHP 是 `INSERT INTO settings (`key`,`value`) ...`（settings 无引号）
	f.execFn = func(q string, args []any) (store.ExecResult, error) {
		if strings.Contains(q, "INSERT INTO settings") {
			f.settings[phpStr(args[0])] = phpStr(args[1])
		}
		return store.ExecResult{LastInsertID: 1, RowsAffected: 1}, nil
	}
	// 空标签
	w := adminCall(t, rt, "admin_tag_preset_save", `{"tag":""}`)
	if w.Body.String() != `{"code":1,"msg":"标签不能为空","data":null}` {
		t.Fatalf("空标签: %s", w.Body.String())
	}
	// 超 10 字
	w = adminCall(t, rt, "admin_tag_preset_save", `{"tag":"`+strings.Repeat("字", 11)+`"}`)
	if w.Body.String() != `{"code":1,"msg":"标签不能超过 10 个字","data":null}` {
		t.Fatalf("超长标签: %s", w.Body.String())
	}
	// 新增
	w = adminCall(t, rt, "admin_tag_preset_save", `{"tag":"vip"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"presets":["vip"]}}` {
		t.Fatalf("新增预设: %s", w.Body.String())
	}
	if got := f.settings["user_tag_presets"]; got != `["vip"]` {
		t.Fatalf("settings 应存 JSON 数组: %q", got)
	}
	// 已存在不重复
	w = adminCall(t, rt, "admin_tag_preset_save", `{"tag":"vip"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"presets":["vip"]}}` {
		t.Fatalf("重复标签不应新增: %s", w.Body.String())
	}
	// 删除
	w = adminCall(t, rt, "admin_tag_preset_delete", `{"tag":"vip"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"presets":[]}}` {
		t.Fatalf("删除预设: %s", w.Body.String())
	}
}

// ---------- 私聊/用户列表结构 ----------

func TestAdminPmMessagesLiteral(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "SELECT * FROM social_pms WHERE id = ?") {
			return srow("id", 3), nil
		}
		return nil, nil
	}
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_pm_messages") {
			return int64(1), true, nil
		}
		return nil, false, nil
	}
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		return []store.Row{srow(
			"id", 11, "conv_id", 3, "from_user", 1, "to_user", 7,
			"from_nickname", "管理员", "from_username", "root",
			"to_nickname", "", "to_username", "alice",
			"content", "你好", "image", "", "video", "", "msg_type", "",
			"is_recalled", 0, "created_at", "2026-01-01 10:00:00")}, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_pm_messages", `{"conv_id":3}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":11,"conv_id":3,"from_user":1,"to_user":7,"from_name":"管理员","to_name":"alice","content":"你好","image":"","msg_type":"","video":"","is_recalled":0,"created_at":"2026-01-01 10:00:00"}],"total":1,"page":1,"page_size":20}}`
	if w.Body.String() != want {
		t.Fatalf("admin_pm_messages 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// 会话不存在
	f2 := newLotteryFake()
	rt2 := newLotteryRouter(f2)
	w = adminCall(t, rt2, "admin_pm_messages", `{"conv_id":3}`)
	if w.Body.String() != `{"code":1,"msg":"会话不存在","data":null}` {
		t.Fatalf("会话不存在: %s", w.Body.String())
	}
}

func TestAdminPmClear(t *testing.T) {
	f := newLotteryFake()
	f.valFn = func(q string, args []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_pm_messages WHERE conv_id") {
			return int64(6), true, nil
		}
		return nil, false, nil
	}
	rt := newLotteryRouter(f)
	w := adminCall(t, rt, "admin_pm_clear", `{"conv_id":3}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"deleted":6}}` {
		t.Fatalf("admin_pm_clear: %s", w.Body.String())
	}
	if len(f.execs("UPDATE social_pms SET last_message_id = 0, last_at = NULL")) != 1 {
		t.Fatalf("应重置会话 last_message_id/last_at")
	}
	// 缺 conv_id
	w = adminCall(t, rt, "admin_pm_clear", `{}`)
	if w.Body.String() != `{"code":1,"msg":"请选择会话","data":null}` {
		t.Fatalf("请选择会话: %s", w.Body.String())
	}
}

// ---------- 路由注册：22 个在、禁迁的被排除 ----------

func TestAdminRouteRegistration(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	mine := []string{
		"admin_groups", "admin_group_save", "admin_group_delete", "admin_group_members",
		"admin_group_member_remove", "admin_social_messages", "admin_social_message_delete",
		"admin_social_message_clear", "admin_pm_conversations", "admin_pm_messages",
		"admin_pm_message_delete", "admin_pm_clear", "admin_notification_list",
		"admin_notification_delete", "admin_notify_send", "admin_users", "admin_user_save",
		"admin_user_delete", "admin_user_tags_set", "admin_tags", "admin_tag_preset_save",
		"admin_tag_preset_delete",
	}
	for _, a := range mine {
		if !strings.Contains(code, `case "`+a+`":`) {
			t.Fatalf("未注册: %s", a)
		}
	}
	forbidden := []string{
		// 5A 刻意透传的 6 个：本单与 5B-2 都不得注册（绝不迁移破坏性/全表 UPDATE）
		"admin_lottery_activity_reset", "admin_lottery_codes_delete", "admin_lottery_codes_import",
		"admin_lottery_prize_delete", "admin_lottery_quota_reset_all", "admin_lottery_quota_all",
	}
	for _, a := range forbidden {
		if strings.Contains(code, `case "`+a+`":`) {
			t.Fatalf("不该注册（越界）: %s", a)
		}
	}
}

func TestAdminGroupSaveInsertAndNameGuard(t *testing.T) {
	f := newLotteryFake()
	rt := newLotteryRouter(f)
	// 名字为空
	w := adminCall(t, rt, "admin_group_save", `{"name":""}`)
	if w.Body.String() != `{"code":1,"msg":"群组名称不能为空","data":null}` {
		t.Fatalf("空群名: %s", w.Body.String())
	}
	// 正常新建
	w = adminCall(t, rt, "admin_group_save", `{"name":"新群","description":"","sort_order":2,"is_active":1}`)
	if w.Code != 200 {
		t.Fatalf("新建群应成功: %d %s", w.Code, w.Body.String())
	}
	ins := f.execs("INSERT INTO social_groups")
	if len(ins) != 1 {
		t.Fatalf("应 INSERT 一次: %d", len(ins))
	}
	// 未显式提交 all_muted → 不应写 all_muted
	if len(f.execs("UPDATE social_groups SET all_muted")) != 0 {
		t.Fatalf("all_muted 未提交时不应写库")
	}
}
