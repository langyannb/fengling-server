package httpapi

// 阶段6: UC 网盘 5 个 action 改 Go 原生
//   uc_qr_create / uc_qr_poll / uc_cookie_set / uc_resolve / uc_import
// uc_status / uc_logout 已在阶段 5B-2 原生（见 native_admin_media.go）。
//
// 逐字对齐要点：
//   - api.php 的 json_error($msg, $code) 在这里统一走 c.Error(msg, code)；
//   - 上游 UC code → HTTP 映射逐 action 照抄：qr_create 一律 502；qr_poll 400→400 否则 502；
//     cookie_set 一律 400；resolve 401→401、400 或 need_pwd→400、其余 502；
//     uc_import 401→401、其余 502；
//   - compress_image 之后的 S3 对象键用的是**入参扩展名**（PHP 原文如此：webp 被压成 png
//     也仍然用 .webp 结尾），这里照抄，保证 url 与 PHP 逐字一致；
//   - resolve 的 suggest 是 PHP 在原 data 上追加的第 4 个键，必须排在 files 之后。

import (
	"net/http"
	"os"

	"github.com/langyannb/fengling-server/goapi/internal/apk"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/uc"
	"github.com/langyannb/fengling-server/goapi/internal/upload"
)

// ucPrepare 把包级配置对齐到当前部署。
//
// AuthPath 必须等于 PHP 的 `__DIR__ . '/uc_auth.php'`（api.php 与 uc.php 同目录 = docroot），
// 否则 Go 读到的是空凭据、而 PHP 面板写的是另一份文件。
// TmpDir 保持包默认（os.TempDir），与 uc.php 里 sys_get_temp_dir() 的语义一致。
func (rt *Router) ucPrepare() {
	uc.AuthPath = rt.ucAuthPath()
}

// ucFilesOf 取 resolve 结果里的文件列表（uc 包保证元素类型是 []map[string]any）。
func ucFilesOf(d map[string]any) []map[string]any {
	if v, ok := d["files"].([]map[string]any); ok {
		return v
	}
	return nil
}

// ucTruthy 对齐 PHP 的 !empty($v)：null/false/0/0.0/""/"0"/空数组都是假。
func ucTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != "" && t != "0"
	}
	return false
}

// ucOrder 按给定键序把 map 转成保序对象；缺失的键跳过（各分支返回的键不同）。
func ucOrder(data map[string]any, order []string) *phpjson.O {
	out := phpjson.New()
	for _, k := range order {
		if v, ok := data[k]; ok {
			out.Set(k, v)
		}
	}
	return out
}

var (
	ucQRCreateOrder  = []string{"token", "qr_url", "expires_in"}
	ucQRPollOrder    = []string{"state", "uc_status", "msg", "nickname"}
	ucCookieSetOrder = []string{"verified", "nickname"}
	// normFile 的键序；子树展开项在末尾多一个 parent。
	ucFileOrder = []string{"name", "size", "size_mb", "isdir", "fid", "fid_token", "ext", "updated", "parent"}
	// suggest 的键序（PHP 在 data 上追加的第 4 个键）。
	ucSuggestOrder = []string{"file", "name", "version", "size_mb", "kind", "has_apk", "images", "packages"}
	// apk 结果对象键序。
	ucImportAPKOrder = []string{"file", "size_mb", "package", "version_name", "version_code", "icon_url"}
)

// ---------- 扫码登录 ----------

// handleUCQRCreate 对齐 api.php:3106-3110。
func (rt *Router) handleUCQRCreate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_qr_create")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rt.ucPrepare()
	res := uc.QRCreate()
	if res.Code != 0 {
		c.Error(res.Msg, 502)
		return
	}
	c.JSON(ucOrder(res.Data, ucQRCreateOrder), 0, "ok", 0)
}

// handleUCQRPoll 对齐 api.php:3112-3116：code 400 原样 400，其余一律 502。
func (rt *Router) handleUCQRPoll(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_qr_poll")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rt.ucPrepare()
	res := uc.QRPoll(phpParamStr(c, "token", ""))
	if res.Code != 0 {
		code := 502
		if res.Code == 400 {
			code = 400
		}
		c.Error(res.Msg, code)
		return
	}
	c.JSON(ucOrder(res.Data, ucQRPollOrder), 0, "ok", 0)
}

// handleUCCookieSet 对齐 api.php:3118-3122：所有失败都是 400（PHP 写死 400）。
func (rt *Router) handleUCCookieSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_cookie_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rt.ucPrepare()
	res := uc.LoginByCookie(phpParamStr(c, "cookie", ""))
	if res.Code != 0 {
		c.Error(res.Msg, 400)
		return
	}
	c.JSON(ucOrder(res.Data, ucCookieSetOrder), 0, "ok", 0)
}

// ---------- 分享解析 ----------

// handleUCResolve 对齐 api.php:3129-3169。
//
// 失败映射：401 → 401；400 或 need_pwd → 400；其余 → 502。
// 成功时在 data 末尾追加 suggest：先按 rank 权重挑主文件（同权重比 size），
// 再用 guessNameVersion 猜名字/版本，最后扫一遍 files 汇总 has_apk/images/packages。
func (rt *Router) handleUCResolve(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_resolve")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rt.ucPrepare()
	res := uc.Resolve(phpParamStr(c, "url", ""), phpParamStr(c, "pwd", ""))
	if res.Code != 0 {
		code := 502
		switch {
		case res.Code == 401:
			code = 401
		case res.Code == 400 || res.NeedPwd:
			code = 400
		}
		c.Error(res.Msg, code)
		return
	}

	d := res.Data
	files := ucFilesOf(d)

	// PHP: $f['_w'] = $w; $main = $f;  同权重时比 size（严格大于才替换）。
	rank := map[string]int64{"apk": 5, "zip": 4, "rar": 4, "7z": 4, "xapk": 3, "apks": 3}
	var main map[string]any
	var mainW int64
	for _, f := range files {
		if ucTruthy(f["isdir"]) {
			continue
		}
		w2 := int64(1)
		if v, ok := rank[phpStr(f["ext"])]; ok {
			w2 = v
		}
		if main == nil || w2 > mainW || (w2 == mainW && phpInt(f["size"]) > phpInt(main["size"])) {
			main = f
			mainW = w2
		}
	}

	suggest := phpjson.New().
		Set("file", "").
		Set("name", "").
		Set("version", "").
		Set("size_mb", 0).
		Set("kind", "").
		Set("has_apk", false).
		Set("images", []any{}).
		Set("packages", []any{})
	if main != nil {
		name, ver := uc.GuessNameVersion(phpStr(main["name"]))
		suggest = phpjson.New().
			Set("file", phpStr(main["name"])).
			Set("name", name).
			Set("version", ver).
			Set("size_mb", main["size_mb"]).
			Set("kind", phpStr(main["ext"])).
			Set("has_apk", false).
			Set("images", []any{}).
			Set("packages", []any{})
	}

	images := []any{}
	packages := []any{}
	hasAPK := false
	for _, f := range files {
		if ucTruthy(f["isdir"]) {
			continue
		}
		ext := phpStr(f["ext"])
		switch ext {
		case "apk":
			hasAPK = true
			packages = append(packages, phpStr(f["name"]))
		case "zip", "rar", "7z", "xapk", "apks":
			packages = append(packages, phpStr(f["name"]))
		case "jpg", "jpeg", "png", "webp":
			images = append(images, phpStr(f["name"]))
		}
	}
	suggest.Set("has_apk", hasAPK).Set("images", images).Set("packages", packages)

	// files 数组：每项按 normFile 键序（展开项末尾带 parent）。
	outFiles := make([]any, 0, len(files))
	for _, f := range files {
		outFiles = append(outFiles, ucOrder(f, ucFileOrder))
	}

	// PHP 键序：share_key, stoken, files（来自 resolve），然后追加 suggest。
	c.JSON(phpjson.New().
		Set("share_key", phpStr(d["share_key"])).
		Set("stoken", phpStr(d["stoken"])).
		Set("files", outFiles).
		Set("suggest", suggest), 0, "ok", 0)
}

// ---------- 一键导入 ----------

// handleUCImport 对齐 api.php:3171-3245。⚠️ 会往对象存储写对象（截图 + 图标），不改数据库。
//
// PHPs 的 @set_time_limit(600) 无需对应：Go 侧靠请求上下文超时，nginx 已给 150m。
func (rt *Router) handleUCImport(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "uc_import")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	rt.ucPrepare()

	auth := uc.LoadAuth()
	if phpFalsy(auth.Cookie) {
		c.Error("UC 账号未登录，请先扫码登录", 401)
		return
	}
	want := phpParamStr(c, "kind", "all")
	raw := uc.Resolve(phpParamStr(c, "url", ""), phpParamStr(c, "pwd", ""))
	if raw.Code != 0 {
		code := 502
		if raw.Code == 401 {
			code = 401
		}
		c.Error(raw.Msg, code)
		return
	}

	key := phpStr(raw.Data["share_key"])
	stoken := phpStr(raw.Data["stoken"])
	cookie := auth.Cookie
	files := ucFilesOf(raw.Data)

	images := []any{}
	errs := []any{}
	var apkObj any // nil → JSON null，与 PHP 的 array('apk' => null) 一致

	if want == "images" || want == "all" {
		for _, f := range files {
			if ucTruthy(f["isdir"]) {
				continue
			}
			ext := phpStr(f["ext"])
			switch ext {
			case "jpg", "jpeg", "png", "webp", "gif":
			default:
				continue
			}
			if len(images) >= 15 {
				break
			}
			name := phpStr(f["name"])
			tmp := uc.DownloadToTmp(key, stoken, phpStr(f["fid"]), phpStr(f["fid_token"]), cookie)
			if tmp == "" {
				errs = append(errs, "下载失败: "+name)
				continue
			}
			data, err := os.ReadFile(tmp)
			_ = os.Remove(tmp)
			if err != nil || len(data) == 0 {
				errs = append(errs, "读取失败: "+name)
				continue
			}
			inExt := ext
			if inExt == "jpeg" {
				inExt = "jpg"
			}
			out := upload.CompressImage(data, inExt, 1600, 82)
			// PHP 用的是**入参**扩展名（不是压缩后可能变成了 png），照抄。
			objKey := upload.S3Key("screenshots", inExt)
			mime := upload.MIMEForImageExt(inExt)
			if err := rt.env.S3.PutBytes(c.R.Context(), objKey, mime, out.Data); err != nil {
				errs = append(errs, "上传失败: "+name)
				continue
			}
			images = append(images, rt.env.S3.ObjectURL(objKey))
		}
	}

	if want == "apk" || want == "all" {
		var pick map[string]any
		for _, f := range files {
			if ucTruthy(f["isdir"]) || phpStr(f["ext"]) != "apk" {
				continue
			}
			if pick == nil || phpInt(f["size"]) > phpInt(pick["size"]) {
				pick = f
			}
		}
		if pick == nil {
			errs = append(errs, "分享里没有 apk 文件")
		} else {
			name := phpStr(pick["name"])
			tmp := uc.DownloadToTmp(key, stoken, phpStr(pick["fid"]), phpStr(pick["fid_token"]), cookie)
			if tmp == "" {
				errs = append(errs, "安装包下载失败（可能超过服务器限制）")
			} else {
				info := apk.ParseFile(tmp)
				_ = os.Remove(tmp)
				iconURL := ""
				switch {
				case len(info.IconData) > 0:
					iconExt := info.IconExt
					switch iconExt {
					case "png", "webp", "jpg":
					default:
						iconExt = "png"
					}
					// 有些包里的图标是 4096x4096 原图（近 1MB），压到 512px 以内再传。
					iconRaw := upload.CompressImage(info.IconData, iconExt, 512, 90)
					objKey := upload.S3Key("icons", iconExt)
					if err := rt.env.S3.PutBytes(c.R.Context(), objKey, upload.MIMEForImageExt(iconExt), iconRaw.Data); err != nil {
						errs = append(errs, "图标上传失败, 请手动上传图标")
					} else {
						iconURL = rt.env.S3.ObjectURL(objKey)
					}
				default:
					// 提取不到时明确提示，不静默留空（用户 2026-10-02 反馈过「图标没填上」）。
					errs = append(errs, "未能从安装包中提取到图标 (资源可能被混淆), 请手动上传")
				}
				apkObj = phpjson.New().
					Set("file", name).
					Set("size_mb", pick["size_mb"]).
					Set("package", info.Package).
					Set("version_name", info.VersionName).
					Set("version_code", info.VersionCode).
					Set("icon_url", iconURL)
				if info.Error != "" {
					errs = append(errs, info.Error)
				}
			}
		}
	}

	c.JSON(phpjson.New().
		Set("images", images).
		Set("apk", apkObj).
		Set("errors", errs), 0, "ok", 0)
}
