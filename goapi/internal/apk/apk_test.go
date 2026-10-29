package apk

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// wantEqual 让断言报错时能直接看出是哪一项不符。
func wantEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, 期望 %v", what, got, want)
	}
}

func mustIconSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("图标数据解不出尺寸: %v", err)
	}
	return cfg.Width, cfg.Height
}

const launcherIconPath = "res/mipmap-xxxhdpi/ic_launcher.png"

// 完整包：manifest（UTF-8 池）里 icon 走资源引用，resources.arsc 反查回真实文件路径。
func TestParseFileFullUTF8(t *testing.T) {
	icon := makePNG(t, 192, 192, color.RGBA{R: 0x11, G: 0x66, B: 0xcc, A: 0xff})
	apkBytes := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8:        true,
			Package:     "com.example.demo",
			VersionName: "1.2.3",
			VersionCode: 10203,
			Label:       "演示应用",
			IconRes:     0x7f010000,
		})},
		{"resources.arsc", buildTestARSC([]arscFixtureEntry{
			{resID: 0x7f010000, density: 640, value: launcherIconPath},
		}, true)},
		{launcherIconPath, icon},
	})

	got := ParseFile(writeAPK(t, "full-utf8.apk", apkBytes))
	wantEqual(t, "Error", got.Error, "")
	wantEqual(t, "Package", got.Package, "com.example.demo")
	wantEqual(t, "VersionName", got.VersionName, "1.2.3")
	wantEqual(t, "VersionCode", got.VersionCode, int64(10203))
	wantEqual(t, "Label", got.Label, "演示应用")
	wantEqual(t, "IconExt", got.IconExt, "png")
	wantEqual(t, "IconPath", got.IconPath, launcherIconPath)
	if len(got.IconData) == 0 {
		t.Fatal("IconData 为空，期望拿到 arsc 反查到的图标字节")
	}
	w, h := mustIconSize(t, got.IconData)
	wantEqual(t, "图标宽", w, 192)
	wantEqual(t, "图标高", h, 192)
}

// 同上但 manifest 与 arsc 的字符串池都是 UTF-16：中文 label 必须逐字还原。
func TestParseFileFullUTF16(t *testing.T) {
	icon := makePNG(t, 144, 144, color.RGBA{R: 0xff, G: 0x88, B: 0x00, A: 0xff})
	apkBytes := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8:        false,
			Package:     "com.example.utf16",
			VersionName: "2.0.0-beta",
			VersionCode: 200,
			Label:       "风铃分享库",
			IconRes:     0x7f010000,
		})},
		{"resources.arsc", buildTestARSC([]arscFixtureEntry{
			{resID: 0x7f010000, density: 480, value: "res/mipmap/ic_icon.webp"},
		}, false)},
		{"res/mipmap/ic_icon.webp", icon},
	})

	got := ParseFile(writeAPK(t, "full-utf16.apk", apkBytes))
	wantEqual(t, "Error", got.Error, "")
	wantEqual(t, "Package", got.Package, "com.example.utf16")
	wantEqual(t, "VersionName", got.VersionName, "2.0.0-beta")
	wantEqual(t, "VersionCode", got.VersionCode, int64(200))
	wantEqual(t, "Label", got.Label, "风铃分享库")
	wantEqual(t, "IconExt", got.IconExt, "webp") // 直取文件，扩展名原样保留
	if w, _ := mustIconSize(t, got.IconData); w != 144 {
		t.Errorf("图标宽 = %d, 期望 144", w)
	}
}

// 没有 AndroidManifest.xml：置 error，包名/版本/标签保持零值，但**仍要**走文件名兜底挑图标。
// 这是 PHP 的真实行为：$m 未定义 → !empty($m['icon_res']) 为假 → 跳过 arsc 反查 → 继续 pickIcon。
// uc_import 只看 icon_data 是否非空，所以这条路径决定了「混淆包/残缺包能不能显示图标」。
func TestParseFileNoManifest(t *testing.T) {
	apkBytes := buildTestAPK(t, []zipEntry{
		{launcherIconPath, makePNG(t, 192, 192, color.White)},
	})
	got := ParseFile(writeAPK(t, "no-manifest.apk", apkBytes))
	wantEqual(t, "Error", got.Error, "APK 内没有 AndroidManifest.xml")
	wantEqual(t, "Package", got.Package, "")
	wantEqual(t, "VersionName", got.VersionName, "")
	wantEqual(t, "VersionCode", got.VersionCode, int64(0))
	wantEqual(t, "Label", got.Label, "")
	// 图标兜底必须与 PHP 一致：这里能挑到 ic_launcher.png
	wantEqual(t, "IconPath", got.IconPath, launcherIconPath)
	wantEqual(t, "IconExt", got.IconExt, "png")
	if w, _ := mustIconSize(t, got.IconData); w != 192 {
		t.Errorf("图标宽 = %d, 期望 192", w)
	}
}

// 没有 AndroidManifest.xml 且没有任何候选图标：只有 Error，图标字段保持空。
func TestParseFileNoManifestNoIcon(t *testing.T) {
	apkBytes := buildTestAPK(t, []zipEntry{
		{"classes.dex", []byte("dex\n035\x00")},
		{"res/drawable/bg.png", makePNG(t, 64, 64, color.Black)}, // 不是 launcher 命名，兜底不选它
	})
	got := ParseFile(writeAPK(t, "no-manifest-no-icon.apk", apkBytes))
	wantEqual(t, "Error", got.Error, "APK 内没有 AndroidManifest.xml")
	wantEqual(t, "Package", got.Package, "")
	wantEqual(t, "IconPath", got.IconPath, "")
	wantEqual(t, "IconExt", got.IconExt, "png")
	if got.IconData != nil {
		t.Errorf("IconData = %d 字节, 期望 nil", len(got.IconData))
	}
}

// 打不开的 zip：截断 / 非 zip / 空文件都必须是「APK 打开失败」，且不 panic。
func TestParseFileBrokenInput(t *testing.T) {
	good := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8: true, Package: "com.example.demo", VersionName: "1.0", VersionCode: 1,
		})},
	})

	cases := []struct {
		name string
		data []byte
	}{
		{"截断的 zip", good[:len(good)/2]},
		{"纯文本", []byte("这不是一个 zip 文件，只是一段普通文本")},
		{"空文件", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseFile(writeAPK(t, "broken.apk", c.data))
			wantEqual(t, "Error", got.Error, "APK 打开失败")
			wantEqual(t, "Package", got.Package, "")
			wantEqual(t, "IconExt", got.IconExt, "png") // PHP 的 $out 默认值
		})
	}
}

// versionName 被写成整数（dataType=0x10）时，PHP 会回退成 (string)$data。
func TestParseFileVersionNameIntegerFallback(t *testing.T) {
	apkBytes := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8:             true,
			Package:          "com.example.intver",
			VersionCode:      42,
			VersionNameAsInt: true,
			Label:            "整数版本",
		})},
	})
	got := ParseFile(writeAPK(t, "int-version.apk", apkBytes))
	wantEqual(t, "Error", got.Error, "")
	wantEqual(t, "VersionName", got.VersionName, "42")
	wantEqual(t, "VersionCode", got.VersionCode, int64(42))
}

// manifest 的 icon 资源反查不可用时，必须回落到文件名兜底。
func TestParseFileIconFallsBackWhenArscUnusable(t *testing.T) {
	icon := makePNG(t, 192, 192, color.White)
	manifest := buildManifestAXML(manifestFixture{
		UTF8: true, Package: "com.example.fallback", VersionName: "1.0",
		VersionCode: 1, Label: "兜底", IconRes: 0x7f010000,
	})

	cases := []struct {
		name  string
		files []zipEntry
	}{
		{
			// arsc 里根本没有 0x7f010000 这个条目
			name: "arsc 无对应条目",
			files: []zipEntry{
				{"resources.arsc", buildTestARSC([]arscFixtureEntry{
					{resID: 0x7f010001, density: 0, value: "res/other.png"},
				}, true)},
			},
		},
		{
			// arsc 指向自适应图标的 xml，不是位图 → 放弃
			name: "arsc 指向 xml",
			files: []zipEntry{
				{"resources.arsc", buildTestARSC([]arscFixtureEntry{
					{resID: 0x7f010000, density: 0, value: "res/mipmap-anydpi-v26/ic_launcher.xml"},
				}, true)},
				{"res/mipmap-anydpi-v26/ic_launcher.xml", []byte("<adaptive-icon/>")},
			},
		},
		{
			// 扩展名是 png 但内容不是图 → 放弃
			name: "arsc 指向损坏文件",
			files: []zipEntry{
				{"resources.arsc", buildTestARSC([]arscFixtureEntry{
					{resID: 0x7f010000, density: 0, value: "res/2V.png"},
				}, true)},
				{"res/2V.png", []byte("这不是 PNG 数据")},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entries := append([]zipEntry{{"AndroidManifest.xml", manifest}}, c.files...)
			entries = append(entries, zipEntry{launcherIconPath, icon})
			got := ParseFile(writeAPK(t, "fallback.apk", buildTestAPK(t, entries)))
			wantEqual(t, "Error", got.Error, "")
			wantEqual(t, "IconPath", got.IconPath, launcherIconPath)
			wantEqual(t, "IconExt", got.IconExt, "png")
			if w, _ := mustIconSize(t, got.IconData); w != 192 {
				t.Errorf("图标宽 = %d, 期望 192", w)
			}
		})
	}
}

// arsc 里同一资源 ID 有多个密度变体时取密度最高的那份。
func TestParseFileArscPicksHighestDensity(t *testing.T) {
	apkBytes := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8: true, Package: "com.example.dens", VersionName: "1.0",
			VersionCode: 1, Label: "密度", IconRes: 0x7f010000,
		})},
		{"resources.arsc", buildTestARSC([]arscFixtureEntry{
			{resID: 0x7f010000, density: 160, value: "res/mipmap-mdpi/ic_launcher.png"},
			{resID: 0x7f010000, density: 640, value: launcherIconPath},
		}, true)},
		{"res/mipmap-mdpi/ic_launcher.png", makePNG(t, 48, 48, color.White)},
		{launcherIconPath, makePNG(t, 192, 192, color.White)},
	})
	got := ParseFile(writeAPK(t, "arsc-dens.apk", apkBytes))
	wantEqual(t, "IconPath", got.IconPath, launcherIconPath)
}

// arsc 的 TYPE_REFERENCE 递归解引用：0x7f020000 → 0x7f030000 → 字符串路径。
func TestParseFileArscReferenceChain(t *testing.T) {
	apkBytes := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8: true, Package: "com.example.ref", VersionName: "1.0",
			VersionCode: 1, Label: "引用", IconRes: 0x7f020000,
		})},
		{"resources.arsc", buildTestARSC([]arscFixtureEntry{
			{resID: 0x7f020000, density: 0, dataType: typeReference, data: 0x7f030000},
			{resID: 0x7f030000, density: 0, value: "res/ab.png"},
		}, true)},
		{"res/ab.png", makePNG(t, 128, 128, color.White)},
	})
	got := ParseFile(writeAPK(t, "ref-chain.apk", apkBytes))
	wantEqual(t, "IconPath", got.IconPath, "res/ab.png")
	wantEqual(t, "IconExt", got.IconExt, "png")
}

// 一个候选图标都没有：不报错，图标字段留空（IconExt 仍是 PHP 的默认 "png"）。
func TestParseFileWithoutAnyIcon(t *testing.T) {
	apkBytes := buildTestAPK(t, []zipEntry{
		{"AndroidManifest.xml", buildManifestAXML(manifestFixture{
			UTF8: true, Package: "com.example.noicon", VersionName: "3.1",
			VersionCode: 31, Label: "无图标",
		})},
		{"assets/data.bin", []byte{1, 2, 3}},
	})
	got := ParseFile(writeAPK(t, "no-icon.apk", apkBytes))
	wantEqual(t, "Error", got.Error, "")
	wantEqual(t, "Package", got.Package, "com.example.noicon")
	wantEqual(t, "IconPath", got.IconPath, "")
	wantEqual(t, "IconExt", got.IconExt, "png")
	if got.IconData != nil {
		t.Errorf("IconData = %d 字节, 期望 nil", len(got.IconData))
	}
}
