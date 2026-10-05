package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 3：群聊（social_*）13 个 action + 2 个禁言管理 action 的原生实现。
//
// 逐条对齐 api.php:830-1427 与它用到的辅助函数（:3496-3638 / :3787-3830 /
// :4147-4194 / :4198-4227 / :4337-4369 / :3992-4002）。硬规矩：
//   - 报错文案、JSON code 与 HTTP 状态码（由 statusForCode 决定）逐字一致；
//   - JSON 字段名与顺序逐字一致（phpjson 有序对象）；
//   - 时间字段一律字符串 2006-01-02 15:04:05 / H:i，按 Asia/Shanghai；
//   - PHP 的怪癖一律照抄（详见各函数注释里的「照抄」标记）；
//   - SQL 里所有值走参数绑定，绝不拼用户输入（列表类 SQL 的 LIMIT/OFFSET 也绑定）。
//
// 刻意保留的 PHP 死代码（照抄不修，见各处的「PHP 死查询」注释）：
//   - social_group 里 SELECT COUNT(DISTINCT user_id) 的结果被 social_group_public 覆盖；
//   - social_messages 里的 at_names 预处理：at_names 不会出现在响应里。
// 这些查询在 PHP 里真的会执行（失败会抛异常变 500），保留它们是为了让两端
// 「发了哪些 SQL、失败在哪一步」也保持一致。

// ---------- 请求级 scope（PHP static 缓存的等价物） ----------

// columnProbe 是列探测能力（store.Store 天然满足）。做成接口是为了让单测能注入
// 「已跑过视频迁移」的假实现，从而同时覆盖有、无 video / msg_type 列两条 INSERT 分支。
type columnProbe interface {
	HasColumn(ctx context.Context, table, column string) bool
}

// socialScope 承载一次请求内的缓存，对应 PHP 里几个 static 缓存：
// social_member_count / social_is_member（api.php:3496 / :3511）以及被反复调用的
// current_user()。PHP 的 static 是「每请求」有效，Go 里按请求建实例语义相同；
// 绝不做跨请求缓存，否则退群/入群、改昵称都要重启才生效。
type socialScope struct {
	rt  *Router
	c   *Ctx
	ctx context.Context

	meRow    store.Row
	meLoaded bool
	meErr    error

	memberCounts map[int64]int64
	isMember     map[string]bool
}

func (rt *Router) newSocialScope(c *Ctx) *socialScope {
	return &socialScope{
		rt:           rt,
		c:            c,
		ctx:          c.R.Context(),
		memberCounts: map[int64]int64{},
		isMember:     map[string]bool{},
	}
}

func (s *socialScope) db() accountsDB { return s.rt.accounts() }

// hasColumn 对齐 api.php:3866 db_has_column()（结果由 store 做进程级缓存）。
func (rt *Router) hasColumn(c *Ctx, table, column string) bool {
	if rt.env.Cols != nil {
		return rt.env.Cols.HasColumn(c.R.Context(), table, column)
	}
	return false
}

func (s *socialScope) hasColumn(table, column string) bool {
	return s.rt.hasColumn(s.c, table, column)
}

// me 对齐 api.php:3348 current_user()：未登录返回 (nil, nil)；数据库异常返回 err。
func (s *socialScope) me() (store.Row, error) {
	if s.meLoaded {
		return s.meRow, s.meErr
	}
	s.meLoaded = true
	row, err := s.db().CurrentUserRow(s.ctx, s.c.Token())
	if err != nil {
		s.meErr = err
		return nil, err
	}
	s.meRow = row
	return row, nil
}

// ---------- 通用小工具 ----------

// phpDateTime 对齐 date('Y-m-d H:i:s')（Asia/Shanghai）。
func phpDateTime(t time.Time) string {
	return t.In(config.LocalZone()).Format("2006-01-02 15:04:05")
}

// phpDateTimeAt 对齐 date('Y-m-d H:i:s', $ts)。
func phpDateTimeAt(ts int64) string {
	return time.Unix(ts, 0).In(config.LocalZone()).Format("2006-01-02 15:04:05")
}

// phpTimeHM 对齐 date('H:i', $ts)。
func phpTimeHM(ts int64) string {
	return time.Unix(ts, 0).In(config.LocalZone()).Format("15:04")
}

// mbSubstr 复刻 mb_substr($s, $start, $length)（按字符计，不是字节）。
func mbSubstr(s string, start, length int) string {
	r := []rune(s)
	if start < 0 {
		start = 0
	}
	if start >= len(r) {
		return ""
	}
	end := start + length
	if end > len(r) {
		end = len(r)
	}
	return string(r[start:end])
}

// phpSplitAt 复刻 preg_split('/[,\s]+/', $s)：分隔符是「逗号」或「空白」
// （PCRE 非 u 模式下的 \s = 空格/\t/\n/\r/\f/\v）。
// 连续分隔符算一个；首尾分隔符各产生一个空段（PHP 也会返回空段）。
func phpSplitAt(s string) []string {
	isSep := func(b byte) bool {
		return b == ',' || b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
	}
	out := []string{}
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if isSep(s[i]) {
			out = append(out, cur.String())
			cur.Reset()
			for i+1 < len(s) && isSep(s[i+1]) {
				i++
			}
			continue
		}
		cur.WriteByte(s[i])
	}
	return append(out, cur.String())
}

// intvalAny 复刻 PHP 的 intval($v)：标量走 (int)，非空数组/对象算 1（PHP 的语义，
// JSON 里的人为嵌套数组就会走这条路径）。
func intvalAny(v any) int64 {
	switch x := v.(type) {
	case []any:
		if len(x) > 0 {
			return 1
		}
		return 0
	case map[string]any:
		if len(x) > 0 {
			return 1
		}
		return 0
	}
	return phpInt(v)
}

// parseAtParam 对齐 api.php:1112-1117 的 at 参数解析：
//
//	字符串  -> preg_split('/[,\s]+/')
//	数组    -> 逐元素 intval
//	其它    -> 空数组（注意：JSON 里传数字 5 会被 PHP 判成「非数组」而整个丢弃）
//
// 然后 array_map(intval) -> array_filter(>0 且 != 我) -> array_unique -> array_values。
func parseAtParam(c *Ctx, meID int64) []int64 {
	v, ok := c.Param("at")
	if !ok {
		return []int64{}
	}
	var raw []any
	if s, isStr := v.(string); isStr {
		for _, seg := range phpSplitAt(s) {
			raw = append(raw, seg)
		}
	} else if arr, isArr := v.([]any); isArr {
		raw = arr
	} else {
		return []int64{}
	}
	out := []int64{}
	seen := map[int64]bool{}
	for _, e := range raw {
		n := intvalAny(e)
		if n <= 0 || n == meID || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// splitInts 复刻 array_map('intval', explode(',', $s))：空串返回空数组，
// 尾随逗号会产生一个 0（PHP 的「@所有人」标记位就是这么来的）。
func splitInts(s string) []int64 {
	if s == "" {
		return []int64{}
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		out = append(out, atoiPrefix(p))
	}
	return out
}

func containsInt64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// phpNum 是 PHP (string)(int) 的等价物（strconv 只在内部用，输出形态一致）。
// 刻意不叫 itoa：shell_test.go 里已有一个 int 版的测试辅助函数。
func phpNum(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// userBrief 对齐 api.php:4303 user_brief()（字段顺序即 JSON 顺序）。
func userBrief(u store.Row) *phpjson.O {
	nick := rowStr(u, "nickname", "")
	if nick == "" {
		nick = rowStr(u, "username", "")
	}
	return phpjson.New().
		Set("id", rowInt(u, "id", 0)).
		Set("username", rowStr(u, "username", "")).
		Set("nickname", nick).
		Set("avatar", rowStr(u, "avatar", "")).
		Set("tags", userTagsArr(u))
}

// ---------- 群/消息结构 ----------

type unreadInfo struct {
	count   int64
	firstID int64
}

type mentionInfo struct {
	atMe       int64
	atMeFirst  int64
	atAll      int64
	atAllFirst int64
}

// socialMsgPublic 对齐 api.php:3604 social_msg_public()。
// 字段顺序：id, group_id, group_name, user_id, nickname, avatar, role, tags, msg_type,
// content, image, image_w, image_h, video, video_w, video_h, video_duration, video_size,
// at, quote_id, quote_nickname, quote_content, is_recalled, created_at, time_text。
func socialMsgPublic(m store.Row) *phpjson.O {
	recalled := rowInt(m, "is_recalled", 0) == 1
	at := []any{}
	if rowStr(m, "at_users", "") != "" {
		for _, v := range splitInts(rowStr(m, "at_users", "")) {
			at = append(at, v)
		}
	}
	ts, ok := phpStrtotime(rowStr(m, "created_at", ""))
	if !ok || ts == 0 {
		ts = nowUnix()
	}
	nick := rowStr(m, "nickname", "")
	if nick == "" {
		nick = "用户" + phpNum(rowInt(m, "user_id", 0))
	}
	str := func(v string) string {
		if recalled {
			return ""
		}
		return v
	}
	num := func(v int64) int64 {
		if recalled {
			return 0
		}
		return v
	}
	return phpjson.New().
		Set("id", rowInt(m, "id", 0)).
		Set("group_id", rowInt(m, "group_id", 0)).
		Set("group_name", rowStr(m, "group_name", "")).
		Set("user_id", rowInt(m, "user_id", 0)).
		Set("nickname", nick).
		Set("avatar", rowStr(m, "avatar", "")).
		Set("role", rowStr(m, "role", "user")).
		Set("tags", userTagsArr(m)).
		Set("msg_type", rowStr(m, "msg_type", "")).
		Set("content", str(rowStr(m, "content", ""))).
		Set("image", str(rowStr(m, "image", ""))).
		Set("image_w", num(rowInt(m, "image_w", 0))).
		Set("image_h", num(rowInt(m, "image_h", 0))).
		Set("video", str(rowStr(m, "video", ""))).
		Set("video_w", num(rowInt(m, "video_w", 0))).
		Set("video_h", num(rowInt(m, "video_h", 0))).
		Set("video_duration", num(rowInt(m, "video_duration", 0))).
		Set("video_size", num(rowInt(m, "video_size", 0))).
		Set("at", at).
		Set("quote_id", rowInt(m, "quote_id", 0)).
		Set("quote_nickname", str(rowStr(m, "quote_nickname", ""))).
		Set("quote_content", str(rowStr(m, "quote_content", ""))).
		Set("is_recalled", boolToInt(recalled)).
		Set("created_at", rowStr(m, "created_at", "")).
		Set("time_text", phpTimeHM(ts))
}

// groupLastMessage 对齐 api.php:3554-3566：把 group_last_messages 的一行转成
// last_message 结构（空行 -> null）。
func groupLastMessage(last store.Row) any {
	if len(last) == 0 {
		return nil
	}
	nick := rowStr(last, "nickname", "")
	if nick == "" {
		nick = rowStr(last, "username", "")
	}
	return phpjson.New().
		Set("id", rowInt(last, "id", 0)).
		Set("user_id", rowInt(last, "user_id", 0)).
		Set("nickname", nick).
		Set("content", rowStr(last, "content", "")).
		Set("image", rowStr(last, "image", "")).
		Set("msg_type", rowStr(last, "msg_type", "")).
		Set("video", rowStr(last, "video", "")).
		Set("video_w", rowInt(last, "video_w", 0)).
		Set("video_h", rowInt(last, "video_h", 0)).
		Set("video_duration", rowInt(last, "video_duration", 0)).
		Set("created_at", rowStr(last, "created_at", ""))
}

// groupPublic 对齐 api.php:3550 social_group_public()。
// 注意 member_count 走 social_member_count()（social_group_members 计数），
// 而 message_count 才是 social_groups 子查询给的那一列。
func (s *socialScope) groupPublic(g store.Row, muted bool, unread, firstUnreadID int64, last store.Row, mention mentionInfo, meID int64) *phpjson.O {
	gid := rowInt(g, "id", 0)
	lastArr := groupLastMessage(last)
	lastTime := ""
	if lastArr != nil {
		lastTime = rowStr(last, "created_at", "")
	}
	isMember := int64(0)
	if meID > 0 && s.isMemberRow(gid, meID) {
		isMember = 1
	}
	mutedFlag := int64(0)
	if muted {
		mutedFlag = 1
	}
	return phpjson.New().
		Set("muted", mutedFlag).
		Set("unread", unread).
		Set("first_unread_id", firstUnreadID).
		Set("last_message", lastArr).
		Set("last_time", lastTime).
		Set("at_me", mention.atMe).
		Set("at_me_first", mention.atMeFirst).
		Set("at_all", mention.atAll).
		Set("at_all_first", mention.atAllFirst).
		Set("id", gid).
		Set("name", rowStr(g, "name", "")).
		Set("icon", rowStr(g, "icon", "")).
		Set("description", rowStr(g, "description", "")).
		Set("notice", rowStr(g, "notice", "")).
		Set("member_count", s.memberCount(gid)).
		Set("is_member", isMember).
		Set("message_count", rowInt(g, "message_count", 0)).
		Set("sort_order", rowInt(g, "sort_order", 0)).
		Set("is_active", rowInt(g, "is_active", 1)).
		Set("all_muted", rowInt(g, "all_muted", 0)).
		Set("created_at", rowStr(g, "created_at", ""))
}

// memberCount 对齐 api.php:3496 social_member_count()：异常按 0 处理（不 500）。
func (s *socialScope) memberCount(gid int64) int64 {
	if v, ok := s.memberCounts[gid]; ok {
		return v
	}
	v, has, err := s.db().QueryValue(s.ctx, "SELECT COUNT(*) FROM social_group_members WHERE group_id = ?", gid)
	if err != nil {
		s.c.Log().Warn("统计群成员数失败, 按 0 处理（PHP 此处 catch）", "err", err, "gid", gid)
		s.memberCounts[gid] = 0
		return 0
	}
	n := intValue(v, has)
	s.memberCounts[gid] = n
	return n
}

// isMemberRow 对齐 api.php:3511 social_is_member()：异常按 0 处理。
func (s *socialScope) isMemberRow(gid, uid int64) bool {
	key := phpNum(gid) + ":" + phpNum(uid)
	if v, ok := s.isMember[key]; ok {
		return v
	}
	row, err := s.db().QueryRow(s.ctx,
		"SELECT 1 FROM social_group_members WHERE group_id = ? AND user_id = ? LIMIT 1", gid, uid)
	if err != nil {
		s.c.Log().Warn("查询群成员失败, 按非成员处理（PHP 此处 catch）", "err", err, "gid", gid, "uid", uid)
		s.isMember[key] = false
		return false
	}
	yes := row != nil
	s.isMember[key] = yes
	return yes
}

// displayName 对齐 api.php:3532 social_user_display_name()。
func displayName(u store.Row) string {
	nick := phpTrim(rowStr(u, "nickname", ""))
	if nick != "" {
		return nick
	}
	name := rowStr(u, "username", "")
	if name != "" {
		return name
	}
	return "用户" + phpNum(rowInt(u, "id", 0))
}

// systemMessage 对齐 api.php:3541 social_system_message()（msg_type='system'、at_users=NULL）。
func (s *socialScope) systemMessage(gid, uid int64, content string) (int64, error) {
	res, err := s.db().Exec(s.ctx,
		"INSERT INTO social_messages\n"+
			"            (group_id, user_id, content, image, image_w, image_h, at_users, quote_id, is_recalled, msg_type)\n"+
			"            VALUES (?, ?, ?, '', 0, 0, NULL, 0, 0, 'system')", gid, uid, content)
	if err != nil {
		return 0, err
	}
	return res.LastInsertID, nil
}

// groupOr404 对齐 api.php:3594 social_group_or_404()：失败时已经写出错误响应。
func (s *socialScope) groupOr404(gid int64, allowInactive bool) (store.Row, bool) {
	row, err := s.db().QueryRow(s.ctx, "SELECT * FROM social_groups WHERE id = ?", gid)
	if err != nil {
		s.c.dbError(err)
		return nil, false
	}
	if row == nil || (!allowInactive && rowInt(row, "is_active", 0) != 1) {
		s.c.Error("群组不存在或已停用", 1)
		return nil, false
	}
	return row, true
}

// myMutedIDs 对齐 api.php:4157 my_muted_ids()（未登录/异常 -> 空集合）。
func (s *socialScope) myMutedIDs(meID int64) map[int64]bool {
	out := map[int64]bool{}
	if meID <= 0 {
		return out
	}
	rows, err := s.db().QueryAll(s.ctx, "SELECT group_id FROM social_mutes WHERE user_id = ?", meID)
	if err != nil {
		s.c.Log().Warn("查询免打扰群失败（PHP 此处 catch）", "err", err)
		return out
	}
	for _, r := range rows {
		out[rowInt(r, "group_id", 0)] = true
	}
	return out
}

// muteUserIDs 对齐 api.php:4337 mute_user_ids()：注意是 social_mutes（免打扰），
// 不是 social_user_mutes（管理员禁言）。
func (s *socialScope) muteUserIDs(gid int64) map[int64]bool {
	out := map[int64]bool{}
	rows, err := s.db().QueryAll(s.ctx, "SELECT user_id FROM social_mutes WHERE group_id = ?", gid)
	if err != nil {
		s.c.Log().Warn("查询群免打扰用户失败（PHP 此处 catch）", "err", err)
		return out
	}
	for _, r := range rows {
		out[rowInt(r, "user_id", 0)] = true
	}
	return out
}

// myUnreadMap 对齐 api.php:4314 my_unread_map()。
func (s *socialScope) myUnreadMap(meID int64) map[int64]unreadInfo {
	out := map[int64]unreadInfo{}
	if meID <= 0 {
		return out
	}
	rows, err := s.db().QueryAll(s.ctx,
		`SELECT m.group_id, COUNT(*) AS c, MIN(m.id) AS first_id
                FROM social_messages m
                LEFT JOIN social_reads r ON r.group_id = m.group_id AND r.user_id = ?
                WHERE m.is_recalled = 0 AND m.user_id <> ? AND m.id > COALESCE(r.last_read_id, 0)
                      AND (m.msg_type IS NULL OR m.msg_type <> ?)
                GROUP BY m.group_id`, meID, meID, "system")
	if err != nil {
		s.c.Log().Warn("统计群未读失败（PHP 此处 catch）", "err", err)
		return out
	}
	for _, r := range rows {
		out[rowInt(r, "group_id", 0)] = unreadInfo{
			count:   rowInt(r, "c", 0),
			firstID: rowInt(r, "first_id", 0),
		}
	}
	return out
}

// myMentionMap 对齐 api.php:4198 my_mention_map()。
//
// 照抄的 PHP 怪癖：at_users 是按逗号 explode 后逐段 intval，
// 因此**尾随逗号会产生一个 0**，而 0 就是「@所有人」的标记位
// （social_send 存 at_users 用 0 表示所有人）—— 于是 "7," 会被当成 @所有人。
// 这条怪癖在 SSE 渲染（realtime 包 renderGroup）里也同样存在，不许「顺手修好」。
func (s *socialScope) myMentionMap(meID int64) map[int64]mentionInfo {
	out := map[int64]mentionInfo{}
	if meID <= 0 {
		return out
	}
	rows, err := s.db().QueryAll(s.ctx,
		`SELECT m.id, m.group_id, m.at_users
            FROM social_messages m
            LEFT JOIN social_reads r ON r.group_id = m.group_id AND r.user_id = ?
            WHERE m.is_recalled = 0 AND m.user_id <> ? AND m.at_users <> ?
              AND m.id > COALESCE(r.last_read_id, 0)
            ORDER BY m.id ASC`, meID, meID, "")
	if err != nil {
		s.c.Log().Warn("统计 @我 失败（PHP 此处 catch）", "err", err)
		return out
	}
	for _, r := range rows {
		ids := splitInts(rowStr(r, "at_users", ""))
		gid := rowInt(r, "group_id", 0)
		cur := out[gid]
		if containsInt64(ids, 0) {
			cur.atAll++
			if cur.atAllFirst == 0 {
				cur.atAllFirst = rowInt(r, "id", 0)
			}
		}
		if containsInt64(ids, meID) {
			cur.atMe++
			if cur.atMeFirst == 0 {
				cur.atMeFirst = rowInt(r, "id", 0)
			}
		}
		out[gid] = cur
	}
	return out
}

// groupLastMessages 对齐 api.php:4175 group_last_messages()（异常向上抛，PHP 里也是不 catch 的）。
func (s *socialScope) groupLastMessages(gids []int64) (map[int64]store.Row, error) {
	out := map[int64]store.Row{}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, g := range gids {
		if g == 0 || seen[g] {
			continue
		}
		seen[g] = true
		ids = append(ids, g)
	}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	vsel := ""
	if s.hasColumn("social_messages", "video") {
		vsel = ", m.video, m.video_w, m.video_h, m.video_duration, m.video_size"
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db().QueryAll(s.ctx,
		"SELECT m.id, m.group_id, m.user_id, m.content, m.image, m.msg_type"+vsel+", m.created_at,\n"+
			"                u.nickname, u.username\n"+
			"            FROM social_messages m LEFT JOIN users u ON u.id = m.user_id\n"+
			"            WHERE m.is_recalled = 0\n"+
			"              AND m.id = (SELECT MAX(x.id) FROM social_messages x WHERE x.group_id = m.group_id AND x.is_recalled = 0)\n"+
			"              AND m.group_id IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[rowInt(r, "group_id", 0)] = r
	}
	return out, nil
}

// notifyPushLink 对齐 api.php:3801 notify_push()（带 link 版本；插入失败只记日志）。
func (rt *Router) notifyPushLink(c *Ctx, uid int64, title, content, typ, link string) {
	if uid <= 0 {
		return
	}
	_, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)",
		uid, title, content, typ, link)
	if err != nil {
		c.Log().Error("notify_push 失败（PHP 此处忽略异常）", "err", err)
	}
}

// notifyMerge 对齐 api.php:4352 notify_merge()：同一用户同 type+title 的未读通知只更新，不堆新的。
func (rt *Router) notifyMerge(c *Ctx, uid int64, title, content, typ, link string) {
	if uid <= 0 {
		return
	}
	v, ok, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT id FROM notifications WHERE user_id = ? AND type = ? AND title = ? AND is_read = 0 ORDER BY id DESC LIMIT 1",
		uid, typ, title)
	if err != nil {
		c.Log().Error("notify_merge 查询失败（PHP 此处忽略异常）", "err", err)
		return
	}
	id := intValue(v, ok)
	if id > 0 {
		if _, err := rt.accounts().Exec(c.R.Context(),
			"UPDATE notifications SET content = ?, link = ?, created_at = NOW() WHERE id = ?",
			content, link, id); err != nil {
			c.Log().Error("notify_merge 更新失败（PHP 此处忽略异常）", "err", err)
		}
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)",
		uid, title, content, typ, link); err != nil {
		c.Log().Error("notify_merge 插入失败（PHP 此处忽略异常）", "err", err)
	}
}

// videoMetaParams 对齐 api.php:3992 video_meta_params()：越界即报错。
// social_send 里**无条件**调用它（纯文字消息带了越界的 video_* 参数也会报错）——照抄。
func videoMetaParams(c *Ctx) (w, h, d, size int64, ok bool) {
	w = c.ParamInt("video_w", 0)
	h = c.ParamInt("video_h", 0)
	d = c.ParamInt("video_duration", 0)
	size = c.ParamInt("video_size", 0)
	if w < 0 || w > 10000 || h < 0 || h > 10000 {
		c.Error("视频尺寸参数不合法", 1)
		return 0, 0, 0, 0, false
	}
	if d < 0 || d > 3600 {
		c.Error("视频时长参数不合法 (最长 60 分钟)", 1)
		return 0, 0, 0, 0, false
	}
	if size < 0 || size > 2147483647 {
		c.Error("视频大小参数不合法", 1)
		return 0, 0, 0, 0, false
	}
	return w, h, d, size, true
}

// requireAdminRow 对齐 api.php:3263 require_admin()，但走可注入的数据访问层（便于单测）。
// 与 native_upload.go 里 requireAdmin 的差别：token 的「空」判定用 PHP 的 falsy 语义
// （PHP 写的是 if (!$token)，因此 token="0" 也算未登录）。
func (rt *Router) requireAdminRow(c *Ctx) (store.Row, bool) {
	if phpFalsy(c.Token()) {
		c.Error("未登录", 401)
		return nil, false
	}
	me, err := rt.accounts().CurrentUserRow(c.R.Context(), c.Token())
	if err != nil {
		c.dbError(err)
		return nil, false
	}
	if me == nil {
		c.Error("登录已失效", 401)
		return nil, false
	}
	if rowStr(me, "role", "") != "admin" {
		c.Error("没有权限, 仅管理员可操作", 403)
		return nil, false
	}
	return me, true
}

// videoConfigSource 是视频配置读取能力（store.Store 天然满足）。做成接口是为了让
// 单测能注入「视频功能已关闭」的配置，覆盖 social_send 的对应分支。
type videoConfigSource interface {
	VideoConfig(ctx context.Context) store.VideoConfig
}

// socialVideoConfig 读视频配置（settings.video_config；异常回落默认值，与 PHP 的 try/catch 同构）。
func (rt *Router) socialVideoConfig(c *Ctx) store.VideoConfig {
	if rt.env.VideoCfg != nil {
		return rt.env.VideoCfg.VideoConfig(c.R.Context())
	}
	return store.VideoConfigDefault()
}

// reverseRows 对齐 array_reverse($rows)（这里只需要整行倒序）。
func reverseRows(in []store.Row) []store.Row {
	out := make([]store.Row, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}

// socialMsgSelect 是分页查询的固定前缀（与 PHP 逐字相同）。
const socialMsgSelect = `SELECT m.*, u.nickname, u.username, u.avatar, u.role, u.tags
                    FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
                    WHERE m.group_id = ?`

// ---------- social_groups (api.php:830) ----------

func (rt *Router) handleSocialGroups(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_groups")
	s := rt.newSocialScope(c)

	rows, err := s.db().QueryAll(s.ctx, `SELECT g.*,
                    (SELECT COUNT(DISTINCT m.user_id) FROM social_messages m WHERE m.group_id = g.id) AS member_count,
                    (SELECT COUNT(*) FROM social_messages m WHERE m.group_id = g.id AND m.is_recalled = 0) AS message_count
                FROM social_groups g WHERE g.is_active = 1 ORDER BY g.sort_order ASC, g.id ASC`)
	if err != nil {
		c.dbError(err)
		return
	}
	me, err := s.me()
	if err != nil {
		c.dbError(err)
		return
	}
	meID := int64(0)
	if me != nil {
		meID = rowInt(me, "id", 0)
	}
	muted := s.myMutedIDs(meID)
	unread := s.myUnreadMap(meID)
	mentions := s.myMentionMap(meID)
	gids := make([]int64, 0, len(rows))
	for _, g := range rows {
		gids = append(gids, rowInt(g, "id", 0))
	}
	lasts, err := s.groupLastMessages(gids)
	if err != nil {
		c.dbError(err)
		return
	}
	list := make([]any, 0, len(rows))
	for _, g := range rows {
		gid := rowInt(g, "id", 0)
		u := unread[gid]
		list = append(list, s.groupPublic(g, muted[gid], u.count, u.firstID, lasts[gid], mentions[gid], meID))
	}
	c.JSON(phpjson.New().Set("list", list), 0, "ok", 0)
}

// ---------- social_group (api.php:851) ----------

func (rt *Router) handleSocialGroup(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group")
	s := rt.newSocialScope(c)

	gid := c.ParamInt("id", 0)
	g, ok := s.groupOr404(gid, false)
	if !ok {
		return
	}
	admins, err := s.db().QueryAll(s.ctx,
		"SELECT DISTINCT u.id, u.nickname, u.username FROM social_messages m INNER JOIN users u ON u.id = m.user_id WHERE m.group_id = ? AND u.role = 'admin'",
		gid)
	if err != nil {
		c.dbError(err)
		return
	}
	// PHP 死查询（api.php:856）：结果写进 $g['member_count']，但 social_group_public
	// 用的是 social_member_count($g['id'])，这个值根本不会出现在响应里。
	// 保留它是因为 PHP 真的会执行（失败会 500），两端「发了哪些 SQL」保持一致。
	if _, _, err := s.db().QueryValue(s.ctx,
		"SELECT COUNT(DISTINCT user_id) AS c FROM social_messages WHERE group_id = ?", gid); err != nil {
		c.dbError(err)
		return
	}
	me, err := s.me()
	if err != nil {
		c.dbError(err)
		return
	}
	meID := int64(0)
	if me != nil {
		meID = rowInt(me, "id", 0)
	}
	muted := s.myMutedIDs(meID)
	unread := s.myUnreadMap(meID)
	mentions := s.myMentionMap(meID)
	lasts, err := s.groupLastMessages([]int64{gid})
	if err != nil {
		c.dbError(err)
		return
	}
	u := unread[gid]
	adminList := make([]any, 0, len(admins))
	for _, a := range admins {
		adminList = append(adminList, phpjson.New().
			Set("id", rowRaw(a, "id")).
			Set("nickname", rowRaw(a, "nickname")).
			Set("username", rowRaw(a, "username")))
	}
	c.JSON(phpjson.New().
		Set("group", s.groupPublic(g, muted[gid], u.count, u.firstID, lasts[gid], mentions[gid], meID)).
		Set("notice", rowStr(g, "notice", "")).
		Set("admins", adminList), 0, "ok", 0)
}

// ---------- social_messages (api.php:875) ----------

func (rt *Router) handleSocialMessages(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_messages")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	after := c.ParamInt("after_id", 0)
	before := c.ParamInt("before_id", 0)
	around := c.ParamInt("around_id", 0)
	limit := c.ParamInt("limit", 30)
	if limit > 50 {
		limit = 50
	}
	if limit < 1 {
		limit = 1
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	var rows []store.Row
	switch {
	case around > 0:
		// 从通知点进来时用: 目标消息放中间, 前后各取一半。
		half := limit / 2
		if half < 5 {
			half = 5
		}
		older, e := s.db().QueryAll(s.ctx, socialMsgSelect+" AND m.id <= ? ORDER BY m.id DESC LIMIT ?", gid, around, half)
		if e != nil {
			c.dbError(e)
			return
		}
		newer, e := s.db().QueryAll(s.ctx, socialMsgSelect+" AND m.id > ? ORDER BY m.id ASC LIMIT ?", gid, around, half)
		if e != nil {
			c.dbError(e)
			return
		}
		rows = append(reverseRows(older), newer...)
	case before > 0:
		older, e := s.db().QueryAll(s.ctx, socialMsgSelect+" AND m.id < ? ORDER BY m.id DESC LIMIT ?", gid, before, limit)
		if e != nil {
			c.dbError(e)
			return
		}
		rows = reverseRows(older)
	case after > 0:
		later, e := s.db().QueryAll(s.ctx, socialMsgSelect+" AND m.id > ? ORDER BY m.id ASC LIMIT ?", gid, after, limit)
		if e != nil {
			c.dbError(e)
			return
		}
		rows = later
	default:
		last, e := s.db().QueryAll(s.ctx, socialMsgSelect+" ORDER BY m.id DESC LIMIT ?", gid, limit)
		if e != nil {
			c.dbError(e)
			return
		}
		rows = reverseRows(last)
	}

	if len(rows) > 0 {
		ids := []int64{}
		for _, row := range rows {
			ids = append(ids, rowInt(row, "user_id", 0))
		}
		uniq := []int64{}
		seen := map[int64]bool{}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(uniq)), ",")
		args := make([]any, 0, len(uniq))
		for _, id := range uniq {
			args = append(args, id)
		}
		// PHP 死查询（api.php:917-922）：查出来的 at_names 挂到每条消息上，
		// 但 social_msg_public 不输出这个键。照抄保留（PHP 里失败会 500）。
		if _, e := s.db().QueryAll(s.ctx,
			"SELECT id, nickname, username FROM users WHERE id IN ("+ph+")", args...); e != nil {
			c.dbError(e)
			return
		}

		// 引用回复：一次性把被引用的原消息查出来（避免 N+1）。
		qids := []int64{}
		qseen := map[int64]bool{}
		for _, row := range rows {
			q := rowInt(row, "quote_id", 0)
			if q > 0 && !qseen[q] {
				qseen[q] = true
				qids = append(qids, q)
			}
		}
		if len(qids) > 0 {
			ph2 := strings.TrimSuffix(strings.Repeat("?,", len(qids)), ",")
			qargs := make([]any, 0, len(qids))
			for _, q := range qids {
				qargs = append(qargs, q)
			}
			qrows, e := s.db().QueryAll(s.ctx, "SELECT m.id, m.content, m.user_id, u.nickname\n"+
				"                                         FROM social_messages m LEFT JOIN users u ON u.id = m.user_id\n"+
				"                                         WHERE m.id IN ("+ph2+")", qargs...)
			if e != nil {
				c.dbError(e)
				return
			}
			type quoteVal struct{ nick, content string }
			qmap := map[int64]quoteVal{}
			for _, q := range qrows {
				qn := rowStr(q, "nickname", "")
				if qn == "" {
					qn = "用户" + phpNum(rowInt(q, "user_id", 0))
				}
				qmap[rowInt(q, "id", 0)] = quoteVal{nick: qn, content: rowStr(q, "content", "")}
			}
			for i, row := range rows {
				q := rowInt(row, "quote_id", 0)
				if q > 0 {
					if v, hit := qmap[q]; hit {
						rows[i]["quote_nickname"] = v.nick
						rows[i]["quote_content"] = v.content
					}
				}
			}
		}
	}

	meID := rowInt(me, "id", 0)
	unread := s.myUnreadMap(meID)
	firstID := int64(0)
	if len(rows) > 0 {
		firstID = rowInt(rows[0], "id", 0)
	}
	// 是否还有更早的消息：客户端上滑到顶时用它决定还要不要继续请求。
	hasMoreBefore := false
	if firstID > 0 {
		v, has, e := s.db().QueryValue(s.ctx,
			"SELECT COUNT(*) FROM social_messages WHERE group_id = ? AND id < ?", gid, firstID)
		if e != nil {
			c.dbError(e)
			return
		}
		hasMoreBefore = intValue(v, has) > 0
	}
	u := unread[gid]
	list := make([]any, 0, len(rows))
	for _, row := range rows {
		list = append(list, socialMsgPublic(row))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("has_more", int64(len(rows)) >= limit).
		Set("has_more_before", hasMoreBefore).
		Set("unread", u.count).
		Set("first_unread_id", u.firstID).
		Set("my_id", meID), 0, "ok", 0)
}

// ---------- social_read (api.php:971) ----------

func (rt *Router) handleSocialRead(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_read")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	lastID := c.ParamInt("last_id", 0)
	if lastID <= 0 {
		v, has, err := s.db().QueryValue(s.ctx, "SELECT COALESCE(MAX(id), 0) FROM social_messages WHERE group_id = ?", gid)
		if err != nil {
			c.dbError(err)
			return
		}
		lastID = intValue(v, has)
	}
	if _, err := s.db().Exec(s.ctx,
		`INSERT INTO social_reads (user_id, group_id, last_read_id, updated_at)
                           VALUES (?, ?, ?, NOW())
                           ON DUPLICATE KEY UPDATE last_read_id = GREATEST(last_read_id, VALUES(last_read_id)), updated_at = NOW()`,
		meID, gid, lastID); err != nil {
		c.dbError(err)
		return
	}
	// 回读真实已读位置（GREATEST 保证不会回退，传更小的 last_id 时库里仍是较大的值）。
	v, has, err := s.db().QueryValue(s.ctx,
		"SELECT last_read_id FROM social_reads WHERE user_id = ? AND group_id = ?", meID, gid)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("ok", true).
		Set("last_read_id", intValue(v, has)), 0, "ok", 0)
}

// ---------- social_send (api.php:1066) ----------

func (rt *Router) handleSocialSend(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_send")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	if rowInt(me, "is_active", 0) != 1 {
		c.Error("你已被封禁", 403)
		return
	}
	role := rowStr(me, "role", "")
	isAdmin := role == "admin"

	gid := c.ParamInt("group_id", 0)
	g, ok := s.groupOr404(gid, false)
	if !ok {
		return
	}
	// 必须先加入群聊才能发言：非管理员且不是群成员 -> 403（放在禁言判定之前，契约 A2）。
	if !isAdmin && !s.isMemberRow(gid, meID) {
		c.Error("请先加入群聊再发言", 403)
		return
	}
	// 全员禁言：非管理员一律拦截（管理员不受限）。
	if rowInt(g, "all_muted", 0) == 1 && !isAdmin {
		c.Error("群主已开启全体禁言, 暂时不能发言", 403)
		return
	}
	// 管理员禁言：群里 / 全站被禁言的用户不能发言（管理员不受限）。
	if !isAdmin {
		muteState, hasMute := rt.userMuteState(c, meID, gid)
		if hasMute {
			leftTxt := muteLeftText(muteState)
			why := phpTrim(rowStr(muteState, "reason", ""))
			msg := "你已被禁言"
			if leftTxt != "" {
				msg += " (" + leftTxt + ")"
			}
			if why != "" {
				msg += ", 原因: " + why
			}
			c.Error(msg, 403)
			return
		}
	}

	content := phpTrim(c.ParamStr("content", ""))
	image := phpTrim(c.ParamStr("image", ""))
	// 地址前缀强校验：只认本站对象存储 /chat/ 目录。
	if image != "" && !strings.HasPrefix(image, c.Env.Cfg.S3PublicURL+"/chat/") {
		c.Error("图片地址不合法", 1)
		return
	}
	imgW := c.ParamInt("image_w", 0)
	if imgW < 0 {
		imgW = 0
	}
	imgH := c.ParamInt("image_h", 0)
	if imgH < 0 {
		imgH = 0
	}
	video := phpTrim(c.ParamStr("video", ""))
	if video != "" && !strings.HasPrefix(video, c.Env.Cfg.S3PublicURL+"/chat/") {
		c.Error("视频地址不合法", 1)
		return
	}
	// 视频消息功能关闭时不能再发出视频消息。
	if video != "" && rt.socialVideoConfig(c).Enabled != 1 {
		c.Error("视频消息功能未开启", 1)
		return
	}
	// 无条件校验 video_* 参数（PHP 也是这样：纯文字消息带越界参数同样报错）。
	vidW, vidH, vidDur, vidSize, metaOK := videoMetaParams(c)
	if !metaOK {
		return
	}
	hasVideoCol := s.hasColumn("social_messages", "video")
	if video != "" && !hasVideoCol {
		c.Error("服务端未完成视频迁移, 请联系管理员", 1)
		return
	}
	msgType := ""
	if video != "" {
		msgType = "video"
	}
	if content == "" && image == "" && video == "" {
		c.Error("消息内容不能为空", 1)
		return
	}
	if mbStrlen(content) > 500 {
		c.Error("消息不能超过 500 个字", 1)
		return
	}
	// 图片/视频消息在通知里统一显示 [图片] / [视频]。
	notifyText := ""
	if content != "" {
		notifyText = mbSubstr(content, 0, 80)
	} else if video != "" {
		notifyText = "[视频]"
	} else {
		notifyText = "[图片]"
	}

	// 限流：查我自己在该群最后一条消息的 created_at（2 秒/群）。
	// DB 是唯一真源；这里刻意不走 Redis（键 fls:rl:social_send:<uid> 不含 group_id，
	// 用它做前置拒绝会把「2 秒内在别的群发过」误判成限流，与 PHP 行为不一致）。
	lastV, hasLast, err := s.db().QueryValue(s.ctx,
		"SELECT created_at FROM social_messages WHERE user_id = ? AND group_id = ? ORDER BY id DESC LIMIT 1",
		meID, gid)
	if err != nil {
		c.dbError(err)
		return
	}
	if hasLast {
		lastStr := phpStr(lastV)
		if !phpFalsy(lastStr) {
			ts, okParse := phpStrtotime(lastStr)
			if okParse && ts > nowUnix()-2 {
				c.Error("发送太快了, 请稍后再试", 1)
				return
			}
		}
	}

	at := parseAtParam(c, meID)
	// @所有人：只有管理员能发，可以由 at_all=1 指定，也可以在内容里写 @所有人。
	atAll := isAdmin && (intvalAny(c.paramAny("at_all")) == 1 || strings.Contains(content, "@所有人"))
	// at_users 里用 0 作为「所有人」的标记位（真实用户 id 都 > 0）。
	atStore := []int64{}
	if atAll {
		atStore = append(atStore, 0)
	}
	atStore = append(atStore, at...)
	atParts := make([]string, 0, len(atStore))
	for _, v := range atStore {
		atParts = append(atParts, phpNum(v))
	}
	atStr := strings.Join(atParts, ",")

	// 引用回复：只接受同群存在的消息，否则静默置 0。
	quoteID := c.ParamInt("quote_id", 0)
	if quoteID > 0 {
		row, err := s.db().QueryRow(s.ctx, "SELECT id FROM social_messages WHERE id = ? AND group_id = ?", quoteID, gid)
		if err != nil {
			c.dbError(err)
			return
		}
		if row == nil {
			quoteID = 0
		}
	}

	// 列先探测再拼 INSERT：迁移没跑时自动退回旧的列组合。
	scols := []string{"group_id", "user_id", "content", "image", "image_w", "image_h", "at_users", "quote_id"}
	svals := []any{gid, meID, content, image, imgW, imgH, atStr, quoteID}
	if hasVideoCol {
		scols = append(scols, "video", "video_w", "video_h", "video_duration", "video_size")
		svals = append(svals, video, vidW, vidH, vidDur, vidSize)
	}
	if s.hasColumn("social_messages", "msg_type") {
		scols = append(scols, "msg_type")
		svals = append(svals, msgType)
	}
	scols = append(scols, "is_recalled")
	svals = append(svals, 0)
	ph := strings.TrimSuffix(strings.Repeat("?,", len(scols)), ",")
	res, err := s.db().Exec(s.ctx,
		"INSERT INTO social_messages ("+strings.Join(scols, ", ")+") VALUES ("+ph+")", svals...)
	if err != nil {
		c.dbError(err)
		return
	}
	mid := res.LastInsertID
	gname := rowStr(g, "name", "")
	link := "msg:" + phpNum(gid) + ":" + phpNum(mid)

	// 通知三兄弟（顺序与条件必须与 PHP 一致：双跑脚本要逐条断言 notifications）。
	if atAll {
		rows, err := s.db().QueryAll(s.ctx, "SELECT id FROM users WHERE is_active = 1 AND id <> ?", meID)
		if err != nil {
			c.dbError(err)
			return
		}
		for _, row := range rows {
			rt.notifyPushLink(c, rowInt(row, "id", 0), "「"+gname+"」有人 @了所有人", notifyText, "social", "")
		}
	} else if len(at) > 0 {
		ph2 := strings.TrimSuffix(strings.Repeat("?,", len(at)), ",")
		args := make([]any, 0, len(at))
		for _, v := range at {
			args = append(args, v)
		}
		rows, err := s.db().QueryAll(s.ctx, "SELECT id FROM users WHERE id IN ("+ph2+") AND is_active = 1", args...)
		if err != nil {
			c.dbError(err)
			return
		}
		for _, row := range rows {
			// link 里带上群 id 与消息 id，客户端点通知能直接跳进群并定位到这条消息。
			rt.notifyPushLink(c, rowInt(row, "id", 0), "有人在「"+gname+"」@了你", notifyText, "social", link)
		}
	}
	// 普通群消息提醒：给群内其他成员推一条「「X」新消息」（同一未读合并，不刷屏）。
	// 开过免打扰的跳过；已经被 @ 单独提醒过的人不再重复推；@所有人 时上面已推过，这里不再推。
	if !atAll {
		atSet := map[int64]bool{}
		for _, aid := range at {
			atSet[aid] = true
		}
		mutedIDs := s.muteUserIDs(gid)
		senderName := rowStr(me, "nickname", "")
		if senderName == "" {
			senderName = rowStr(me, "username", "")
		}
		brief := ""
		if content != "" {
			brief = mbSubstr(content, 0, 60)
		} else if video != "" {
			brief = "[视频]"
		} else {
			brief = "[图片]"
		}
		rows, err := s.db().QueryAll(s.ctx, "SELECT id FROM users WHERE is_active = 1 AND id <> ?", meID)
		if err != nil {
			c.dbError(err)
			return
		}
		for _, row := range rows {
			uid := rowInt(row, "id", 0)
			if atSet[uid] || mutedIDs[uid] {
				continue
			}
			rt.notifyMerge(c, uid, "「"+gname+"」新消息", senderName+": "+brief, "social", link)
		}
	}
	c.JSON(phpjson.New().
		Set("id", mid).
		Set("at_all", boolToInt(atAll)).
		Set("quote_id", quoteID).
		Set("created_at", phpDateTime(time.Now())), 0, "ok", 0)
}

// ---------- social_recall (api.php:1189) ----------

func (rt *Router) handleSocialRecall(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_recall")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	id := c.ParamInt("id", 0)
	m, err := s.db().QueryRow(s.ctx, "SELECT * FROM social_messages WHERE id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	if m == nil {
		c.Error("消息不存在", 1)
		return
	}
	if rowInt(m, "is_recalled", 0) == 1 {
		c.Error("消息已经撤回了", 1)
		return
	}
	isAdmin := rowStr(me, "role", "") == "admin"
	mine := rowInt(m, "user_id", 0) == rowInt(me, "id", 0)
	if !isAdmin && !mine {
		c.Error("没有权限撤回这条消息", 403)
		return
	}
	if !isAdmin {
		ts, okParse := phpStrtotime(rowStr(m, "created_at", ""))
		if !okParse {
			ts = 0
		}
		if ts < nowUnix()-300 {
			c.Error("只能撤回 5 分钟内的消息", 1)
			return
		}
	}
	if _, err := s.db().Exec(s.ctx,
		"UPDATE social_messages SET is_recalled = 1, recalled_by = ?, recalled_at = NOW() WHERE id = ?",
		rowInt(me, "id", 0), id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true), 0, "ok", 0)
}

// ---------- social_group_notice_set (api.php:1205) ----------

func (rt *Router) handleSocialGroupNoticeSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group_notice_set")
	s := rt.newSocialScope(c)

	admin, ok := rt.requireAdminRow(c)
	if !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	g, ok := s.groupOr404(gid, true)
	if !ok {
		return
	}
	notice := phpTrim(c.ParamStr("notice", ""))
	if mbStrlen(notice) > 500 {
		c.Error("公告不能超过 500 个字", 1)
		return
	}
	if _, err := s.db().Exec(s.ctx, "UPDATE social_groups SET notice = ? WHERE id = ?", notice, gid); err != nil {
		c.dbError(err)
		return
	}
	// 公告是重要信息，开了免打扰也提醒（和 QQ 一样）。
	if notice != "" {
		rows, err := s.db().QueryAll(s.ctx, "SELECT id FROM users WHERE is_active = 1 AND id <> ?", rowInt(admin, "id", 0))
		if err != nil {
			c.dbError(err)
			return
		}
		gname := rowStr(g, "name", "")
		for _, row := range rows {
			rt.notifyMerge(c, rowInt(row, "id", 0), "「"+gname+"」群公告更新",
				mbSubstr(notice, 0, 80), "social", "notice:"+phpNum(gid))
		}
	}
	c.JSON(phpjson.New().
		Set("ok", true).
		Set("notified", notice != ""), 0, "ok", 0)
}

// ---------- social_group_allmute_set (api.php:1223) ----------

func (rt *Router) handleSocialGroupAllmuteSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group_allmute_set")
	s := rt.newSocialScope(c)

	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	if gid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	muted := int64(0)
	if c.ParamInt("muted", 0) == 1 {
		muted = 1
	}
	// 注意：这里不是 social_group_or_404，停用的群也能改（照抄）。
	row, err := s.db().QueryRow(s.ctx, "SELECT id FROM social_groups WHERE id = ?", gid)
	if err != nil {
		c.dbError(err)
		return
	}
	if row == nil {
		c.Error("群组不存在", 1)
		return
	}
	if _, err := s.db().Exec(s.ctx, "UPDATE social_groups SET all_muted = ? WHERE id = ?", muted, gid); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("group_id", gid).
		Set("all_muted", muted), 0, "ok", 0)
}

// ---------- social_group_join (api.php:1237) ----------

func (rt *Router) handleSocialGroupJoin(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group_join")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	if gid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	groupRole := "member"
	if rowStr(me, "role", "") == "admin" {
		groupRole = "owner"
	}
	res, err := s.db().Exec(s.ctx,
		"INSERT IGNORE INTO social_group_members (group_id, user_id, role, joined_at) VALUES (?, ?, ?, NOW())",
		gid, meID, groupRole)
	if err != nil {
		c.dbError(err)
		return
	}
	already := int64(0)
	if res.RowsAffected <= 0 {
		// 已经是成员（也可能是并发下被别人先插入）：不加成员、不写系统消息。
		already = 1
	} else {
		// 新加入：落一条系统消息（昵称取 users.nickname，空则 username）。
		if _, err := s.systemMessage(gid, meID, displayName(me)+"加入了群聊"); err != nil {
			c.dbError(err)
			return
		}
	}
	c.JSON(phpjson.New().
		Set("group_id", gid).
		Set("joined", 1).
		Set("already", already).
		Set("member_count", s.memberCount(gid)), 0, "ok", 0)
}

// ---------- social_group_leave (api.php:1260) ----------

func (rt *Router) handleSocialGroupLeave(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group_leave")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	if gid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	res, err := s.db().Exec(s.ctx, "DELETE FROM social_group_members WHERE group_id = ? AND user_id = ?", gid, meID)
	if err != nil {
		c.dbError(err)
		return
	}
	if res.RowsAffected <= 0 {
		c.Error("你还不是群成员", 1)
		return
	}
	if _, err := s.systemMessage(gid, meID, displayName(me)+"退出了群聊"); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("group_id", gid).
		Set("left", 1).
		Set("member_count", s.memberCount(gid)), 0, "ok", 0)
}

// ---------- social_group_images (api.php:1272) ----------

func (rt *Router) handleSocialGroupImages(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group_images")
	s := rt.newSocialScope(c)

	gid := c.ParamInt("group_id", 0)
	if gid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	page := c.ParamInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.ParamInt("page_size", 30)
	if ps > 60 {
		ps = 60
	}
	if ps < 1 {
		ps = 1
	}
	off := (page - 1) * ps

	v, has, err := s.db().QueryValue(s.ctx, `SELECT COUNT(*) FROM social_messages m
                                  WHERE m.group_id = ? AND m.is_recalled = 0 AND m.image <> ''`, gid)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, has)
	rows, err := s.db().QueryAll(s.ctx, `SELECT m.id, m.user_id, m.image, m.image_w, m.image_h, m.created_at,
                                        u.nickname, u.username, u.avatar
                                   FROM social_messages m LEFT JOIN users u ON u.id = m.user_id
                                  WHERE m.group_id = ? AND m.is_recalled = 0 AND m.image <> ''
                                  ORDER BY m.id DESC LIMIT ? OFFSET ?`, gid, ps, off)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		nick := phpTrim(rowStr(row, "nickname", ""))
		if nick == "" {
			nick = "用户" + phpNum(rowInt(row, "user_id", 0))
		}
		list = append(list, phpjson.New().
			Set("id", rowInt(row, "id", 0)).
			Set("user_id", rowInt(row, "user_id", 0)).
			Set("nickname", nick).
			Set("username", rowStr(row, "username", "")).
			Set("avatar", rowStr(row, "avatar", "")).
			Set("image", rowStr(row, "image", "")).
			Set("image_w", rowInt(row, "image_w", 0)).
			Set("image_h", rowInt(row, "image_h", 0)).
			Set("created_at", rowStr(row, "created_at", "")))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("page", page).
		Set("page_size", ps).
		Set("total", total).
		Set("has_more", off+int64(len(list)) < total), 0, "ok", 0)
}

// ---------- social_mute_set (api.php:1313) ----------

func (rt *Router) handleSocialMuteSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_mute_set")
	s := rt.newSocialScope(c)

	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	gid := c.ParamInt("group_id", 0)
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	muted := int64(0)
	if c.ParamInt("muted", 0) == 1 {
		muted = 1
	}
	meID := rowInt(me, "id", 0)
	if muted == 1 {
		if _, err := s.db().Exec(s.ctx,
			"INSERT IGNORE INTO social_mutes (group_id, user_id, created_at) VALUES (?, ?, NOW())", gid, meID); err != nil {
			c.dbError(err)
			return
		}
	} else {
		if _, err := s.db().Exec(s.ctx,
			"DELETE FROM social_mutes WHERE group_id = ? AND user_id = ?", gid, meID); err != nil {
			c.dbError(err)
			return
		}
	}
	c.JSON(phpjson.New().
		Set("group_id", gid).
		Set("muted", muted), 0, "ok", 0)
}

// ---------- social_group_members (api.php:1329) ----------

func (rt *Router) handleSocialGroupMembers(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "social_group_members")
	s := rt.newSocialScope(c)

	// 游客也能看：这里用 current_user()（不是 _or_401），未登录时 is_member = 0。
	me, err := s.me()
	if err != nil {
		c.dbError(err)
		return
	}
	gid := c.ParamInt("group_id", 0)
	if gid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	kw := phpTrim(c.ParamStr("keyword", ""))
	page := c.ParamInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.ParamInt("page_size", 30)
	if ps > 60 {
		ps = 60
	}
	if ps < 1 {
		ps = 1
	}
	off := (page - 1) * ps

	where := []string{"m.group_id = ?"}
	args := []any{gid}
	if kw != "" {
		where = append(where, "(u.nickname LIKE ? OR u.username LIKE ?)")
		args = append(args, "%"+kw+"%", "%"+kw+"%")
	}
	cond := " WHERE " + strings.Join(where, " AND ")
	v, has, err := s.db().QueryValue(s.ctx,
		"SELECT COUNT(*) FROM social_group_members m JOIN users u ON u.id = m.user_id"+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, has)
	selArgs := append(append([]any{}, args...), ps, off)
	// 排序: 群主 -> 管理员 -> 普通成员, 同角色按加入时间升序。
	rows, err := s.db().QueryAll(s.ctx,
		"SELECT u.*, m.role AS group_role, m.joined_at\n"+
			"                                   FROM social_group_members m JOIN users u ON u.id = m.user_id"+
			cond+" ORDER BY FIELD(m.role, 'owner', 'admin', 'member'), m.joined_at ASC, m.id ASC\n"+
			"                          LIMIT ? OFFSET ?", selArgs...)
	if err != nil {
		c.dbError(err)
		return
	}
	meID := int64(0)
	if me != nil {
		meID = rowInt(me, "id", 0)
	}
	list := []any{}
	for _, u := range rows {
		uid := rowInt(u, "id", 0)
		muteState, hasMute := rt.userMuteState(c, uid, gid)
		groupRole := rowStr(u, "group_role", "")
		item := userBrief(u)
		// 群角色: owner/admin/member
		item.Set("role", groupRole).
			Set("group_role", groupRole).
			Set("joined_at", rowStr(u, "joined_at", "")).
			// 兼容旧客户端 @ 选人面板（SocialScreen.kt 里读 role == 'admin' 判全局管理员）
			Set("global_role", rowStr(u, "role", "user")).
			Set("is_admin", boolToInt(rowStr(u, "role", "") == "admin"))
		if hasMute {
			item.Set("muted", int64(1)).
				Set("mute_left", muteLeftText(muteState)).
				Set("mute_reason", rowStr(muteState, "reason", ""))
		} else {
			item.Set("muted", int64(0)).
				Set("mute_left", "").
				Set("mute_reason", "")
		}
		list = append(list, item)
	}
	isMember := int64(0)
	if meID > 0 && s.isMemberRow(gid, meID) {
		isMember = 1
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("page", page).
		Set("page_size", ps).
		Set("total", total).
		Set("is_member", isMember).
		Set("member_count", s.memberCount(gid)), 0, "ok", 0)
}

// ---------- admin_user_mute (api.php:1380) ----------

func (rt *Router) handleAdminUserMute(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_user_mute")
	s := rt.newSocialScope(c)

	admin, ok := rt.requireAdminRow(c)
	if !ok {
		return
	}
	uid := c.ParamInt("user_id", 0)
	gid := c.ParamInt("group_id", 0)
	minutes := c.ParamInt("minutes", 60)
	reason := phpTrim(c.ParamStr("reason", ""))
	if mbStrlen(reason) > 60 {
		c.Error("禁言原因不能超过 60 个字", 1)
		return
	}
	u, err := s.db().QueryRow(s.ctx, "SELECT id, nickname, username, role FROM users WHERE id = ?", uid)
	if err != nil {
		c.dbError(err)
		return
	}
	if u == nil {
		c.Error("用户不存在", 1)
		return
	}
	if rowStr(u, "role", "") == "admin" {
		c.Error("不能禁言管理员", 1)
		return
	}
	if gid > 0 {
		if _, ok := s.groupOr404(gid, false); !ok {
			return
		}
	}
	if minutes < 0 {
		minutes = 0
	}
	if minutes > 432000 {
		minutes = 432000 // 上限 300 天, 0 = 永久
	}
	until := ""
	if minutes > 0 {
		until = phpDateTimeAt(nowUnix() + minutes*60)
	}
	var untilArg any
	if until != "" {
		untilArg = until
	}
	if _, err := s.db().Exec(s.ctx,
		`INSERT INTO social_user_mutes (group_id, user_id, until_at, reason, created_by, created_at)
                           VALUES (?, ?, ?, ?, ?, NOW())
                           ON DUPLICATE KEY UPDATE until_at = VALUES(until_at), reason = VALUES(reason),
                                                   created_by = VALUES(created_by), created_at = NOW()`,
		gid, uid, untilArg, reason, rowInt(admin, "id", 0)); err != nil {
		c.dbError(err)
		return
	}
	mute := store.Row{"until_at": untilArg, "reason": reason}
	leftTxt := muteLeftText(mute)
	gname := ""
	if gid > 0 {
		v, has, err := s.db().QueryValue(s.ctx, "SELECT name FROM social_groups WHERE id = ?", gid)
		if err != nil {
			c.dbError(err)
			return
		}
		// PHP 写的是 `(string)($st->fetchColumn() ?: '')`：名为 "0" 的群也会被当成空串（照抄）。
		if has && v != nil {
			if g := phpStr(v); !phpFalsy(g) {
				gname = g
			}
		}
	}
	where := "全站"
	if gname != "" {
		where = "在「" + gname + "」"
	}
	content := where + "被管理员禁言"
	if leftTxt != "" {
		content += ", " + leftTxt
	}
	if reason != "" {
		content += ", 原因: " + reason
	}
	rt.notifyPush(c, uid, "你已被禁言", content, "admin")
	c.JSON(phpjson.New().
		Set("user_id", uid).
		Set("group_id", gid).
		Set("until_at", until).
		Set("left_text", leftTxt).
		Set("reason", reason), 0, "ok", 0)
}

// ---------- admin_user_unmute (api.php:1416) ----------

func (rt *Router) handleAdminUserUnmute(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_user_unmute")
	s := rt.newSocialScope(c)

	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	uid := c.ParamInt("user_id", 0)
	gid := c.ParamInt("group_id", 0)
	if uid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	res, err := s.db().Exec(s.ctx, "DELETE FROM social_user_mutes WHERE user_id = ? AND group_id = ?", uid, gid)
	if err != nil {
		c.dbError(err)
		return
	}
	n := res.RowsAffected
	if n > 0 {
		rt.notifyPush(c, uid, "禁言已解除", "管理员已解除你的禁言, 现在可以正常发言了", "admin")
	}
	c.JSON(phpjson.New().
		Set("user_id", uid).
		Set("group_id", gid).
		Set("removed", n), 0, "ok", 0)
}
