package apk

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

func openTestArchive(t *testing.T, entries []zipEntry) *apkArchive {
	t.Helper()
	a, err := openAPK(writeAPK(t, "archive.apk", buildTestAPK(t, entries)))
	if err != nil {
		t.Fatalf("打开测试 zip 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// decodeFormat 返回解码格式与尺寸，用来确认合成输出真的是 PNG。
func decodeFormat(t *testing.T, data []byte) (string, int, int) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("图标解码失败: %v", err)
	}
	b := img.Bounds()
	return format, b.Dx(), b.Dy()
}

// 文件名兜底：组内既看密度也看像素数 —— 密度只决定「先试谁」，最终取像素最多的。
func TestPickIconDensityThenPixels(t *testing.T) {
	small := makePNG(t, 48, 48, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	large := makePNG(t, 96, 96, color.RGBA{R: 0x40, G: 0x50, B: 0x60, A: 0xff})

	cases := []struct {
		name  string
		files []zipEntry
		want  string
	}{
		{
			name: "密度高且像素多",
			files: []zipEntry{
				{"res/mipmap-mdpi/ic_launcher.png", small},
				{"res/mipmap-xxxhdpi/ic_launcher.png", large},
			},
			want: "res/mipmap-xxxhdpi/ic_launcher.png",
		},
		{
			name: "低密度但像素多者胜出",
			files: []zipEntry{
				{"res/mipmap-mdpi/ic_launcher.png", large},
				{"res/mipmap-xxxhdpi/ic_launcher.png", small},
			},
			want: "res/mipmap-mdpi/ic_launcher.png",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickIcon(openTestArchive(t, c.files))
			if got == nil {
				t.Fatal("pickIcon 返回 nil")
			}
			wantEqual(t, "IconPath", got.path, c.want)
			wantEqual(t, "IconExt", got.ext, "png")
		})
	}
}

// 分组优先级 exact → round → other：round 组即使像素更小，也优先于 other 组。
func TestPickIconGroupOrder(t *testing.T) {
	small := makePNG(t, 48, 48, color.White)
	large := makePNG(t, 192, 192, color.Black)

	t.Run("round 优先于 other", func(t *testing.T) {
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher_round.png", small},
			{"res/mipmap-xxxhdpi/ic_launcher_alt.png", large},
		}))
		if got == nil {
			t.Fatal("pickIcon 返回 nil")
		}
		wantEqual(t, "IconPath", got.path, "res/mipmap-xxxhdpi/ic_launcher_round.png")
	})

	t.Run("exact 优先于 round", func(t *testing.T) {
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher_round.png", large},
			{"res/mipmap-xxxhdpi/ic_launcher.png", small},
		}))
		if got == nil {
			t.Fatal("pickIcon 返回 nil")
		}
		wantEqual(t, "IconPath", got.path, "res/mipmap-xxxhdpi/ic_launcher.png")
	})

	t.Run("只有 other 组也能选中", func(t *testing.T) {
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher_v2.png", small},
		}))
		if got == nil {
			t.Fatal("pickIcon 返回 nil")
		}
		wantEqual(t, "IconPath", got.path, "res/mipmap-xxxhdpi/ic_launcher_v2.png")
	})

	t.Run("非 launcher 命名一律不算候选", func(t *testing.T) {
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xxxhdpi/other.png", large},
			{"res/drawable/notice.png", large},
			{"assets/ic_launcher.png", large},
		}))
		if got != nil {
			t.Errorf("pickIcon = %+v, 期望 nil", got)
		}
	})
}

// monochrome 分层、解不出来的图、以及不在 res/mipmap|drawable 下的路径都要被忽略。
func TestPickIconSkipsUnusable(t *testing.T) {
	cases := []struct {
		name  string
		files []zipEntry
	}{
		{"monochrome 跳过", []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher_monochrome.png", makePNG(t, 192, 192, color.White)},
		}},
		{"内容不是图", []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher.png", []byte("这不是 PNG")},
		}},
		{"扩展名不在白名单", []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher.gif", makePNG(t, 192, 192, color.White)},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickIcon(openTestArchive(t, c.files)); got != nil {
				t.Errorf("pickIcon = %+v, 期望 nil", got)
			}
		})
	}
}

// 自适应图标：背景铺满 → 前景叠加 → 按 round(w*0.6667) 居中裁切，输出 PNG。
func TestComposeAdaptiveIcon(t *testing.T) {
	const side = 108
	fore := makePNG(t, side, side, color.RGBA{R: 0xff, A: 0xff})
	back := makePNG(t, side, side, color.RGBA{B: 0xff, A: 0xff})

	t.Run("前景+背景", func(t *testing.T) {
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher_fore.png", fore},
			{"res/mipmap-xxxhdpi/ic_launcher_back.png", back},
		}))
		if got == nil {
			t.Fatal("pickIcon 返回 nil")
		}
		wantEqual(t, "IconExt", got.ext, "png")
		wantEqual(t, "IconPath", got.path,
			"res/mipmap-xxxhdpi/ic_launcher_fore.png + res/mipmap-xxxhdpi/ic_launcher_back.png [合成裁切]")

		format, w, h := decodeFormat(t, got.data)
		wantEqual(t, "格式", format, "png")
		wantEqual(t, "边长(宽)", w, 72) // round(108*0.6667) = 72
		wantEqual(t, "边长(高)", h, 72)
		// 前景不透明且铺满，所以裁切区中心必须是前景色（前景在上层）
		img, _, err := image.Decode(bytes.NewReader(got.data))
		if err != nil {
			t.Fatalf("解码合成图失败: %v", err)
		}
		r, g, b, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
		if r <= g || r <= b {
			t.Errorf("中心像素 = (%d,%d,%d), 期望以前景红色为主", r>>8, g>>8, b>>8)
		}
	})

	t.Run("只有前景", func(t *testing.T) {
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xxxhdpi/ic_launcher_fore.png", fore},
		}))
		if got == nil {
			t.Fatal("pickIcon 返回 nil")
		}
		wantEqual(t, "IconPath", got.path,
			"res/mipmap-xxxhdpi/ic_launcher_fore.png + (无背景) [合成裁切]")
		if _, w, h := decodeFormat(t, got.data); w != 72 || h != 72 {
			t.Errorf("尺寸 = %dx%d, 期望 72x72", w, h)
		}
	})

	t.Run("背景取同密度优先", func(t *testing.T) {
		lowBack := makePNG(t, side, side, color.RGBA{G: 0xff, A: 0xff})
		got := pickIcon(openTestArchive(t, []zipEntry{
			{"res/mipmap-xhdpi/ic_launcher_back.png", lowBack}, // 密度 40
			{"res/mipmap-xxxhdpi/ic_launcher_back.png", back},  // 密度 60，与前景同密度
			{"res/mipmap-xxxhdpi/ic_launcher_fore.png", fore},
		}))
		if got == nil {
			t.Fatal("pickIcon 返回 nil")
		}
		wantEqual(t, "IconPath", got.path,
			"res/mipmap-xxxhdpi/ic_launcher_fore.png + res/mipmap-xxxhdpi/ic_launcher_back.png [合成裁切]")
	})
}

func TestPathExt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"res/mipmap-xxxhdpi/ic_launcher.png", "png"},
		{"res/2V.PNG", "PNG"}, // pathinfo 不做大小写折叠，调用方自己 ToLower
		{"res/folder.d/icon.webp", "webp"},
		{".png", ""},
		{"noext", ""},
		{"", ""},
	}
	for _, c := range cases {
		wantEqual(t, "pathExt("+c.in+")", pathExt(c.in), c.want)
	}
}

func TestImageSizeRejectsGarbage(t *testing.T) {
	if _, _, ok := imageSize([]byte("not an image at all")); ok {
		t.Error("imageSize 把垃圾数据当成了图片")
	}
	if _, _, ok := imageSize(nil); ok {
		t.Error("imageSize(nil) 返回 ok=true")
	}
	pngData := makePNG(t, 7, 11, color.White)
	if w, h, ok := imageSize(pngData); !ok || w != 7 || h != 11 {
		t.Errorf("imageSize = (%d,%d,%v), 期望 (7,11,true)", w, h, ok)
	}
}
