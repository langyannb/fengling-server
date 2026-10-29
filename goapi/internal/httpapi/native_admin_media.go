package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/jobs"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5B-2：后台「设置/视频/举报/UC/发版」12 个 action 的原生实现。
//
//   about_config_set / notice_set                —— 设置写入侧（读取侧阶段 5A 已原生）
//   admin_video_config_get / _set / admin_video_clean —— 视频配置与清理
//   crash_reports / harm_reports / _status / _delete  —— 崩溃与和谐反馈管理
//   uc_status / uc_logout                         —— UC 网盘登录态（文件语义）
//   version_update                                —— 后台发版（写 settings.latest_version）
//
// 对齐要点（逐字照抄 api.php）：
//  1. 全部先 require_admin / requireAdminRow，再解析业务参数；
//  2. 中文文案、JSON code 与 HTTP 状态码走既有的 shell.go（403→HTTP 400 等）；
//  3. SQL 文本、ORDER BY、LIMIT 与 PHP 一字不差；空集合输出 []；
//  4. 全库零事务零审计，照抄；Redis 零新增、不推事件、不加依赖。

// ---------- PHP param() 语义（带 isset 的「显式 null 视为未提供」） ----------

// phpParam 对齐 config.php:37 param()：request_body(JSON) → $_POST → $_GET，逐层用
// isset 判定。因此 JSON body 里显式传 null 时 PHP 会**继续往下一层回落**，
// 而 Ctx.Param 遇到 null 就返回 (nil,true)。本批 action 的参数默认值/校验顺序
// 与参数是否「真的提供」强相关（如 admin_video_config_set 的 max_mb 默认取旧配置），
// 必须用这个更精确的取值器。
func phpParam(c *Ctx, key string) (any, bool) {
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

// phpParamAny 取原始参数值（缺省给 def，保留 PHP 的字符串/数字形态）。
func phpParamAny(c *Ctx, key string, def any) any {
	if v, ok := phpParam(c, key); ok {
		return v
	}
	return def
}

// phpParamStr 对齐 (string)param($key, $def)。
func phpParamStr(c *Ctx, key, def string) string {
	if v, ok := phpParam(c, key); ok {
		return phpStr(v)
	}
	return def
}

// phpParamInt 对齐 (int)param($key, $def)。
func phpParamInt(c *Ctx, key string, def int64) int64 {
	if v, ok := phpParam(c, key); ok {
		return phpInt(v)
	}
	return def
}

// ---------- 设置写入：about_config_set / notice_set ----------

// handleAboutConfigSet 对齐 api.php:2873-2889。
func (rt *Router) handleAboutConfigSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "about_config_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	// PHP 用 param($k, '')，JSON body 里显式 null 同样回落到 ''。
	cfg := phpjson.New().
		Set("qq_group", phpParamAny(c, "qq_group", "")).
		Set("qq_key", phpParamAny(c, "qq_key", "")).
		Set("qq_url", phpParamAny(c, "qq_url", "")).
		Set("qq_channel", phpParamAny(c, "qq_channel", "")).
		Set("website", phpParamAny(c, "website", "")).
		Set("github", phpParamAny(c, "github", "")).
		Set("feedback", phpParamAny(c, "feedback", "")).
		Set("donate", phpParamAny(c, "donate", "")).
		Set("banner_text", phpParamAny(c, "banner_text", "")).
		Set("banner_sub", phpParamAny(c, "banner_sub", ""))
	raw := string(phpjson.Marshal(cfg))
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO `settings` (`key`, `value`) VALUES ('about_config', ?) ON DUPLICATE KEY UPDATE `value` = ?",
		raw, raw); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// handleNoticeSet 对齐 api.php:3023-3032。
func (rt *Router) handleNoticeSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "notice_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	// enabled = (int)param('enabled', 0) ? 1 : 0
	enabled := int64(0)
	if phpParamInt(c, "enabled", 0) != 0 {
		enabled = 1
	}
	cfg := phpjson.New().
		Set("content", phpParamAny(c, "content", "")).
		Set("mode", phpParamAny(c, "mode", "daily")).
		Set("enabled", enabled)
	raw := string(phpjson.Marshal(cfg))
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO `settings` (`key`, `value`) VALUES ('notice', ?) ON DUPLICATE KEY UPDATE `value` = ?",
		raw, raw); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- 视频配置：admin_video_config_get / _set ----------

// videoConfigJSON 把 video_config() 的 5 个已知键按 PHP 的插入顺序输出。
//
// 说明：PHP 的 video_config() 是 array_merge(默认值, settings 里的 JSON)，settings
// 里若存了未知键会原样保留在末尾；本实现只输出 5 个已知键，未知键不落库的正常路径
// 完全一致（写入侧永远只写这 5 个键）。
func videoConfigJSON(v store.VideoConfig) *phpjson.O {
	return phpjson.New().
		Set("enabled", int64(v.Enabled)).
		Set("max_mb", int64(v.MaxMB)).
		Set("total_limit_mb", int64(v.TotalLimitMB)).
		Set("keep_days", int64(v.KeepDays)).
		Set("auto_clean", int64(v.AutoClean))
}

// handleAdminVideoConfigGet 对齐 api.php:2041-2047。
func (rt *Router) handleAdminVideoConfigGet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_video_config_get")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	out := videoConfigJSON(rt.socialVideoConfig(c))
	out.Set("usage", rt.videoUsageStats(c))
	out.Set("server_upload_max_mb", int64(videoServerMaxMB()))
	c.JSON(out, 0, "ok", 0)
}

// handleAdminVideoConfigSet 对齐 api.php:2049-2075。
func (rt *Router) handleAdminVideoConfigSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_video_config_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	vcfg := rt.socialVideoConfig(c)

	vmax := phpParamInt(c, "max_mb", int64(vcfg.MaxMB))
	if vmax < 1 {
		c.Error("单个视频上限不能小于 1MB", 1)
		return
	}
	if vmax > 500 {
		c.Error("单个视频不能超过 500MB", 1)
		return
	}
	svrMax := int64(videoServerMaxMB())
	if svrMax > 0 && vmax > svrMax {
		c.Error(fmt.Sprintf("单个视频上限不能超过服务器上传上限 (%dMB), 请先调大 php.ini 的 upload_max_filesize", svrMax), 1)
		return
	}
	vtotal := phpParamInt(c, "total_limit_mb", int64(vcfg.TotalLimitMB))
	if vtotal < 100 {
		c.Error("总容量上限不能小于 100MB", 1)
		return
	}
	if vtotal > 100000 {
		c.Error("总容量上限不能超过 100000MB", 1)
		return
	}
	if vtotal < vmax {
		c.Error("总容量上限不能小于单个视频上限", 1)
		return
	}
	vdays := phpParamInt(c, "keep_days", int64(vcfg.KeepDays))
	if vdays < 0 {
		c.Error("保留天数不能为负数", 1)
		return
	}
	if vdays > 3650 {
		c.Error("保留天数不能超过 3650 天", 1)
		return
	}
	enabled := int64(store.VideoFlag(phpParamAny(c, "enabled", int64(vcfg.Enabled))))
	autoClean := int64(store.VideoFlag(phpParamAny(c, "auto_clean", int64(vcfg.AutoClean))))

	// video_config_save($vnew)：整份 JSON 覆写（键序 enabled,max_mb,total_limit_mb,keep_days,auto_clean）。
	saved := phpjson.New().
		Set("enabled", enabled).
		Set("max_mb", vmax).
		Set("total_limit_mb", vtotal).
		Set("keep_days", vdays).
		Set("auto_clean", autoClean)
	raw := string(phpjson.Marshal(saved))
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO `settings` (`key`, `value`) VALUES ('video_config', ?) ON DUPLICATE KEY UPDATE `value` = ?",
		raw, raw); err != nil {
		c.dbError(err)
		return
	}
	out := phpjson.New().
		Set("enabled", enabled).
		Set("max_mb", vmax).
		Set("total_limit_mb", vtotal).
		Set("keep_days", vdays).
		Set("auto_clean", autoClean)
	out.Set("usage", rt.videoUsageStats(c))
	c.JSON(out, 0, "ok", 0)
}

// ---------- admin_video_clean ----------

// objectDeleter 是清理时需要的对象存储能力（*upload.Client 天然满足）。
type objectDeleter interface {
	KeyFromURL(url string) string
	Delete(ctx context.Context, key string) error
}

func (rt *Router) objectStore() objectDeleter {
	if rt.env.S3 == nil {
		return nil
	}
	return rt.env.S3
}

type videoCleanResult struct {
	deleted int64
	freed   int64
	visited int64
	skipped string
}

// handleAdminVideoClean 对齐 api.php:2087-2097。
func (rt *Router) handleAdminVideoClean(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_video_clean")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	mode := phpParamStr(c, "mode", "over_limit")
	if mode != "over_limit" && mode != "all" {
		c.Error("清理模式不合法", 1)
		return
	}
	rows := rt.queryVideoRows(c)
	var res videoCleanResult
	if mode == "all" {
		// video_clean_all()：清空全部视频（不受 auto_clean 限制）
		res.visited = int64(len(rows))
		obj := rt.objectStore()
		for _, row := range rows {
			rt.cleanupVideoRow(c, obj, row, &res)
		}
	} else {
		// video_cleanup(true, false, false)：只按总容量裁掉最旧的，忽略保留天数与 auto_clean
		cfg := rt.socialVideoConfig(c)
		doomed, planRes := jobs.PlanCleanup(rows, cfg, true, false, false, time.Now())
		res.visited = int64(planRes.Visited)
		res.skipped = planRes.Skipped
		obj := rt.objectStore()
		for _, i := range doomed {
			rt.cleanupVideoRow(c, obj, rows[i], &res)
		}
	}
	out := phpjson.New().
		Set("deleted", res.deleted).
		Set("freed_bytes", res.freed).
		Set("visited", res.visited).
		Set("skipped", res.skipped).
		Set("mode", mode).
		Set("freed_mb", phpRound(float64(res.freed)/1048576, 2))
	out.Set("usage", rt.videoUsageStats(c))
	c.JSON(out, 0, "ok", 0)
}

// queryVideoRows 复刻 api.php:4065 与 4105 的两条查询（表序与 PHP 一致）。
// 单表出错只记日志并跳过该表（同 PHP 的 try/catch）。
func (rt *Router) queryVideoRows(c *Ctx) []store.VideoRow {
	out := []store.VideoRow{}
	for _, tb := range []string{"social_messages", "social_pm_messages"} {
		rows, err := rt.accounts().QueryAll(c.R.Context(),
			"SELECT id, video, video_size, created_at FROM `"+tb+"` "+
				"WHERE video IS NOT NULL AND video <> '' ORDER BY created_at ASC, id ASC")
		if err != nil {
			c.Log().Error("读取待清理视频行失败, 跳过该表", "table", tb, "err", err)
			continue
		}
		for _, row := range rows {
			out = append(out, store.VideoRow{
				Table:     tb,
				ID:        rowInt(row, "id", 0),
				Video:     rowStr(row, "video", ""),
				Size:      rowInt(row, "video_size", 0),
				CreatedAt: rowStr(row, "created_at", ""),
			})
		}
	}
	return out
}

// cleanupVideoRow 对齐 api.php:4118 video_cleanup_one：先删对象（失败忽略），
// 再改库；只有 UPDATE 成功才计入 deleted/freed_bytes。
func (rt *Router) cleanupVideoRow(c *Ctx, obj objectDeleter, row store.VideoRow, out *videoCleanResult) {
	if obj != nil {
		if key := obj.KeyFromURL(row.Video); key != "" {
			dctx, cancel := context.WithTimeout(c.R.Context(), 60*time.Second)
			if err := obj.Delete(dctx, key); err != nil {
				c.Log().Warn("清理视频对象失败(继续清理消息)", "key", key, "err", err)
			}
			cancel()
		}
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE `"+row.Table+"` SET video = '', content = CONCAT(content, '[视频已清理]') WHERE id = ?",
		row.ID); err != nil {
		c.Log().Error("清理视频消息失败", "table", row.Table, "id", row.ID, "err", err)
		return
	}
	out.deleted++
	out.freed += row.Size
}

// ---------- video_usage_stats / video_server_max_mb ----------

// videoUsageStats 对齐 api.php:4016-4047（键序 count,total_bytes,max_size,max_size_id,
// oldest_at,oldest_id,cleaned_count）。每张表一个 try：任一查询异常即 log 并跳过该表剩余。
func (rt *Router) videoUsageStats(c *Ctx) *phpjson.O {
	var (
		count      int64
		totalBytes int64
		maxSize    int64
		maxSizeID  int64
		oldestAt   string
		oldestID   int64
		cleaned    int64
	)
	for _, tb := range []string{"social_messages", "social_pm_messages"} {
		func() {
			row, err := rt.accounts().QueryRow(c.R.Context(),
				"SELECT COUNT(*) AS c, COALESCE(SUM(video_size), 0) AS s FROM `"+tb+"` "+
					"WHERE video IS NOT NULL AND video <> ''")
			if err != nil {
				c.Log().Error("[video_usage] "+tb, "err", err)
				return
			}
			if row != nil {
				count += rowInt(row, "c", 0)
				totalBytes += rowInt(row, "s", 0)
			}
			row, err = rt.accounts().QueryRow(c.R.Context(),
				"SELECT id, video_size FROM `"+tb+"` WHERE video IS NOT NULL AND video <> '' "+
					"ORDER BY video_size DESC LIMIT 1")
			if err != nil {
				c.Log().Error("[video_usage] "+tb, "err", err)
				return
			}
			if row != nil {
				if sz := rowInt(row, "video_size", 0); sz > maxSize {
					maxSize = sz
					maxSizeID = rowInt(row, "id", 0)
				}
			}
			row, err = rt.accounts().QueryRow(c.R.Context(),
				"SELECT id, created_at FROM `"+tb+"` WHERE video IS NOT NULL AND video <> '' "+
					"ORDER BY created_at ASC, id ASC LIMIT 1")
			if err != nil {
				c.Log().Error("[video_usage] "+tb, "err", err)
				return
			}
			if row != nil {
				at := rowStr(row, "created_at", "")
				if oldestAt == "" || at < oldestAt {
					oldestAt = at
					oldestID = rowInt(row, "id", 0)
				}
			}
			v, has, err := rt.accounts().QueryValue(c.R.Context(),
				"SELECT COUNT(*) FROM `"+tb+"` WHERE (video IS NULL OR video = '') "+
					"AND content LIKE '%[视频已清理]%'")
			if err != nil {
				c.Log().Error("[video_usage] "+tb, "err", err)
				return
			}
			if has {
				cleaned += intValue(v, has)
			}
		}()
	}
	return phpjson.New().
		Set("count", count).
		Set("total_bytes", totalBytes).
		Set("max_size", maxSize).
		Set("max_size_id", maxSizeID).
		Set("oldest_at", oldestAt).
		Set("oldest_id", oldestID).
		Set("cleaned_count", cleaned)
}

// videoServerMaxMB 对齐 api.php:3894 video_server_max_mb()。
//
// Go 进程读不到 php.ini，取值来源改为环境变量（按优先级）：
//  1. VIDEO_SERVER_MAX_MB：直接给 MB 整数；
//  2. PHP_UPLOAD_MAX_FILESIZE / PHP_POST_MAX_SIZE：形如 "150M"/"1G"，按 ini_bytes
//     换算后取小的那个 /1048576（与 PHP 的 floor(min(a,b)/1048576) 一致）。
//
// 两者都没有时返回 0（= 不限），此时 admin_video_config_set 的「不能超过服务器上传上限」
// 校验会被跳过 —— 这是本批唯一无法在 Go 侧自动复刻的 PHP ini 依赖，部署时须在
// goapi.env 里设置 VIDEO_SERVER_MAX_MB。
func videoServerMaxMB() int {
	if v := phpTrim(os.Getenv("VIDEO_SERVER_MAX_MB")); v != "" {
		return int(phpInt(v))
	}
	a := iniBytes(os.Getenv("PHP_UPLOAD_MAX_FILESIZE"))
	b := iniBytes(os.Getenv("PHP_POST_MAX_SIZE"))
	if a <= 0 && b <= 0 {
		return 0
	}
	if a <= 0 {
		return int(math.Floor(float64(b) / 1048576))
	}
	if b <= 0 {
		return int(math.Floor(float64(a) / 1048576))
	}
	smaller := a
	if b < smaller {
		smaller = b
	}
	return int(math.Floor(float64(smaller) / 1048576))
}

// iniBytes 对齐 api.php:3881-3891 ini_bytes()。
func iniBytes(val string) int64 {
	val = phpTrim(val)
	if val == "" || val == "-1" {
		return 0
	}
	unit := strings.ToLower(val[len(val)-1:])
	num := phpFloat(val)
	switch unit {
	case "g":
		return int64(math.Round(num * 1073741824))
	case "m":
		return int64(math.Round(num * 1048576))
	case "k":
		return int64(math.Round(num * 1024))
	}
	return int64(num)
}

// phpRound 复刻 PHP round($v, $precision)（默认 half away from zero）。
func phpRound(v float64, precision int) float64 {
	p := math.Pow10(precision)
	return math.Round(v*p) / p
}

// ---------- crash_reports / harm_reports ----------

// SELECT * 的列顺序 = PHP 数组键序。store.Row 是 map（丢顺序），所以按 schema.sql 的
// 建表列序重建（这两张表自建表后未再加列；若线上新增了列，PHP 会带上、本实现不会，
// 已在回报里列为未覆盖项）。
var (
	crashReportCols = []string{"id", "device", "android_version", "app_version", "stack", "ip", "created_at"}
	harmReportCols  = []string{"id", "app_id", "app_name", "content", "contact", "status", "ip", "created_at"}
)

// pdoCell 复刻 PDO 模拟预处理取值：非 NULL 一律字符串（store/row.go 顶部注释同结论），
// NULL 保持 null（TEXT/DATETIME 可空列）。
func pdoCell(r store.Row, key string) any {
	v, ok := r.Get(key)
	if !ok || v == nil {
		return nil
	}
	return phpStr(v)
}

// handleCrashReports 对齐 api.php:3049-3053。
func (rt *Router) handleCrashReports(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "crash_reports")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT * FROM crash_reports ORDER BY id DESC LIMIT 100")
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		o := phpjson.New()
		for _, col := range crashReportCols {
			o.Set(col, pdoCell(row, col))
		}
		list = append(list, o)
	}
	c.JSON(list, 0, "ok", 0)
}

// handleHarmReports 对齐 api.php:3082-3086。
func (rt *Router) handleHarmReports(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "harm_reports")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT * FROM harm_reports ORDER BY status ASC, id DESC LIMIT 200")
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		o := phpjson.New()
		for _, col := range harmReportCols {
			o.Set(col, pdoCell(row, col))
		}
		list = append(list, o)
	}
	c.JSON(list, 0, "ok", 0)
}

// handleHarmReportDelete 对齐 api.php:3088-3092（无存在性校验）。
func (rt *Router) handleHarmReportDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "harm_report_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"DELETE FROM harm_reports WHERE id = ?", phpParamInt(c, "id", 0)); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// handleHarmReportStatus 对齐 api.php:3094-3099：只有 status===1 才置 1，其余一律置 0。
func (rt *Router) handleHarmReportStatus(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "harm_report_status")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	status := int64(0)
	if phpParamInt(c, "status", 0) == 1 {
		status = 1
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE harm_reports SET status = ? WHERE id = ?", status, phpParamInt(c, "id", 0)); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- UC 网盘：uc_status / uc_logout ----------

// ucAuthPath 对齐 uc.php 的 `__DIR__ . '/uc_auth.php'`（api.php 与 uc.php 同目录 = docroot）。
func (rt *Router) ucAuthPath() string {
	return filepath.Join(rt.env.Cfg.PHPDocRoot, "uc_auth.php")
}

// loadUCAuth 对齐 UcDrive::loadAuth()：文件不存在 / 解析不出数组 → 空数组。
func (rt *Router) loadUCAuth() map[string]any {
	b, err := os.ReadFile(rt.ucAuthPath())
	if err != nil {
		return map[string]any{}
	}
	return parseUCAuthPHP(string(b))
}

var ucAuthPropRe = regexp.MustCompile(`^\s*'([^']+)'\s*=>\s*(.+?)\s*,?\s*$`)
var ucAuthIntRe = regexp.MustCompile(`^-?\d+$`)

// parseUCAuthPHP 从 var_export(array) 写出的 PHP 文件里取顶层标量键。
// uc_auth.php 的标量键恰好是 status() 需要的 cookie/ts/nickname/account。
func parseUCAuthPHP(src string) map[string]any {
	out := map[string]any{}
	for _, line := range strings.Split(src, "\n") {
		m := ucAuthPropRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		raw := phpTrim(m[2])
		switch {
		case len(raw) >= 2 && strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'"):
			out[key] = phpUnescapeSingleQuoted(raw[1 : len(raw)-1])
		case ucAuthIntRe.MatchString(raw):
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				out[key] = n
			}
		case raw == "true":
			out[key] = true
		case raw == "false":
			out[key] = false
		}
	}
	return out
}

// phpUnescapeSingleQuoted 复刻 PHP 单引号字符串：只有 \\ 与 \' 是转义。
func phpUnescapeSingleQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '\'') {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// handleUCStatus 对齐 api.php:3102-3104 + uc.php status()。
func (rt *Router) handleUCStatus(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_status")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	auth := rt.loadUCAuth()
	cookie := ""
	if v, ok := auth["cookie"]; ok {
		cookie = phpStr(v)
	}
	// PHP 是 empty($a['cookie'])：空串与 "0" 都算未登录。
	if phpFalsy(cookie) {
		c.JSON(phpjson.New().Set("logged_in", false), 0, "ok", 0)
		return
	}
	ts := int64(0)
	if v, ok := auth["ts"]; ok {
		ts = phpInt(v)
	}
	nick := ""
	if v, ok := auth["nickname"]; ok {
		nick = phpStr(v)
	}
	acct := ""
	if v, ok := auth["account"]; ok {
		acct = phpStr(v)
	}
	c.JSON(phpjson.New().
		Set("logged_in", true).
		Set("login_time", ts).
		Set("nickname", nick).
		Set("account", acct).
		Set("cookie_len", int64(len(cookie))), 0, "ok", 0)
}

// handleUCLogout 对齐 api.php:3124-3127 + uc.php clearAuth()（@unlink，失败忽略）。
func (rt *Router) handleUCLogout(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_logout")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	_ = os.Remove(rt.ucAuthPath())
	c.JSON(nil, 0, "ok", 0)
}

// ---------- version_update ----------

// handleVersionUpdate 对齐 api.php:2808-2852。
//
// 校验顺序逐字照抄：先算 size_mb（本地文件 / S3 HEAD）与 release_date，
// **最后**才判「版本号不能为空」。
func (rt *Router) handleVersionUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "version_update")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	version := phpParamStr(c, "version", "")
	urlStr := phpParamStr(c, "url", "")
	updateLog := phpParamStr(c, "update_log", "")
	updateMode := phpParamStr(c, "update_mode", "internal")
	forceUpdate := phpParamInt(c, "force_update", 0)
	sizeMB := phpFloat(phpParamAny(c, "size_mb", float64(0)))

	if sizeMB <= 0 && urlStr != "" {
		if strings.Contains(urlStr, "/uploads/apk/") {
			if u, err := neturl.Parse(urlStr); err == nil {
				filePath := rt.env.Cfg.PHPDocRoot + u.Path
				if fi, err := os.Stat(filePath); err == nil && !fi.IsDir() {
					sizeMB = phpRound(float64(fi.Size())/1048576, 1)
				}
			}
		} else if strings.HasPrefix(urlStr, rt.env.Cfg.S3PublicURL) || strings.HasPrefix(urlStr, rt.env.Cfg.S3Endpoint) {
			// S3 对象：HEAD 拿 Content-Length（不下载）
			if n := headContentLength(c.R.Context(), urlStr); n > 0 {
				sizeMB = phpRound(float64(n)/1048576, 1)
			}
		}
	}

	today := time.Now().In(config.LocalZone()).Format("2006-01-02")
	releaseDate := phpParamStr(c, "release_date", today)
	if phpFalsy(releaseDate) {
		releaseDate = today
	}
	if phpFalsy(version) {
		c.Error("版本号不能为空", 1)
		return
	}

	cfg := phpjson.New().
		Set("version", version).
		Set("url", urlStr).
		Set("update_log", updateLog).
		Set("update_mode", updateMode).
		Set("force_update", forceUpdate).
		Set("size_mb", sizeMB).
		Set("release_date", releaseDate)
	raw := string(phpjson.Marshal(cfg))
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO `settings` (`key`, `value`) VALUES ('latest_version', ?) ON DUPLICATE KEY UPDATE `value` = ?",
		raw, raw); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// headContentLength 对齐 api.php:2825-2834 的 cURL HEAD：connect 10s / 总 30s，
// 取 CONTENT_LENGTH_DOWNLOAD（<=0 视为拿不到）。
func headContentLength(ctx context.Context, urlStr string) int64 {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, urlStr, nil)
	if err != nil {
		return 0
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 {
		return resp.ContentLength
	}
	return 0
}
