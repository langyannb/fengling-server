package upload

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// makePNG 生成 w*h 的 PNG；alpha=true 时带一个半透明像素（走 hasAlpha 分支）。
func makePNG(t *testing.T, w, h int, alpha bool) []byte {
	t.Helper()
	var img image.Image
	if alpha {
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				m.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 128})
			}
		}
		img = m
	} else {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				m.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 30, A: 255})
			}
		}
		img = m
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetRGBA(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 253), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}}
	m := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, m, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeDims(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("结果不是可解码的图片: %v", err)
	}
	return cfg.Width, cfg.Height
}

// 路径 1：不需要缩放 → 原样返回（Go 侧决定，见 README 阶段 1 差异说明）。
func TestCompressImageNoScaleKeepsOriginalBytes(t *testing.T) {
	src := makePNG(t, 100, 50, false)
	got := CompressImage(src, "png", 512, 90)
	if got.Scaled {
		t.Fatal("100 宽不应触发缩放")
	}
	if !bytes.Equal(got.Data, src) {
		t.Fatal("不需要缩放时必须原样上传原始字节")
	}
	if got.Ext != "png" || got.MIME != "image/png" {
		t.Fatalf("ext/mime=%s/%s", got.Ext, got.MIME)
	}

	// 边界：宽 == maxWidth 也不缩放（PHP 是 `$w > $maxWidth`）
	exact := makePNG(t, 512, 100, false)
	if r := CompressImage(exact, "png", 512, 90); r.Scaled {
		t.Fatal("宽恰好等于 maxWidth 不应缩放")
	}
}

// 路径 2：需要缩放 + 有 alpha → PNG（扩展名/MIME 随输出格式变）。
func TestCompressImageScaledPNGKeepsAlpha(t *testing.T) {
	src := makePNG(t, 1200, 600, true)
	got := CompressImage(src, "png", 512, 90)
	if !got.Scaled {
		t.Fatal("1200 > 512 应该缩放")
	}
	if got.Ext != "png" || got.MIME != "image/png" {
		t.Fatalf("ext/mime=%s/%s", got.Ext, got.MIME)
	}
	w, h := decodeDims(t, got.Data)
	if w != 512 || h != 256 {
		t.Fatalf("尺寸=%dx%d, 期望 512x256", w, h)
	}
	img, _, err := image.Decode(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, a := img.At(10, 10).RGBA()
	if a == 0xffff {
		t.Fatal("带 alpha 的源图缩放后应保留透明")
	}
}

// 路径 2b：需要缩放 + 无 alpha + 源是 webp 扩展名 → 输出 JPEG（Go 侧差异点）。
func TestCompressImageScaledToJPEG(t *testing.T) {
	src := makeJPEG(t, 2000, 1000)
	// 用 "webp" 扩展名模拟「PHP 会编 WebP、Go 只能编 JPEG」的那条路径。
	got := CompressImage(src, "webp", 1600, 82)
	if !got.Scaled {
		t.Fatal("2000 > 1600 应该缩放")
	}
	if got.Ext != "jpg" || got.MIME != "image/jpeg" {
		t.Fatalf("无 alpha 缩放后应为 jpg/image/jpeg, 实得 %s/%s", got.Ext, got.MIME)
	}
	w, h := decodeDims(t, got.Data)
	if w != 1600 || h != 800 {
		t.Fatalf("尺寸=%dx%d, 期望 1600x800", w, h)
	}
}

// 路径 3：头部合法但数据截断 → 解码失败 → 原样返回（PHP 同样回退原始字节）。
func TestCompressImageDecodeFailureReturnsOriginal(t *testing.T) {
	full := makePNG(t, 2000, 1000, false)
	if len(full) < 400 {
		t.Fatalf("样本太小: %d", len(full))
	}
	truncated := full[:200] // 保留签名 + IHDR，IDAT 不完整
	// 先确认头部确实可读（否则测的不是「解码失败」而是「头部失败」）
	if _, err := ProbeImage(truncated); err != nil {
		t.Fatalf("前提不成立, 截断样本头部不可读: %v", err)
	}
	got := CompressImage(truncated, "png", 512, 90)
	if got.Scaled {
		t.Fatal("解码失败不应标记为已缩放")
	}
	if !bytes.Equal(got.Data, truncated) {
		t.Fatal("解码失败必须原样返回")
	}
	if got.Ext != "png" {
		t.Fatalf("ext=%s", got.Ext)
	}
}

// 路径 3b：像素数超 6000 万 → 不解码、原样返回（对齐 api.php:155）。
func TestCompressImageTooManyPixelsReturnsOriginal(t *testing.T) {
	// 拿一张合法小 PNG，把 IHDR 里的宽高改成 9000x8000 (=7200 万像素)。
	// 只读头部的 DecodeConfig 不会校验 IDAT，正好复刻 getimagesize 的行为。
	src := append([]byte(nil), makePNG(t, 8, 8, false)...)
	if len(src) < 33 {
		t.Fatal("样本太短")
	}
	putUint32(src[16:20], 9000)
	putUint32(src[20:24], 8000)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || cfg.Width != 9000 || cfg.Height != 8000 {
		t.Skipf("无法构造超大尺寸头部(DecodeConfig: %v %dx%d), 跳过", err, cfg.Width, cfg.Height)
	}
	got := CompressImage(src, "png", 512, 90)
	if got.Scaled {
		t.Fatal("超大像素不应解码缩放")
	}
	if !bytes.Equal(got.Data, src) {
		t.Fatal("超大像素必须原样返回")
	}
}

func putUint32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

// ---------- ProbeImage（getimagesize 语义） ----------

func TestProbeImageFormats(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", makePNG(t, 40, 20, false), "png"},
		{"jpeg", makeJPEG(t, 40, 20), "jpg"},
		{"gif", makeGIF(t, 40, 20), "gif"},
	}
	for _, c := range cases {
		info, err := ProbeImage(c.data)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if info.Ext != c.ext || info.Width != 40 || info.Height != 20 {
			t.Fatalf("%s: %+v", c.name, info)
		}
	}
}

func TestProbeImageRejectsNonImage(t *testing.T) {
	if _, err := ProbeImage([]byte("hello world")); !errors.Is(err, ErrNotImage) {
		t.Fatalf("任意文本应为 ErrNotImage, 实得 %v", err)
	}
	if _, err := ProbeImage(nil); !errors.Is(err, ErrNotImage) {
		t.Fatalf("空数据应为 ErrNotImage, 实得 %v", err)
	}
	// 结构像 webp 但内容非法：PHP getimagesize 也认不出 → 同一句「这不是一张有效的图片」
	bogus := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8L"), make([]byte, 16)...)
	if _, err := ProbeImage(bogus); !errors.Is(err, ErrNotImage) {
		t.Fatalf("畸形 webp 应为 ErrNotImage, 实得 %v", err)
	}
}

func TestProbeImageUnsupportedFormats(t *testing.T) {
	// getimagesize 认识 BMP/TIFF/PSD/ICO：PHP 会走到白名单分支报「只支持 …」；
	// Go 没有解码器，但用文件头识别成 ErrUnsupportedImage，让调用方给出同一句文案。
	cases := map[string][]byte{
		"bmp":  append([]byte("BM"), make([]byte, 40)...),
		"tiff": append([]byte("II*\x00"), make([]byte, 40)...),
		"psd":  append([]byte("8BPS"), make([]byte, 40)...),
		"ico":  append([]byte{0, 0, 1, 0}, make([]byte, 40)...),
	}
	for name, data := range cases {
		if _, err := ProbeImage(data); !errors.Is(err, ErrUnsupportedImage) {
			t.Fatalf("%s 应为 ErrUnsupportedImage, 实得 %v", name, err)
		}
	}
}

func TestMIMEForImageExt(t *testing.T) {
	want := map[string]string{
		"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
		"webp": "image/webp", "gif": "image/gif", "apk": "application/octet-stream",
	}
	for ext, m := range want {
		if got := MIMEForImageExt(ext); got != m {
			t.Fatalf("%s: %s != %s", ext, got, m)
		}
	}
}
