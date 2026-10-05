package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
	"github.com/langyannb/fengling-server/goapi/internal/upload"
)

// 本文件是阶段 1 的六个 native action 的分发入口。
//
// 三条贯穿全文件的原则：
//  1. **不用 r.ParseMultipartForm**：Android 客户端先发 file、后发 video_* 文本部件，
//     所以统一走 upload.ReadMultipart（file 流式落临时文件 → 读完剩余部件 → 再上传）；
//  2. 临时文件一律 `defer os.Remove`；
//  3. 所有中文报错文案逐字照抄 api.php（见 .go002-contract.md 第 2 节）。

// ---------- 公共前置 ----------

// currentUserOr401 对齐 api.php:3369 current_user_or_401()。
// 返回 nil 表示已经写过响应（500 或 401），调用方直接 return。
func (rt *Router) currentUserOr401(c *Ctx) *store.User {
	me, err := rt.env.Store.CurrentUser(c.R.Context(), c.Token())
	if err != nil {
		c.Log().Error("鉴权查询失败", "err", err)
		c.Error("服务器错误: "+err.Error(), 500)
		return nil
	}
	if me == nil {
		c.Error("登录已失效", 401)
		return nil
	}
	return me
}

// requireAdmin 对齐 api.php:3263 require_admin()：token 空 → 未登录(401)；
// 查不到用户 → 登录已失效(401)；role 非 admin → 没有权限, 仅管理员可操作(403)。
func (rt *Router) requireAdmin(c *Ctx) *store.User {
	if c.Token() == "" {
		c.Error("未登录", 401)
		return nil
	}
	me, err := rt.env.Store.CurrentUser(c.R.Context(), c.Token())
	if err != nil {
		c.Log().Error("鉴权查询失败", "err", err)
		c.Error("服务器错误: "+err.Error(), 500)
		return nil
	}
	if me == nil {
		c.Error("登录已失效", 401)
		return nil
	}
	if me.Role != "admin" {
		c.Error("没有权限, 仅管理员可操作", 403)
		return nil
	}
	return me
}

// allowUpload 是契约第 6 节要求的「每人每 10 分钟最多 30 次上传」限流。
//
// 注意：这是**相对 PHP 的新增行为**（api.php 没有上传限流），所以阈值做成可配
// （UPLOAD_RATE_LIMIT / UPLOAD_RATE_WINDOW，置 0 即关闭），双跑对比时可按需关掉。
// Redis 不可用时 store.Redis.Allow 会 fail-open 放行（绝不因 Redis 挂掉打死上传）。
func (rt *Router) allowUpload(c *Ctx, uid int64) bool {
	if rt.env.Redis == nil || rt.env.Cfg.UploadRateLimit <= 0 {
		return true
	}
	if !rt.env.Redis.Allow(c.R.Context(), uid, "upload", rt.env.Cfg.UploadRateLimit, rt.env.Cfg.UploadRateWindow) {
		c.Error("上传太频繁, 请稍后再试", 1)
		return false
	}
	return true
}

// readUpload 是流式 multipart 的唯一入口。
// 第二个返回值 false 表示已经写过错误响应。
func (rt *Router) readUpload(c *Ctx, maxBytes int64) (*upload.MultipartResult, bool) {
	res, err := upload.ReadMultipart(c.R, "file", rt.env.Cfg.UploadTmpDir, maxBytes)
	if err != nil {
		if errors.Is(err, upload.ErrNotMultipart) {
			// 不是 multipart（或没有 boundary）：交给调用方按「没有收到文件」处理，
			// 与 PHP 里 $_FILES 为空一致。
			return res, true
		}
		c.Log().Warn("multipart 解析中断", "err", err)
		c.Error("读取文件失败", 1)
		return nil, false
	}
	// 文本部件交给 Param 使用（等价 PHP 的 $_POST，优先于 $_GET）。
	c.setMultipart(res.Fields)
	return res, true
}

// s3Ready 兜住「S3 客户端没装好」的情况（正常启动流程里不可能发生）。
func (rt *Router) s3Ready(c *Ctx) bool {
	if rt.env.S3 == nil {
		c.Log().Error("S3 客户端未初始化")
		c.Error("上传失败, 请稍后重试", 1)
		return false
	}
	return true
}

// ---------- user_avatar / social_image_upload ----------

// imageSpec 描述两个图片上传 action 的差异，其余逻辑完全共用。
type imageSpec struct {
	action       string
	maxBytes     int64
	tooLargeMsg  string
	whitelistMsg string
	allowGIF     bool
	maxWidth     int
	quality      int
	dir          string
	needOrigin   bool // 返回压缩前的原始尺寸（social_image_upload）
	banCheck     bool // is_active 校验（social_image_upload）
	avatarUpdate bool // 回写 users.avatar（user_avatar）
}

func (rt *Router) handleImageUpload(w http.ResponseWriter, r *http.Request, spec imageSpec) {
	c := rt.env.newCtxStreaming(w, r, spec.action)
	me := rt.currentUserOr401(c)
	if me == nil {
		return
	}
	// 与 PHP 同构：current_user() 的 SQL 自带 is_active=1，所以「不是 active」的
	// 用户根本进不到这里（PHP 里这条也是死分支），保留判定只为逐字对齐语义。
	if spec.banCheck && me.IsActive != 1 {
		c.Error("你已被封禁", 403)
		return
	}
	if !rt.allowUpload(c, me.ID) {
		return
	}
	if !rt.s3Ready(c) {
		return
	}

	res, ok := rt.readUpload(c, spec.maxBytes)
	if !ok {
		return
	}
	if res.File == nil {
		c.Error("未收到文件", 1)
		return
	}
	defer os.Remove(res.File.Path)

	if res.File.Size > spec.maxBytes {
		c.Error(spec.tooLargeMsg, 1)
		return
	}
	data, err := os.ReadFile(res.File.Path)
	if err != nil || len(data) == 0 {
		c.Error("读取文件失败", 1)
		return
	}

	info, perr := upload.ProbeImage(data)
	switch {
	case errors.Is(perr, upload.ErrNotImage):
		c.Error("这不是一张有效的图片", 1)
		return
	case perr != nil:
		c.Error(spec.whitelistMsg, 1)
		return
	}
	if info.Ext == "gif" && !spec.allowGIF {
		c.Error(spec.whitelistMsg, 1)
		return
	}

	out := upload.CompressImage(data, info.Ext, spec.maxWidth, spec.quality)
	key := upload.S3Key(spec.dir, out.Ext)
	if err := rt.env.S3.PutBytes(c.R.Context(), key, out.MIME, out.Data); err != nil {
		c.Log().Warn("对象存储上传失败", "key", key, "err", err)
		c.Error("上传失败, 请稍后重试", 1)
		return
	}
	url := rt.env.S3.ObjectURL(key)

	if spec.avatarUpdate {
		if _, err := rt.env.Store.DB().ExecContext(c.R.Context(),
			"UPDATE users SET avatar = ? WHERE id = ?", url, me.ID); err != nil {
			c.Log().Error("回写用户头像失败", "err", err)
			c.Error("服务器错误: "+err.Error(), 500)
			return
		}
	}

	out2 := phpjson.New().Set("url", url)
	if spec.needOrigin {
		// PHP 的 width/height 是**压缩前**的原始尺寸（api.php:1007 先取 $info[0/1]）。
		out2 = out2.Set("width", int64(info.Width)).Set("height", int64(info.Height))
	}
	c.JSON(out2, 0, "ok", 0)
}

// handleUserAvatar 对应 api.php:318-339。
func (rt *Router) handleUserAvatar(w http.ResponseWriter, r *http.Request) {
	rt.handleImageUpload(w, r, imageSpec{
		action:       "user_avatar",
		maxBytes:     10 * 1024 * 1024,
		tooLargeMsg:  "头像不能超过 10MB",
		whitelistMsg: "只支持 jpg / png / gif / webp 图片",
		allowGIF:     true,
		maxWidth:     512,
		quality:      90,
		dir:          "avatars",
		avatarUpdate: true,
	})
}

// handleSocialImageUpload 对应 api.php:992-1012。
func (rt *Router) handleSocialImageUpload(w http.ResponseWriter, r *http.Request) {
	rt.handleImageUpload(w, r, imageSpec{
		action:       "social_image_upload",
		maxBytes:     10 * 1024 * 1024,
		tooLargeMsg:  "图片不能超过 10MB",
		whitelistMsg: "只支持 jpg / png / webp 图片",
		allowGIF:     false,
		maxWidth:     1600,
		quality:      82,
		dir:          "chat",
		needOrigin:   true,
		banCheck:     true,
	})
}

// ---------- social_video_upload ----------

// handleSocialVideoUpload 对应 api.php:1019-1064。
func (rt *Router) handleSocialVideoUpload(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtxStreaming(w, r, "social_video_upload")
	me := rt.currentUserOr401(c)
	if me == nil {
		return
	}
	if me.IsActive != 1 {
		c.Error("你已被封禁", 403)
		return
	}
	if !rt.allowUpload(c, me.ID) {
		return
	}
	// 顺序对齐 PHP：先读配置判开关，再碰文件；S3 客户端没装好属于本机配置故障。
	vcfg := rt.env.Store.VideoConfig(c.R.Context())
	if vcfg.Enabled != 1 {
		c.Error("视频消息功能未开启", 1)
		return
	}
	if !rt.s3Ready(c) {
		return
	}
	maxBytes := int64(vcfg.MaxMB) * 1024 * 1024

	res, ok := rt.readUpload(c, maxBytes)
	if !ok {
		return
	}
	if res.File == nil {
		// PHP：$_FILES 为空且 CONTENT_LENGTH>0，说明请求体超了 post_max_size；
		// 否则就是真的没带文件。
		if r.ContentLength > 0 {
			c.Error("文件超过服务器上传限制, 请压缩后再传", 1)
			return
		}
		c.Error("未收到文件", 1)
		return
	}
	defer os.Remove(res.File.Path)

	if res.File.Size > maxBytes {
		c.Error(fmt.Sprintf("视频不能超过 %dMB", vcfg.MaxMB), 1)
		return
	}
	head, err := res.File.Head(4096)
	if err != nil || len(head) == 0 {
		c.Error("读取文件失败", 1)
		return
	}
	ext := upload.VideoExtFromHead(head)
	if ext == "" {
		c.Error("只支持 mp4 / mov / mkv / webm 视频", 1)
		return
	}
	// 参数在 file 部件**之后**才发出，所以必须等所有部件读完才能校验（契约第 1 节）。
	vw := c.ParamInt("video_w", 0)
	vh := c.ParamInt("video_h", 0)
	vd := c.ParamInt("video_duration", 0)
	vs := c.ParamInt("video_size", 0)
	if err := upload.VideoMetaParams(vw, vh, vd, vs); err != nil {
		c.Error(err.Error(), 1)
		return
	}

	key := upload.S3Key("chat", ext)
	fh, err := res.File.Open()
	if err != nil {
		c.Error("读取文件失败", 1)
		return
	}
	// 流式 PUT：SHA256 是落临时文件时顺带算出来的，与 PHP s3_upload_bytes 的
	// 真实 payload 签名语义一致（不是 UNSIGNED-PAYLOAD）。
	putErr := rt.env.S3.PutStream(c.R.Context(), key, upload.VideoContentType(ext), fh, res.File.Size, res.File.SHA256)
	_ = fh.Close()
	if putErr != nil {
		c.Log().Warn("对象存储上传失败", "key", key, "err", putErr)
		c.Error("上传失败, 请稍后重试", 1)
		return
	}

	// 上传成功后顺手清理一次（等价 api.php:1056 video_cleanup_if_needed()）。
	cleaned := 0
	if rt.env.Cleaner != nil {
		cleaned = rt.env.Cleaner.Cleanup(c.R.Context()).Deleted
	}

	c.JSON(phpjson.New().
		Set("url", rt.env.S3.ObjectURL(key)).
		Set("size", res.File.Size).
		Set("width", vw).
		Set("height", vh).
		Set("duration", vd).
		Set("cleaned", int64(cleaned)), 0, "ok", 0)
}

// ---------- upload / upload_apk（管理员） ----------

// maxUploadBytes 是 ReadMultipart 的硬上限：admin upload 的两种类型里最大是 200MB。
// 具体文案仍由各自的 maxSize 判断给出，所以这里只防「无限写盘」。
const maxUploadBytes = 200*1024*1024 + 1

// handleUpload 对应 api.php:2658-2682。
func (rt *Router) handleUpload(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtxStreaming(w, r, "upload")
	me := rt.requireAdmin(c)
	if me == nil {
		return
	}
	if !rt.allowUpload(c, me.ID) {
		return
	}
	if !rt.s3Ready(c) {
		return
	}

	res, ok := rt.readUpload(c, maxUploadBytes)
	if !ok {
		return
	}
	if res.File == nil {
		c.Error("未收到文件", 1)
		return
	}
	defer os.Remove(res.File.Path)

	// type 是 param()：JSON body → $_POST(multipart 文本部件) → $_GET，
	// 所以要在读完部件之后取（admin 后台两种传法都有）。
	isApk := c.ParamStr("type", "") == "apk"
	ext := upload.ExtFromFilename(res.File.Filename)
	allowed := []string{"jpg", "jpeg", "png", "gif", "webp"}
	if isApk {
		allowed = []string{"apk"}
	}
	if !containsStr(allowed, ext) {
		c.Error("不支持的文件类型: "+ext, 1)
		return
	}
	maxSize := int64(10 * 1024 * 1024)
	if isApk {
		maxSize = 200 * 1024 * 1024
	}
	if res.File.Size > maxSize {
		if isApk {
			c.Error("文件不能超过 200MB", 1)
		} else {
			c.Error("文件不能超过 10MB", 1)
		}
		return
	}

	dir := "images"
	mime := upload.MIMEForImageExt(ext)
	if isApk {
		dir = "files"
		mime = "application/vnd.android.package-archive"
	}

	size := res.File.Size
	if isApk {
		// APK 不压缩：直接流式 PUT（PHP 这里会把整个文件读进内存，见 README 差异说明）。
		key := upload.S3Key(dir, ext)
		fh, err := res.File.Open()
		if err != nil {
			c.Error("读取文件失败", 1)
			return
		}
		putErr := rt.env.S3.PutStream(c.R.Context(), key, mime, fh, size, res.File.SHA256)
		_ = fh.Close()
		if putErr != nil {
			c.Log().Warn("对象存储上传失败", "key", key, "err", putErr)
			c.Error("上传对象存储失败", 1)
			return
		}
		c.JSON(phpjson.New().
			Set("url", rt.env.S3.ObjectURL(key)).
			Set("filename", key).
			Set("size", size), 0, "ok", 0)
		return
	}

	data, err := os.ReadFile(res.File.Path)
	if err != nil {
		c.Error("读取文件失败", 1)
		return
	}
	// PHP：gif 不压缩；jpg/jpeg/png/webp 走 compress_image(1600, 80)。
	if ext != "gif" {
		out := upload.CompressImage(data, ext, 1600, 80)
		data, mime = out.Data, out.MIME
		if out.Ext != ext {
			ext = out.Ext // webp 需要缩放时会变成 png/jpg（见 README 差异说明）
		}
	}
	key := upload.S3Key(dir, ext)
	if err := rt.env.S3.PutBytes(c.R.Context(), key, mime, data); err != nil {
		c.Log().Warn("对象存储上传失败", "key", key, "err", err)
		c.Error("上传对象存储失败", 1)
		return
	}
	c.JSON(phpjson.New().
		Set("url", rt.env.S3.ObjectURL(key)).
		Set("filename", key).
		Set("size", int64(len(data))), 0, "ok", 0)
}

// handleUploadApk 对应 api.php:2756-2805：仅 .apk、上限 200MB、流式 PUT +
// x-amz-content-sha256: UNSIGNED-PAYLOAD（与 PHP 完全一致）。
func (rt *Router) handleUploadApk(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtxStreaming(w, r, "upload_apk")
	me := rt.requireAdmin(c)
	if me == nil {
		return
	}
	if !rt.allowUpload(c, me.ID) {
		return
	}
	if !rt.s3Ready(c) {
		return
	}

	res, ok := rt.readUpload(c, maxUploadBytes)
	if !ok {
		return
	}
	if res.File == nil {
		c.Error("未收到文件", 1)
		return
	}
	defer os.Remove(res.File.Path)

	size := res.File.Size
	if size <= 0 || size > 200*1024*1024 {
		c.Error("文件大小无效 (最大 200MB)", 1)
		return
	}
	if upload.ExtFromFilename(res.File.Filename) != "apk" {
		c.Error("仅支持 .apk 文件", 1)
		return
	}

	key := upload.APKKey(time.Now())
	fh, err := res.File.Open()
	if err != nil {
		c.Error("读取文件失败", 1)
		return
	}
	putErr := rt.env.S3.PutStream(c.R.Context(), key, "application/vnd.android.package-archive",
		fh, size, upload.UnsignedPayload)
	_ = fh.Close()
	if putErr != nil {
		c.Log().Warn("APK 对象存储上传失败", "key", key, "err", putErr)
		// 逐字对齐 PHP：`上传对象存储失败 (HTTP <code>)`（网络层错误时 code=0）。
		c.Error(fmt.Sprintf("上传对象存储失败 (HTTP %d)", upload.StatusOf(putErr)), 1)
		return
	}
	c.JSON(phpjson.New().
		Set("url", rt.env.S3.ObjectURL(key)).
		Set("size", size), 0, "ok", 0)
}

// ---------- video_config ----------

// handleVideoConfig 对应 api.php:1014-1017：无需登录，只吐 enabled / max_mb。
func (rt *Router) handleVideoConfig(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "video_config")
	vcfg := rt.env.Store.VideoConfig(c.R.Context())
	c.JSON(phpjson.New().
		Set("enabled", int64(vcfg.Enabled)).
		Set("max_mb", int64(vcfg.MaxMB)), 0, "ok", 0)
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
