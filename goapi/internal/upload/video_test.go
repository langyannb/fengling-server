package upload

import (
	"bytes"
	"testing"
)

// ftypBox 构造一个带 ftyp box 的文件头（前面可加 padPrime 字节的填充）。
func ftypBox(brand string, pad int) []byte {
	b := bytes.Repeat([]byte{0x00}, pad)
	b = append(b, 0x00, 0x00, 0x00, 0x18) // box size
	b = append(b, []byte("ftyp")...)
	b = append(b, []byte(brand)...)
	b = append(b, make([]byte, 16)...)
	return b
}

func TestVideoExtFromHead(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"mp4 常见位置", ftypBox("isom", 0), "mp4"},
		{"mp4 带 free box 前导", ftypBox("isom", 8), "mp4"},
		{"mov (qt  brand)", ftypBox("qt  ", 0), "mov"},
		{"m4a 纯音频不算视频", ftypBox("M4A ", 0), ""},
		{"m4b 不算视频", ftypBox("m4b ", 0), ""},
		{"m4p 不算视频", ftypBox("m4p ", 0), ""},
		{"ebml 且含 webm", append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("....webm....")...), "webm"},
		{"ebml 不含 webm", append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("....matroska")...), "mkv"},
		{"认不出", []byte("not a video at all"), ""},
		{"空", nil, ""},
		{"ftyp 在位置 0 不算(需要 pos>=4)", append([]byte("ftypisom"), make([]byte, 32)...), ""},
		{"伪造后缀无法兜底: 函数根本不看文件名", []byte("PK\x03\x04zipfile"), ""},
	}
	for _, c := range cases {
		if got := VideoExtFromHead(c.head); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestVideoExtFromHeadOnlyLooksAtFirst4K(t *testing.T) {
	// ftyp 出现在 4096 字节之后 → PHP 的 substr($raw,0,4096) 也看不到
	head := append(bytes.Repeat([]byte{'x'}, 5000), ftypBox("isom", 0)...)
	if got := VideoExtFromHead(head); got != "" {
		t.Fatalf("超出 4096 窗口不应识别, got %q", got)
	}
}

func TestVideoContentType(t *testing.T) {
	want := map[string]string{
		"mp4": "video/mp4", "mov": "video/quicktime",
		"mkv": "video/x-matroska", "webm": "video/webm",
		"": "application/octet-stream",
	}
	for ext, ctype := range want {
		if got := VideoContentType(ext); got != ctype {
			t.Fatalf("%q: got %q want %q", ext, got, ctype)
		}
	}
}

func TestVideoMetaParams(t *testing.T) {
	ok := []struct{ w, h, d, s int64 }{
		{0, 0, 0, 0},
		{10000, 10000, 3600, 2147483647},
		{1080, 1920, 60, 1024},
	}
	for _, c := range ok {
		if err := VideoMetaParams(c.w, c.h, c.d, c.s); err != nil {
			t.Fatalf("合法参数被拒: %+v -> %v", c, err)
		}
	}
	bad := []struct {
		w, h, d, s int64
		want       string
	}{
		{-1, 0, 0, 0, "视频尺寸参数不合法"},
		{0, 10001, 0, 0, "视频尺寸参数不合法"},
		{0, 0, -1, 0, "视频时长参数不合法 (最长 60 分钟)"},
		{0, 0, 3601, 0, "视频时长参数不合法 (最长 60 分钟)"},
		{0, 0, 0, -1, "视频大小参数不合法"},
		{0, 0, 0, 2147483648, "视频大小参数不合法"},
	}
	for _, c := range bad {
		err := VideoMetaParams(c.w, c.h, c.d, c.s)
		if err == nil {
			t.Fatalf("越界参数未被拒: %+v", c)
		}
		if err.Error() != c.want {
			t.Fatalf("%+v: got %q want %q", c, err.Error(), c.want)
		}
	}
}
