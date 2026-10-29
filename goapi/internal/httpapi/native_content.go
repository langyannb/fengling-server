package httpapi

// 阶段 5C：内容与展示类 23 个 action 的原生实现，逐字对齐 api.php:2422-2753 与 2920-3009。
//
// 覆盖（公开 8 个 / 管理 15 个）：
//   公开：categories、apps、app_detail、banners、link_click、contributors、
//         contributor_apps、stats（后两个里 contributor_apps/stats 实为管理员）
//   管理：category_*、app_*、banner_*、link_*、contributor_*（各 create/update/delete）
//
// 全局口径（与契约 .go008-contract.md 一致，别顺手"优化"）：
//   - `param()` 的取值优先级 JSON body → $_POST → $_GET，且 **isset 语义**：显式 JSON
//     null 视同「未传」，继续下沉（见 contentParam；Ctx.Param 不满足这一点）。
//   - 更新类 `*_update` 用「参数是否出现过」组装动态 SET，`0`/`""` 与「未传」必须区分。
//   - `apps`/`app_detail` 的 `SELECT a.*` **列顺序即键序**，非管理员 `contributor_qq`
//     整个键消失（不是 null）；`app_detail` 的 `parent_pack` 缺行输出 **false**。
//   - 403/429/502 经 statusForCode 全部落 HTTP 400；本文件所有错误只有 1（400）。
//   - Redis 零新增、不推事件、不加事务/审计。
import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// ---------- 参数与 PHP 语义小工具 ----------

// contentParam 复刻 config.php:37 param()：JSON body → $_POST → $_GET，缺省 (nil,false)。
// 与 Ctx.Param 的关键差别是 **isset 语义**：PHP 的 isset($body[$k]) 对显式 null 为 false，
// 会继续往 $_POST/$_GET 回落；Ctx.Param 会直接返回 (nil,true)，所以本文件一律用它。
func contentParam(c *Ctx, key string) (any, bool) {
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

func contentStr(c *Ctx, key, def string) string {
	v, ok := contentParam(c, key)
	if !ok {
		return def
	}
	return phpStr(v)
}

func contentInt(c *Ctx, key string, def int64) int64 {
	v, ok := contentParam(c, key)
	if !ok {
		return def
	}
	return phpInt(v)
}

// contentFalsy 复刻 PHP 的 `if (!$v)`：null/false/0/0.0/""/"0"/[] 为假。
func contentFalsy(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == "" || x == "0"
	case json.Number:
		f, _ := x.Float64()
		return f == 0
	case float64:
		return x == 0
	case float32:
		return x == 0
	case int:
		return x == 0
	case int64:
		return x == 0
	}
	return false
}

// contentDBArg 把 param() 原始值转成驱动可绑定形态。
// PHP 侧 json_decode 把 JSON 数字解成 int/float，Go 的 json.Number 必须落成数值；
// bool 在 PDO 里等价于 1/0。
func contentDBArg(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	default:
		return v
	}
}

// phpSubstrBytes 复刻 PHP 的 substr($s, 0, $n)：按**字节**截断（可能切坏 UTF-8）。
func phpSubstrBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// phpURLEncode 复刻 PHP urlencode()：空格 → `+`，未保留字符 → %XX 大写十六进制，
// 只放行 [A-Za-z0-9-_.]（注意 PHP **不**放行 `~`，而 Go 的 url.QueryEscape 放行）。
func phpURLEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.':
			b.WriteByte(ch)
		case ch == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[ch>>4])
			b.WriteByte(hex[ch&0x0f])
		}
	}
	return b.String()
}

// ---------- 鉴权 ----------

// contentIsAdmin 对齐 api.php:3287 is_admin()：静默判定，任何失败都返回 false、绝不报错。
// 第二个返回值 = 查询过程没出错（出错时已写过 500，调用方直接 return）。
func (rt *Router) contentIsAdmin(c *Ctx) (bool, bool) {
	if phpFalsy(c.Token()) {
		return false, true
	}
	me, err := rt.accounts().CurrentUserRow(c.R.Context(), c.Token())
	if err != nil {
		c.Log().Error("is_admin 鉴权查询失败", "err", err)
		c.Error("服务器错误: "+err.Error(), 500)
		return false, false
	}
	if me == nil {
		return false, true
	}
	return rowStr(me, "role", "") == "admin", true
}

// ---------- 有序输出（SELECT a.* 的列顺序 = 键序） ----------

// appsColumns 是线上 apps 表的列顺序：schema.sql:27-43 的基础列，再按 schema.sql:98-168
// 的 ALTER（都带 AFTER）链式插入得出。Go 的 store.Row 是 map（丢顺序），所以必须显式定序。
var appsColumns = []string{
	"id", "category_id", "name", "icon", "version", "release_date", "is_new", "new_until",
	"description", "screenshots", "contributor_qq", "package_name", "download_count",
	"rating", "sort_order", "pack_id", "is_top", "is_featured", "is_active", "created_at", "updated_at",
}

// httpapi/categories 表列顺序（schema.sql:15-24）。
var categoriesColumns = []string{"id", "parent_id", "name", "color", "icon", "sort_order", "created_at"}

// banners 表列顺序（schema.sql:83-96 + 其后 `ADD COLUMN url AFTER app_id`）。
var bannersColumns = []string{"id", "image", "title", "app_id", "url", "sort_order", "is_active", "created_at"}

// contentObj 按 cols 给定顺序把一行造成有序对象；行里存在但不在 cols 的列
// （线上新增列）按列名排序追加在末尾。cols 里存在但行里没有的列直接跳过，
// 避免硬编码列名与真实表结构漂移时多出 `null` 键。
func contentObj(r store.Row, cols []string) *phpjson.O {
	o := phpjson.New()
	seen := make(map[string]bool, len(r))
	for _, k := range cols {
		seen[k] = true
		if _, ok := r.Get(k); !ok {
			continue
		}
		o.Set(k, r[k])
	}
	var extra []string
	for k := range r {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		o.Set(k, r[k])
	}
	return o
}

// contentList 把多行按同一列序输出成数组（空 → `[]`，不是 `{}`）。
func contentList(rows []store.Row, cols []string) []any {
	out := []any{}
	for _, r := range rows {
		out = append(out, contentObj(r, cols))
	}
	return out
}

// appIsNew 对齐 `($app['new_until'] && strtotime($app['new_until']) > time()) ? 1 : 0`。
func appIsNew(r store.Row) int64 {
	v, _ := r.Get("new_until")
	if contentFalsy(v) {
		return 0
	}
	ts, ok := phpStrtotime(phpStr(v))
	if ok && ts > nowUnix() {
		return 1
	}
	return 0
}

// appScreenshots 对齐 `$v ? (json_decode($v, true) ?: []) : []`（坏 JSON / 假值 → []）。
func appScreenshots(r store.Row) any {
	v, _ := r.Get("screenshots")
	if contentFalsy(v) {
		return []any{}
	}
	dec, ok := decodeOrderedJSON(phpStr(v))
	if !ok || contentFalsy(dec) {
		return []any{}
	}
	return dec
}

// appColumnValue 返回 apps 某一列在**加工后**的输出值（列名 → 转换规则逐字对齐 PHP）。
func appColumnValue(r store.Row, k string) any {
	v, _ := r.Get(k)
	switch k {
	case "download_count", "id", "is_top", "is_featured":
		return phpInt(v)
	case "rating":
		return phpFloat(v)
	case "pack_id":
		// `$app['pack_id'] ? (int)$app['pack_id'] : null`
		if contentFalsy(v) {
			return nil
		}
		return phpInt(v)
	case "is_new":
		return appIsNew(r)
	case "release_date":
		// `$app['release_date'] ?: ''`
		if contentFalsy(v) {
			return ""
		}
		return v
	case "screenshots":
		return appScreenshots(r)
	}
	return v
}

// contentAppRow 造 apps/app_detail 的主体对象：列顺序同 cols，逐列套 appColumnValue；
// 非管理员时 contributor_qq **整个键消失**（PHP unset，而不是给 null/空串）。
func contentAppRow(r store.Row, cols []string, isAdmin bool) *phpjson.O {
	o := phpjson.New()
	seen := make(map[string]bool, len(r))
	for _, k := range cols {
		seen[k] = true
		if _, ok := r.Get(k); !ok {
			continue
		}
		if k == "contributor_qq" && !isAdmin {
			continue
		}
		o.Set(k, appColumnValue(r, k))
	}
	var extra []string
	for k := range r {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		if k == "contributor_qq" && !isAdmin {
			continue
		}
		o.Set(k, r[k])
	}
	return o
}

// contentCount 跑一条 `(int)SELECT COUNT(*)…` 聚合。
func (rt *Router) contentCount(c *Ctx, sql string, args ...any) (int64, bool) {
	v, _, err := rt.accounts().QueryValue(c.R.Context(), sql, args...)
	if err != nil {
		c.dbError(err)
		return 0, false
	}
	return intValue(v, true), true
}

// categoryExists 对齐 api.php:3255 category_exists()。
func (rt *Router) categoryExists(c *Ctx, id int64) (bool, bool) {
	_, has, err := rt.accounts().QueryValue(c.R.Context(), "SELECT 1 FROM categories WHERE id = ?", id)
	if err != nil {
		c.dbError(err)
		return false, false
	}
	return has, true
}

// ---------- categories ----------

func (rt *Router) handleCategories(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "categories")
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT * FROM categories ORDER BY parent_id ASC, sort_order ASC, id ASC")
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(contentList(rows, categoriesColumns), 0, "ok", 0)
}

func (rt *Router) handleCategoryCreate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "category_create")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	nameRaw, _ := contentParam(c, "name")
	if contentFalsy(nameRaw) {
		c.Error("分类名不能为空", 1)
		return
	}
	name := phpStr(nameRaw)
	parentID := contentInt(c, "parent_id", 0)
	color := contentStr(c, "color", "#4C6FFF")
	icon := contentStr(c, "icon", "")
	order := contentInt(c, "sort_order", 0)
	if parentID > 0 {
		exists, ok := rt.categoryExists(c, parentID)
		if !ok {
			return
		}
		if !exists {
			c.Error("父分类不存在", 1)
			return
		}
	}
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO categories (parent_id, name, color, icon, sort_order) VALUES (?, ?, ?, ?, ?)",
		parentID, name, color, icon, order)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("id", res.LastInsertID), 0, "ok", 0)
}

func (rt *Router) handleCategoryUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "category_update")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	nameV, nameOK := contentParam(c, "name")
	parentV, parentOK := contentParam(c, "parent_id")
	colorV, colorOK := contentParam(c, "color")
	iconV, iconOK := contentParam(c, "icon")
	sortV, sortOK := contentParam(c, "sort_order")
	// 注释写了「不能指向自己的子分类（防循环）」但代码只判了自己 —— 照抄，别补防循环。
	if parentOK && phpInt(parentV) == id {
		c.Error("父分类不能是自己", 1)
		return
	}
	var sets []string
	var args []any
	if nameOK {
		sets = append(sets, "name = ?")
		args = append(args, contentDBArg(nameV))
	}
	if parentOK {
		sets = append(sets, "parent_id = ?")
		args = append(args, phpInt(parentV))
	}
	if colorOK {
		sets = append(sets, "color = ?")
		args = append(args, contentDBArg(colorV))
	}
	if iconOK {
		sets = append(sets, "icon = ?")
		args = append(args, contentDBArg(iconV))
	}
	if sortOK {
		sets = append(sets, "sort_order = ?")
		args = append(args, phpInt(sortV))
	}
	if len(sets) == 0 {
		c.Error("没有要更新的字段", 1)
		return
	}
	args = append(args, id)
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE categories SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

func (rt *Router) handleCategoryDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "category_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	// 无事务的两步写：先把子分类升为顶级，再删自己（照抄，勿加事务/级联）。
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE categories SET parent_id = 0 WHERE parent_id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"DELETE FROM categories WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- apps ----------

func (rt *Router) handleApps(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "apps")
	isAdmin, ok := rt.contentIsAdmin(c)
	if !ok {
		return
	}
	categoryID := contentInt(c, "category_id", 0)
	keyword := contentStr(c, "keyword", "")
	packV, packOK := contentParam(c, "pack_id")
	includeInactive := contentInt(c, "include_inactive", 0) == 1 && isAdmin

	where := "a.is_active = 1"
	if includeInactive {
		where = "1 = 1"
	}
	sql := "SELECT a.*, c.name AS category_name, c.color AS category_color FROM apps a" +
		" LEFT JOIN categories c ON a.category_id = c.id WHERE " + where
	var args []any
	if packOK {
		// 只要传了 pack_id（含 0）就走整合包分支，且不再走分类过滤。
		sql += " AND a.pack_id = ?"
		args = append(args, phpInt(packV))
	} else if categoryID > 0 {
		subs, err := rt.accounts().QueryAll(c.R.Context(),
			"SELECT id FROM categories WHERE parent_id = ?", categoryID)
		if err != nil {
			c.dbError(err)
			return
		}
		if len(subs) > 0 {
			ids := make([]string, 0, len(subs)+1)
			for _, s := range subs {
				ids = append(ids, strconv.FormatInt(rowInt(s, "id", 0), 10))
			}
			ids = append(ids, strconv.FormatInt(categoryID, 10))
			sql += " AND a.category_id IN (" + strings.Join(ids, ",") + ")"
		} else {
			sql += " AND a.category_id = ?"
			args = append(args, categoryID)
		}
	}
	if keyword != "" {
		sql += " AND a.name LIKE ?"
		args = append(args, "%"+keyword+"%")
	}
	sql += " ORDER BY a.is_top DESC, (a.new_until IS NOT NULL AND a.new_until > NOW()) DESC, " +
		"COALESCE(a.release_date, DATE(a.created_at)) DESC, a.id DESC"

	rows, err := rt.accounts().QueryAll(c.R.Context(), sql, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	cols := append(append([]string{}, appsColumns...), "category_name", "category_color")
	out := []any{}
	for _, row := range rows {
		out = append(out, contentAppRow(row, cols, isAdmin))
	}
	c.JSON(out, 0, "ok", 0)
}

func (rt *Router) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "app_detail")
	isAdmin, ok := rt.contentIsAdmin(c)
	if !ok {
		return
	}
	id := contentInt(c, "id", 0)
	// include_inactive 只对管理员生效；非管理员强制只看上架（下架 = 软件不存在）。
	cond := " AND a.is_active = 1"
	if contentInt(c, "include_inactive", 0) == 1 && isAdmin {
		cond = ""
	}
	app, err := rt.accounts().QueryRow(c.R.Context(),
		"SELECT a.*, c.name AS category_name FROM apps a LEFT JOIN categories c ON a.category_id = c.id WHERE a.id = ?"+cond, id)
	if err != nil {
		c.dbError(err)
		return
	}
	if app == nil {
		c.Error("软件不存在", 1)
		return
	}
	cols := append(append([]string{}, appsColumns...), "category_name")
	o := contentAppRow(app, cols, isAdmin)

	links, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT id, pan_type, label, url, password FROM pan_links WHERE app_id = ? AND is_active = 1 ORDER BY sort_order ASC, id ASC", id)
	if err != nil {
		c.dbError(err)
		return
	}
	o.Set("pan_links", contentList(links, []string{"id", "pan_type", "label", "url", "password"}))

	items, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT id, name, icon, version, download_count FROM apps WHERE pack_id = ? AND is_active = 1 ORDER BY sort_order ASC, id ASC", id)
	if err != nil {
		c.dbError(err)
		return
	}
	itemOut := []any{}
	for _, it := range items {
		io := contentObj(it, []string{"id", "name", "icon", "version", "download_count"})
		io.Put("id", rowInt(it, "id", 0))
		io.Put("download_count", rowInt(it, "download_count", 0))
		itemOut = append(itemOut, io)
	}
	o.Set("pack_items", itemOut)

	// 所属整合包：父行被删时 PHP 的 fetch() 返回 false ⇒ JSON 里是 **false 不是 null**。
	packV, _ := app.Get("pack_id")
	if !contentFalsy(packV) {
		parent, err := rt.accounts().QueryRow(c.R.Context(),
			"SELECT id, name, icon FROM apps WHERE id = ?", phpInt(packV))
		if err != nil {
			c.dbError(err)
			return
		}
		if parent == nil {
			o.Set("parent_pack", false)
		} else {
			o.Set("parent_pack", contentObj(parent, []string{"id", "name", "icon"}))
		}
	} else {
		o.Set("parent_pack", nil)
	}
	c.JSON(o, 0, "ok", 0)
}

// insertApp 对齐 api.php:3310 insert_app()。返回新 id；失败时已写响应、ok=false。
func (rt *Router) insertApp(c *Ctx) (int64, bool) {
	nameRaw, _ := contentParam(c, "name")
	if contentFalsy(nameRaw) {
		c.Error("软件名不能为空", 1)
		return 0, false
	}
	categoryID := contentInt(c, "category_id", 0)
	var categoryArg any
	if categoryID != 0 {
		categoryArg = categoryID
	}
	packID := contentInt(c, "pack_id", 0)
	var packArg any
	if packID != 0 {
		packArg = packID
	}
	releaseRaw, _ := contentParam(c, "release_date")
	var releaseArg any
	if !contentFalsy(releaseRaw) {
		releaseArg = contentDBArg(releaseRaw)
	}
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO apps (category_id, name, icon, version, description, screenshots, contributor_qq, package_name, rating, sort_order, pack_id, is_top, is_featured, release_date)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		categoryArg, phpStr(nameRaw),
		contentStr(c, "icon", ""), contentStr(c, "version", ""), contentStr(c, "description", ""),
		contentStr(c, "screenshots", ""), contentStr(c, "contributor_qq", ""), contentStr(c, "package_name", ""),
		phpFloat(mustContentParam(c, "rating")), contentInt(c, "sort_order", 0),
		packArg, contentInt(c, "is_top", 0), contentInt(c, "is_featured", 0),
		releaseArg)
	if err != nil {
		c.dbError(err)
		return 0, false
	}
	return res.LastInsertID, true
}

// mustContentParam 取参数，缺省返回 int64(0)（PHP `param('rating', 0)` 的默认值）。
func mustContentParam(c *Ctx, key string) any {
	if v, ok := contentParam(c, key); ok {
		return v
	}
	return int64(0)
}

func (rt *Router) handleAppCreate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "app_create")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	newID, ok := rt.insertApp(c)
	if !ok {
		return
	}
	// new_flag：手动标「新」3 天（上海时区），不传则不动（新建时 new_until 保持 NULL）。
	if nf, nfOK := contentParam(c, "new_flag"); nfOK {
		var until any
		if phpInt(nf) != 0 {
			until = time.Unix(nowUnix()+3*86400, 0).In(config.LocalZone()).Format("2006-01-02 15:04:05")
		}
		if _, err := rt.accounts().Exec(c.R.Context(),
			"UPDATE apps SET new_until = ? WHERE id = ?", until, newID); err != nil {
			c.dbError(err)
			return
		}
	}
	c.JSON(phpjson.New().Set("id", newID), 0, "ok", 0)
}

// appUpdateFields 是 app_update 的列白名单（顺序即 SQL SET 顺序）。
var appUpdateFields = []string{
	"name", "category_id", "icon", "version", "description", "screenshots", "package_name",
	"rating", "sort_order", "is_active", "pack_id", "is_top", "is_featured", "contributor_qq", "release_date",
}

func (rt *Router) handleAppUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "app_update")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	newFlag, newFlagOK := contentParam(c, "new_flag")
	var sets []string
	var args []any
	for _, f := range appUpdateFields {
		v, present := contentParam(c, f)
		if !present {
			continue
		}
		switch f {
		case "pack_id":
			sets = append(sets, "pack_id = ?")
			if n := phpInt(v); n != 0 {
				args = append(args, n)
			} else {
				args = append(args, nil)
			}
		case "is_top", "is_featured":
			sets = append(sets, f+" = ?")
			args = append(args, phpInt(v))
		case "release_date":
			sets = append(sets, "release_date = ?")
			if s, isStr := v.(string); isStr && s == "" {
				args = append(args, nil)
			} else {
				args = append(args, contentDBArg(v))
			}
		default:
			sets = append(sets, f+" = ?")
			args = append(args, contentDBArg(v))
		}
	}
	if newFlagOK {
		if phpInt(newFlag) != 0 {
			sets = append(sets, "new_until = DATE_ADD(NOW(), INTERVAL 3 DAY)")
		} else {
			sets = append(sets, "new_until = NULL")
		}
	}
	if len(args) == 0 && !newFlagOK {
		c.Error("没有要更新的字段", 1)
		return
	}
	args = append(args, id)
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE apps SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

func (rt *Router) handleAppDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "app_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	// 先删 apps 再删 pan_links（顺序照抄，无事务，不级联 clicks）。
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM apps WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM pan_links WHERE app_id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- link_* ----------

func (rt *Router) handleLinkCreate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "link_create")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	appID := contentInt(c, "app_id", 0)
	urlRaw, _ := contentParam(c, "url")
	label := contentStr(c, "label", "UC网盘")
	panType := contentStr(c, "pan_type", "uc")
	password := contentStr(c, "password", "")
	if contentFalsy(urlRaw) {
		c.Error("链接地址不能为空", 1)
		return
	}
	url := phpStr(urlRaw)
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO pan_links (app_id, pan_type, label, url, password) VALUES (?, ?, ?, ?, ?)",
		appID, panType, label, url, password)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("id", res.LastInsertID), 0, "ok", 0)
}

// linkUpdateFields 是 link_update 的白名单：注意 **is_active 不在其中**，传了也改不了。
var linkUpdateFields = []string{"pan_type", "label", "url", "password", "sort_order"}

func (rt *Router) handleLinkUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "link_update")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	var sets []string
	var args []any
	for _, f := range linkUpdateFields {
		v, present := contentParam(c, f)
		if !present {
			continue
		}
		sets = append(sets, f+" = ?")
		args = append(args, contentDBArg(v))
	}
	if len(sets) == 0 {
		c.Error("没有要更新的字段", 1)
		return
	}
	args = append(args, id)
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE pan_links SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

func (rt *Router) handleLinkDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "link_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	// 不删 clicks 里该链接的历史点击（统计口径，照抄）。
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM pan_links WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

func (rt *Router) handleLinkClick(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "link_click")
	// 公开、**无任何限流**、无登录要求、无事务 —— 每次点击都写一条 clicks。
	id := contentInt(c, "id", 0)
	link, err := rt.accounts().QueryRow(c.R.Context(),
		"SELECT * FROM pan_links WHERE id = ? AND is_active = 1", id)
	if err != nil {
		c.dbError(err)
		return
	}
	if link == nil {
		c.Error("链接不存在", 1)
		return
	}
	ua := phpSubstrBytes(r.Header.Get("User-Agent"), 255)
	referer := phpSubstrBytes(r.Header.Get("Referer"), 255)
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO clicks (link_id, ip, user_agent, referer) VALUES (?, ?, ?, ?)",
		id, clientIP(r), ua, referer); err != nil {
		c.dbError(err)
		return
	}
	appID, _ := link.Get("app_id")
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE apps SET download_count = download_count + 1 WHERE id = ?", appID); err != nil {
		c.dbError(err)
		return
	}
	url, _ := link.Get("url")
	password, _ := link.Get("password")
	c.JSON(phpjson.New().Set("url", url).Set("password", password), 0, "ok", 0)
}

// ---------- banners ----------

func (rt *Router) handleBanners(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "banners")
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT b.*, a.name AS app_name FROM banners b LEFT JOIN apps a ON b.app_id = a.id"+
			" WHERE b.is_active = 1 ORDER BY b.sort_order ASC, b.id ASC")
	if err != nil {
		c.dbError(err)
		return
	}
	cols := append(append([]string{}, bannersColumns...), "app_name")
	out := []any{}
	for _, row := range rows {
		o := contentObj(row, cols)
		o.Put("id", rowInt(row, "id", 0))
		appID, _ := row.Get("app_id")
		if contentFalsy(appID) {
			o.Put("app_id", nil)
		} else {
			o.Put("app_id", phpInt(appID))
		}
		out = append(out, o)
	}
	c.JSON(out, 0, "ok", 0)
}

func (rt *Router) handleBannerCreate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "banner_create")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	appID := contentInt(c, "app_id", 0)
	var appArg any
	if appID != 0 {
		appArg = appID
	}
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO banners (image, title, app_id, url, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?)",
		contentStr(c, "image", ""), contentStr(c, "title", ""), appArg, contentStr(c, "url", ""),
		contentInt(c, "sort_order", 0), contentInt(c, "is_active", 1))
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("id", res.LastInsertID), 0, "ok", 0)
}

func (rt *Router) handleBannerUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "banner_update")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	var sets []string
	var args []any
	for _, f := range []string{"image", "title", "app_id", "url", "sort_order", "is_active"} {
		v, present := contentParam(c, f)
		if !present {
			continue
		}
		sets = append(sets, f+" = ?")
		if f == "app_id" {
			// `(int)$v ?: null`：app_id=0 / "" 会把 NULL 写进去（清除绑定）。
			if n := phpInt(v); n != 0 {
				args = append(args, n)
			} else {
				args = append(args, nil)
			}
			continue
		}
		args = append(args, contentDBArg(v))
	}
	if len(sets) == 0 {
		c.Error("没有要更新的字段", 1)
		return
	}
	args = append(args, id)
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE banners SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

func (rt *Router) handleBannerDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "banner_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	// 只删行，不动 S3 图片对象。
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM banners WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- stats ----------

func (rt *Router) handleStats(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "stats")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	// 计数**含已下架**；today_clicks 用 DB 的 CURDATE()（不是 Go 本地时间）。
	totalApps, ok := rt.contentCount(c, "SELECT COUNT(*) FROM apps")
	if !ok {
		return
	}
	totalDownloads, ok := rt.contentCount(c, "SELECT COALESCE(SUM(download_count),0) FROM apps")
	if !ok {
		return
	}
	totalClicks, ok := rt.contentCount(c, "SELECT COUNT(*) FROM clicks")
	if !ok {
		return
	}
	todayClicks, ok := rt.contentCount(c, "SELECT COUNT(*) FROM clicks WHERE created_at >= CURDATE()")
	if !ok {
		return
	}
	topLinks, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT l.id, l.label, COUNT(c.id) AS cnt FROM clicks c JOIN pan_links l ON c.link_id = l.id"+
			" GROUP BY l.id ORDER BY cnt DESC LIMIT 10")
	if err != nil {
		c.dbError(err)
		return
	}
	topApps, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT a.id, a.name, a.icon, COUNT(c.id) AS cnt FROM clicks c"+
			" JOIN pan_links l ON c.link_id = l.id JOIN apps a ON l.app_id = a.id"+
			" GROUP BY a.id ORDER BY cnt DESC LIMIT 10")
	if err != nil {
		c.dbError(err)
		return
	}
	topAppsToday, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT a.id, a.name, a.icon, COUNT(c.id) AS cnt FROM clicks c"+
			" JOIN pan_links l ON c.link_id = l.id JOIN apps a ON l.app_id = a.id"+
			" WHERE c.created_at >= CURDATE() GROUP BY a.id ORDER BY cnt DESC LIMIT 10")
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("total_apps", totalApps).
		Set("total_downloads", totalDownloads).
		Set("total_clicks", totalClicks).
		Set("today_clicks", todayClicks).
		// top_links 的 id/cnt **不做 int 强转**（top_apps/top_apps_today 才转）。
		Set("top_links", contentList(topLinks, []string{"id", "label", "cnt"})).
		Set("top_apps", topAppsInt(topApps)).
		Set("top_apps_today", topAppsInt(topAppsToday)), 0, "ok", 0)
}

func topAppsInt(rows []store.Row) []any {
	out := []any{}
	for _, row := range rows {
		o := contentObj(row, []string{"id", "name", "icon", "cnt"})
		o.Put("id", rowInt(row, "id", 0))
		o.Put("cnt", rowInt(row, "cnt", 0))
		out = append(out, o)
	}
	return out
}

// ---------- contributors ----------

// contributorQQRe 对齐 api.php:2951 `/^\d{5,12}$/`（5~12 位，不是 5~11）。
var contributorQQRe = regexp.MustCompile(`^\d{5,12}$`)

// contributorAvatar 对齐硬编码的 QQ 官方头像 CDN。
func contributorAvatar(qq string) string {
	return "https://q1.qlogo.cn/g?b=qq&nk=" + phpURLEncode(qq) + "&s=100"
}

// contributorRows 是公开 contributors 与管理员 contributor_apps 共用的聚合核心。
// manage=true 时输出含 qq、不含 avatar（contributor_apps）；false 时反之（contributors）。
func (rt *Router) contributorRows(c *Ctx, sql string, manage bool) ([]any, bool) {
	rows, err := rt.accounts().QueryAll(c.R.Context(), sql)
	if err != nil {
		c.dbError(err)
		return nil, false
	}
	type agg struct {
		id    int64
		name  string
		bio   string
		names []any
	}
	order := []string{}
	m := map[string]*agg{}
	for _, row := range rows {
		qq := phpTrim(rowStr(row, "qq", ""))
		if qq == "" {
			continue
		}
		a, exists := m[qq]
		if !exists {
			a = &agg{id: rowInt(row, "id", 0), name: rowStr(row, "name", ""), bio: rowStr(row, "bio", "")}
			m[qq] = a
			order = append(order, qq)
		}
		if av, ok := row.Get("app_name"); ok && av != nil {
			a.names = append(a.names, av)
		}
	}
	out := []any{}
	for _, qq := range order {
		a := m[qq]
		names := a.names
		if names == nil {
			names = []any{}
		}
		o := phpjson.New().Set("id", a.id)
		if manage {
			o.Set("qq", qq)
		} else {
			o.Set("avatar", contributorAvatar(qq))
		}
		o.Set("name", a.name).Set("bio", a.bio).Set("app_names", names)
		out = append(out, o)
	}
	return out, true
}

func (rt *Router) handleContributors(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "contributors")
	// 公开：绝不输出 qq；app_names 只收 is_active=1 的 app；顺序由 ORDER BY 决定。
	out, ok := rt.contributorRows(c,
		"SELECT c.id, c.qq, c.name, c.bio, c.sort_order, a.name AS app_name FROM contributors c"+
			" LEFT JOIN apps a ON a.contributor_qq = c.qq AND a.is_active = 1"+
			" ORDER BY c.sort_order ASC, c.id ASC", false)
	if !ok {
		return
	}
	c.JSON(out, 0, "ok", 0)
}

func (rt *Router) handleContributorApps(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "contributor_apps")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	// 管理版含 qq、无 avatar；SQL 多选 a.id AS app_id 但白查不用（不输出）。
	out, ok := rt.contributorRows(c,
		"SELECT c.id, c.qq, c.name, c.bio, c.sort_order, a.id AS app_id, a.name AS app_name FROM contributors c"+
			" LEFT JOIN apps a ON a.contributor_qq = c.qq AND a.is_active = 1"+
			" ORDER BY c.sort_order ASC, c.id ASC", true)
	if !ok {
		return
	}
	c.JSON(out, 0, "ok", 0)
}

func (rt *Router) handleContributorCreate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "contributor_create")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	qq := phpTrim(contentStr(c, "qq", ""))
	if qq == "" || !contributorQQRe.MatchString(qq) {
		c.Error("QQ 号格式不正确", 1)
		return
	}
	name := phpTrim(contentStr(c, "name", ""))
	bio := phpTrim(contentStr(c, "bio", ""))
	order := contentInt(c, "sort_order", 0)
	v, _, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM contributors WHERE qq = ?", qq)
	if err != nil {
		c.dbError(err)
		return
	}
	if intValue(v, true) > 0 {
		c.Error("该 QQ 已在投稿名单中", 1)
		return
	}
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO contributors (qq, name, bio, sort_order) VALUES (?, ?, ?, ?)",
		qq, name, bio, order)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("id", res.LastInsertID), 0, "ok", 0)
}

func (rt *Router) handleContributorUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "contributor_update")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	var sets []string
	var args []any
	// 三个字段都 trim((string)$v)（sort_order 也 trim 成字符串后入库）；qq 不可改。
	for _, f := range []string{"name", "bio", "sort_order"} {
		v, present := contentParam(c, f)
		if !present {
			continue
		}
		sets = append(sets, f+" = ?")
		args = append(args, phpTrim(phpStr(v)))
	}
	if len(sets) == 0 {
		c.Error("没有要更新的字段", 1)
		return
	}
	args = append(args, id)
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE contributors SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

func (rt *Router) handleContributorDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "contributor_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := contentInt(c, "id", 0)
	// 不清理 apps.contributor_qq（照抄）。
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM contributors WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}
