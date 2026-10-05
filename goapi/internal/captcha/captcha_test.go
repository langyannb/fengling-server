package captcha

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

// 验证码字符集必须与 api.php:3414 逐字一致（少了任何字符都会改变校验通过率）。
func TestCharsetMatchesPHP(t *testing.T) {
	if Charset != "ABCDEFGHJKLMNPQRSTUVWXY3456789" {
		t.Fatalf("字符集被改动: %q", Charset)
	}
	if len(Charset) != 30 {
		t.Fatalf("字符集长度应为 30, 实际 %d", len(Charset))
	}
	for _, bad := range []byte("IOZ012") {
		if strings.IndexByte(Charset, bad) >= 0 {
			t.Fatalf("字符集不应包含易混字符 %q", bad)
		}
	}
}

func TestNewCode(t *testing.T) {
	for i := 0; i < 200; i++ {
		c := NewCode()
		if len(c) != Length {
			t.Fatalf("长度应为 %d, 实际 %q", Length, c)
		}
		for _, r := range c {
			if !strings.ContainsRune(Charset, r) {
				t.Fatalf("出现字符集外字符: %q", c)
			}
		}
	}
}

func TestImagePNGIsDecodable280x100(t *testing.T) {
	raw, err := ImagePNG("A3K9")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("不是 PNG: %x", raw[:8])
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != ImageWidth || b.Dy() != ImageHeight {
		t.Fatalf("尺寸应为 %dx%d, 实际 %dx%d", ImageWidth, ImageHeight, b.Dx(), b.Dy())
	}
}

// 空字符串不能 panic（PHP 里 strlen(”)=0 时 $len 用 max(1,…) 兜底）。
func TestImagePNGEmptyCodeDoesNotPanic(t *testing.T) {
	if _, err := ImagePNG(""); err != nil {
		t.Fatal(err)
	}
}
