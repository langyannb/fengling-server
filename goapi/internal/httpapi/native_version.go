package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// handleVersion 原生实现 api.php:2891 case 'version'（公开接口，不需要鉴权）。
//
// 逐字对齐三条容易踩的点：
//  1. 键序固定 version,url,update_log,update_mode,force_update,size_mb,release_date；
//  2. size_mb 走 (float) 强转，输出恒为浮点（12 → 12.0）；
//  3. settings 里没有 latest_version 这一行时，PHP 的三个变量根本没被赋值，
//     json_encode 出来是 null（不是 0 / 0.0 / ""）—— 这里用 nil 指针复刻。
func (rt *Router) handleVersion(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "version")

	version := "1.0.0"
	url := ""
	updateLog := ""
	updateMode := "internal"

	var forceUpdate *int64
	var sizeMB *float64
	var releaseDate *string

	raw, hasRow, err := rt.env.Store.SettingValue(r.Context(), "latest_version")
	if err != nil {
		// 只把原因给客户端（PHP 从不暴露文件路径），细节进日志。
		c.Log().Error("version 读 settings 失败", "err", err)
		c.Error("服务器错误: "+err.Error(), 500)
		return
	}
	if hasRow {
		// 有行：PHP 会把这三个都赋上默认值，所以 json 里一定不是 null。
		fu := int64(0)
		sm := 0.0
		rd := ""
		forceUpdate, sizeMB, releaseDate = &fu, &sm, &rd

		var cfg map[string]any
		dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
		dec.UseNumber() // 保留 int/float 形态，size_mb 才能和 PHP 一样输出 12.0
		if derr := dec.Decode(&cfg); derr == nil && cfg != nil {
			// 对应 PHP 的 `$cfg['x'] ?? $default`（键缺失/为 null 都用默认值）。
			if v, ok := cfg["version"]; ok && v != nil {
				version = phpStr(v)
			}
			if v, ok := cfg["url"]; ok && v != nil {
				url = phpStr(v)
			}
			if v, ok := cfg["update_log"]; ok && v != nil {
				updateLog = phpStr(v)
			}
			if v, ok := cfg["update_mode"]; ok && v != nil {
				updateMode = phpStr(v)
			}
			if v, ok := cfg["force_update"]; ok {
				*forceUpdate = phpInt(v)
			}
			if v, ok := cfg["size_mb"]; ok {
				*sizeMB = phpFloat(v)
			}
			if v, ok := cfg["release_date"]; ok {
				*releaseDate = phpStr(v)
			}
		}
	}

	data := phpjson.New().
		Set("version", version).
		Set("url", url).
		Set("update_log", updateLog).
		Set("update_mode", updateMode)
	if forceUpdate != nil {
		data.Set("force_update", *forceUpdate)
	} else {
		data.Set("force_update", nil)
	}
	if sizeMB != nil {
		data.Set("size_mb", *sizeMB)
	} else {
		data.Set("size_mb", nil)
	}
	if releaseDate != nil {
		data.Set("release_date", *releaseDate)
	} else {
		data.Set("release_date", nil)
	}

	c.JSON(data, 0, "ok", 0)
}
