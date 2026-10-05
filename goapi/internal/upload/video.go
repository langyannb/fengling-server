package upload

import (
	"bytes"
	"errors"
	"strings"
)

// 视频参数兜底的三句中文（逐字照抄 api.php:3998-4000）。
var (
	ErrVideoWH       = errors.New("视频尺寸参数不合法")
	ErrVideoDuration = errors.New("视频时长参数不合法 (最长 60 分钟)")
	ErrVideoSize     = errors.New("视频大小参数不合法")
)

// headLimit 是 PHP video_detect_ext 里 substr($raw, 0, 4096) 的窗口大小。
const headLimit = 4096

// VideoExtFromHead 复刻 api.php:3955 video_detect_ext 的**文件头分支**。
//
// 与 PHP 的差异（见 README）：PHP 在文件头之前还有一层 finfo（按 mime 判
// mp4/quicktime/mkv/webm/m4v），Go 侧没有等价的 finfo；对真实容器两者结果一致，
// 差别只可能出现在「finfo 认得、但文件头不符合任何已知容器」的畸形文件上。
//
// 刻意**不接受文件名**：PHP 也不用后缀兜底（否则改名成 .mp4 就能上传伪装文件）。
func VideoExtFromHead(head []byte) string {
	if len(head) > headLimit {
		head = head[:headLimit]
	}
	// mp4 / mov：认 ftyp box（正常在 4~8 字节，少数文件前面还有 free/wide box，
	// 所以在前 4KB 里找）。brand 以 m4a/m4b/m4p 开头的是纯音频，不算视频。
	if pos := bytes.Index(head, []byte("ftyp")); pos >= 4 {
		brand := ""
		if pos+4 <= len(head) {
			end := pos + 8
			if end > len(head) {
				end = len(head)
			}
			brand = strings.ToLower(string(head[pos+4 : end]))
		}
		if !strings.HasPrefix(brand, "m4a") && !strings.HasPrefix(brand, "m4b") && !strings.HasPrefix(brand, "m4p") {
			if brand == "qt  " {
				return "mov"
			}
			return "mp4"
		}
	}
	// mkv / webm：EBML 头 1A 45 DF A3，DocType 里带 webm 就是 webm。
	if len(head) >= 4 && head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3 {
		if bytes.Contains(bytes.ToLower(head), []byte("webm")) {
			return "webm"
		}
		return "mkv"
	}
	// 认不出容器就返回空串（调用方报「只支持 mp4 / mov / mkv / webm 视频」）。
	return ""
}

// VideoContentType 对齐 api.php:3948 video_ctype。
func VideoContentType(ext string) string {
	switch ext {
	case "mp4":
		return "video/mp4"
	case "mov":
		return "video/quicktime"
	case "mkv":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	}
	return "application/octet-stream"
}

// VideoMetaParams 对齐 api.php:3992 video_meta_params 的三段范围兜底
// （客户端传值，服务端只做范围校验；参数缺失时上游按 0 传入，与 PHP 的 (int) 一致）。
func VideoMetaParams(w, h, duration, size int64) error {
	if w < 0 || w > 10000 || h < 0 || h > 10000 {
		return ErrVideoWH
	}
	if duration < 0 || duration > 3600 {
		return ErrVideoDuration
	}
	if size < 0 || size > 2147483647 {
		return ErrVideoSize
	}
	return nil
}
