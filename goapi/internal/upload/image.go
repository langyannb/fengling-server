// Package upload 实现阶段 1 的三个纯 Go 上传构件：
//
//   - 图片格式判定与压缩（对齐 api.php:150 compress_image，差异见 README 阶段 1 章节）
//   - 视频容器判定与参数兜底（对齐 api.php:3948 video_ctype / :3955 video_detect_ext / :3992 video_meta_params）
//   - multipart 流式解析（file 部件在前、文本部件在后，禁止 ParseMultipartForm）
//   - S3 SigV4 客户端（stdlib 手写，path-style，只签 host;x-amz-content-sha256;x-amz-date）
//
// 本包不依赖 httpapi / store，可独立单测。
package upload

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"

	// 只为了注册解码器：jpeg/png/gif 走标准库，webp 走 x/image（纯 Go，无 cgo）。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MIMEForImageExt 是 api.php 里那三处「扩展名 → Content-Type」映射的并集：
//
//	jpg/jpeg → image/jpeg、png → image/png、webp → image/webp、其余（gif）→ image/gif
func MIMEForImageExt(ext string) string {
	switch ext {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

var (
	// ErrNotImage 对应 PHP 里 getimagesizefromstring 返回 false 的情形
	// （文案：这不是一张有效的图片）。
	ErrNotImage = errors.New("这不是一张有效的图片")
	// ErrUnsupportedImage 对应「getimagesize 认识这个格式、但不在白名单」的情形
	// （文案由调用方按 action 决定，见契约第 2 节）。
	ErrUnsupportedImage = errors.New("图片格式不在白名单")
)

// ImageInfo 是 getimagesize 能给到的那部分信息（内容判定，不信文件名/Content-Type）。
type ImageInfo struct {
	Ext    string // jpg / png / gif / webp
	Width  int
	Height int
}

// ProbeImage 复刻 api.php 里 getimagesize + $info['mime'] 查表的语义：
// 按**内容**判定格式，返回映射后的扩展名与原始像素尺寸。
//
// 与 PHP 的差异（已知，见 README）：
//   - getimagesize 能识别 BMP/TIFF/PSD/ICO 等，PHP 会走到白名单分支报
//     「只支持 …」；Go 标准库没有这些解码器，这里用文件头识别出来并返回
//     ErrUnsupportedImage，让调用方给出**同一句**白名单文案。
//   - 头部合法但数据被截断的文件：getimagesize 与 image.DecodeConfig 都只读
//     头部，因此两者都会「通过」，后续交给 CompressImage 原样返回。
func ProbeImage(data []byte) (ImageInfo, error) {
	if len(data) == 0 {
		return ImageInfo{}, ErrNotImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil && cfg.Width > 0 && cfg.Height > 0 {
		var ext string
		switch format {
		case "jpeg":
			ext = "jpg"
		case "png":
			ext = "png"
		case "gif":
			ext = "gif"
		case "webp":
			ext = "webp"
		default:
			// Go 只注册了上面四种；理论上不可达。
			return ImageInfo{}, ErrUnsupportedImage
		}
		return ImageInfo{Ext: ext, Width: cfg.Width, Height: cfg.Height}, nil
	}
	if looksLikeOtherImage(data) {
		return ImageInfo{}, ErrUnsupportedImage
	}
	return ImageInfo{}, ErrNotImage
}

// looksLikeOtherImage 用文件头识别「getimagesize 能认出、但不在白名单」的格式，
// 目的是让报错文案与 PHP 一致（而不是退化成「这不是一张有效的图片」）。
func looksLikeOtherImage(b []byte) bool {
	switch {
	case len(b) >= 2 && b[0] == 'B' && b[1] == 'M': // BMP
		return true
	case len(b) >= 4 && (bytes.Equal(b[:4], []byte("II*\x00")) || bytes.Equal(b[:4], []byte("MM\x00*"))): // TIFF
		return true
	case len(b) >= 4 && bytes.Equal(b[:4], []byte("8BPS")): // PSD
		return true
	case len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 1 && b[3] == 0: // ICO
		return true
	}
	return false
}

// ImageResult 是一次 compress_image 的结果。
type ImageResult struct {
	Data   []byte // 要上传的字节
	Ext    string // 实际使用的扩展名（webp 需要缩放时会变成 png 或 jpg）
	MIME   string // 与 Ext 对应的 Content-Type
	Scaled bool   // 是否发生了缩放（false = 原样上传原始字节）
}

// MaxPixels 是 api.php:155 的「超过 6000 万像素不解码」保护阈值。
const MaxPixels = 60000000

// CompressImage 复刻 api.php:152-180 compress_image 的**可见语义**。
//
// 与 PHP 的差异（契约附录 C 已定，客户端不可见）：
//  1. 不需要缩放（宽 <= maxWidth）时 PHP 仍会无条件重编码，Go **原样上传原始字节**
//     （Go 没有纯 Go 的 lossy webp 编码器，重编码要么需要 cgo、要么劣化画质）；
//  2. 需要缩放时 PHP 按**原扩展名**编码（webp→WebP），Go 按**有无 alpha** 编码：
//     有 alpha → PNG、无 alpha → JPEG，key 扩展名与 Content-Type 随之改变；
//  3. PNG 压缩级别与 GD 不同、JPEG 编码器不同，字节不会与 PHP 相同（像素尺寸一致）。
func CompressImage(data []byte, ext string, maxWidth, quality int) ImageResult {
	orig := ImageResult{Data: data, Ext: ext, MIME: MIMEForImageExt(ext)}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return orig
	}
	// ① 防 GD 吃内存的同款保护：像素超阈值直接原样返回。
	if cfg.Width*cfg.Height > MaxPixels {
		return orig
	}
	// ② 只缩不放；不需要缩放 → 原样（契约附录 C 的 Go 侧决定）。
	if cfg.Width <= maxWidth {
		return orig
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return orig // ③ 解码失败 → 原样返回
	}

	nh := int(math.Round(float64(cfg.Height) * float64(maxWidth) / float64(cfg.Width)))
	if nh < 1 {
		nh = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, maxWidth, nh))
	// 插值用 CatmullRom（契约允许；像素级不要求与 GD 的 imagecopyresampled 一致）。
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)

	if hasAlpha(img) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil || buf.Len() == 0 {
			return orig
		}
		return ImageResult{Data: buf.Bytes(), Ext: "png", MIME: "image/png", Scaled: true}
	}
	var buf bytes.Buffer
	opts := &jpeg.Options{Quality: quality}
	if err := jpeg.Encode(&buf, dst, opts); err != nil || buf.Len() == 0 {
		return orig
	}
	return ImageResult{Data: buf.Bytes(), Ext: "jpg", MIME: "image/jpeg", Scaled: true}
}

// hasAlpha 判断源图是否带 alpha 通道（决定缩放后编 PNG 还是 JPEG）。
//
// 说明：Go 的 png 解码器对「无 alpha 的真彩 PNG」也返回 *image.RGBA，
// 因此这里对非调色板图按 ColorModel 判定即可 —— 结果是 png 源 → PNG、
// jpeg/webp(lossy) 源 → JPEG，与 PHP 按扩展名编码的结果一致。
func hasAlpha(img image.Image) bool {
	if p, ok := img.(*image.Paletted); ok {
		for _, c := range p.Palette {
			if _, _, _, a := c.RGBA(); a < 0xffff {
				return true
			}
		}
		return false
	}
	switch img.ColorModel() {
	case color.RGBAModel, color.NRGBAModel, color.RGBA64Model, color.NRGBA64Model,
		color.AlphaModel, color.Alpha16Model:
		return true
	}
	return false
}
