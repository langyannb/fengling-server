package httpapi

import (
	"net/http"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 4 通知 action 的原生实现，逐字对齐 api.php:1562-1606。

// notifyPublic 对齐 api.php:3787 notify_public()：通知转客户端结构。
// 列表 SQL 是 SELECT *，但只输出这 7 个键（user_id 绝不外泄）。
func notifyPublic(n store.Row) *phpjson.O {
	isRead := int64(0)
	if rowInt(n, "is_read", 0) == 1 {
		isRead = 1
	}
	return phpjson.New().
		Set("id", rowInt(n, "id", 0)).
		Set("title", rowStr(n, "title", "")).
		Set("content", rowStr(n, "content", "")).
		Set("type", rowStr(n, "type", "")).
		Set("link", rowStr(n, "link", "")).
		Set("is_read", isRead).
		Set("created_at", rowStr(n, "created_at", ""))
}

// notifyTypeCounts 把 GROUP BY type 的结果转成 unread_by_type。
// PHP 里 $byType 是关联数组，空的时候 json_encode 出 []（不是 {}）——这里保持一致。
func notifyTypeCounts(rows []store.Row) any {
	if len(rows) == 0 {
		return []any{}
	}
	o := phpjson.New()
	for _, row := range rows {
		o.Set(rowStr(row, "type", ""), rowInt(row, "c", 0))
	}
	return o
}

// ---------- notifications (api.php:1562) ----------

func (rt *Router) handleNotifications(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "notifications")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
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
	unreadOnly := c.ParamInt("unread_only", 0) == 1
	cond := "user_id = ?"
	if unreadOnly {
		cond += " AND is_read = 0"
	}

	v, hasRow, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM notifications WHERE "+cond, meID)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, hasRow)

	v, hasRow, err = rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM notifications WHERE user_id = ? AND is_read = 0", meID)
	if err != nil {
		c.dbError(err)
		return
	}
	unread := intValue(v, hasRow)

	byTypeRows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT type, COUNT(*) AS c FROM notifications WHERE user_id = ? AND is_read = 0 GROUP BY type", meID)
	if err != nil {
		c.dbError(err)
		return
	}

	off := (page - 1) * ps
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT * FROM notifications WHERE "+cond+" ORDER BY id DESC LIMIT "+phpNum(ps)+" OFFSET "+phpNum(off), meID)
	if err != nil {
		c.dbError(err)
		return
	}
	list := make([]any, 0, len(rows))
	for _, n := range rows {
		list = append(list, notifyPublic(n))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", total).
		Set("unread", unread).
		Set("unread_by_type", notifyTypeCounts(byTypeRows)).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- notification_read (api.php:1588) ----------

func (rt *Router) handleNotificationRead(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "notification_read")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	if c.ParamInt("all", 0) == 1 {
		if _, err := rt.accounts().Exec(c.R.Context(),
			"UPDATE notifications SET is_read = 1 WHERE user_id = ?", meID); err != nil {
			c.dbError(err)
			return
		}
	} else {
		id := c.ParamInt("id", 0)
		if id <= 0 {
			c.Error("参数错误", 1)
			return
		}
		if _, err := rt.accounts().Exec(c.R.Context(),
			"UPDATE notifications SET is_read = 1 WHERE id = ? AND user_id = ?", id, meID); err != nil {
			c.dbError(err)
			return
		}
	}
	v, hasRow, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM notifications WHERE user_id = ? AND is_read = 0", meID)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("ok", true).
		Set("unread", intValue(v, hasRow)), 0, "ok", 0)
}

// ---------- notification_delete (api.php:1601) ----------

func (rt *Router) handleNotificationDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "notification_delete")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	id := c.ParamInt("id", 0)
	if id <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"DELETE FROM notifications WHERE id = ? AND user_id = ?", id, rowInt(me, "id", 0)); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true), 0, "ok", 0)
}
