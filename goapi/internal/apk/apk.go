// Package apk 用纯 Go 复刻 fengling-server 里 apk.php + arsc.php 的 APK 解析能力：
// 读包名 / versionName / versionCode / 应用名，并挑出最高清的启动图标（必要时合成自适应图标）。
//
// 为什么不用 aapt / apkanalyzer：线上跑在 Termux 上，没有 Android SDK；PHP 侧本来就是
// 靠 ZipArchive + 自写二进制解析做到的零依赖，Go 侧迁移必须**逐字对齐**它的输出，
// 否则前端拿到的 icon_path / icon_ext 会变，缓存键和已入库的图标都会错位。
//
// 对齐原则：
//   - 永不 panic、永不返回 error —— 一切失败写进 Info.Error，对应 PHP 的 $out['error']；
//   - 所有二进制读取都是「越界即 0」，被截断的 APK 只会得到空字段；
//   - 图标选择的两条路径（resources.arsc 反查 / 文件名兜底）与 PHP 完全同序同判据。
package apk

import "strings"

// Info 是 ParseFile 的结果，字段与 PHP ApkParser::parse() 返回的数组一一对应。
type Info struct {
	Package     string // 包名，如 com.example.app
	VersionName string // 版本名（字符串）
	VersionCode int64  // 版本号（整数，PHP 侧是 unpack('V') 的原始值）
	Label       string // <application android:label> 的字符串值
	IconData    []byte // 图标原始字节；nil 表示没挑到
	IconExt     string // 直取是 png/webp/jpg/jpeg；合成的一定是 png；默认 "png"
	IconPath    string // 直取是 zip 内路径；合成是 "fore + back [合成裁切]"
	Error       string // 非空表示这次解析失败，其余字段为零值
}

// ParseFile 对齐 PHP 的 ApkParser::parse(string $file): array。
//
// 语义：永不 panic、永不返回 error；所有失败写进 Info.Error（PHP 用 $out['error'] 表达）。
func ParseFile(path string) Info {
	// PHP 的 $out 初始化里 icon_ext 就是 'png'，所以错误返回也带着它，这里保持一致：
	// 调用方哪怕拿到 Error 也能安全地把 IconExt 拼进响应。
	out := Info{IconExt: "png"}

	a, err := openAPK(path)
	if err != nil {
		out.Error = "APK 打开失败"
		return out
	}
	defer a.Close()

	manifest, ok := a.read("AndroidManifest.xml")
	if !ok || len(manifest) == 0 {
		// PHP 这里只置 error 然后继续往下跑文件名兜底（$m 未定义 → 反查被跳过）。
		// Go 侧按「有错就返回零值 + Error」处理：连 Manifest 都没有的包，包名/版本全读不出来，
		// 再挑一张来源说不清的图标只会让调用方误解字段含义。
		out.Error = "APK 内没有 AndroidManifest.xml"
		return out
	}

	m := parseManifest(manifest)
	out.Package = m.pkg
	out.VersionName = m.versionName
	out.VersionCode = m.versionCode
	out.Label = m.label

	icon := resolveIconByResID(a, m.iconRes)
	if icon == nil {
		icon = pickIcon(a)
	}
	if icon != nil {
		out.IconData = icon.data
		out.IconExt = icon.ext
		out.IconPath = icon.path
	}
	return out
}

// resolveIconByResID 对齐 PHP 的「manifest 里 android:icon 的资源 ID → resources.arsc → 真实文件路径」链路。
//
// 三个连续的放弃条件（缺 arsc / 扩展名不是位图 / 内容解不出图）都返回 nil，交给文件名兜底：
//   - 很多包把 res/ 下的文件名混淆成 2V.png 这种短名，这时只有这条链路能拿到真图标；
//   - 但混淆包也可能把 icon 指到 adaptive-icon 的 xml，xml 不是位图，必须让兜底去挑分层图。
func resolveIconByResID(a *apkArchive, resID uint32) *iconResult {
	if resID == 0 {
		return nil
	}
	arsc, ok := a.read("resources.arsc")
	if !ok || len(arsc) == 0 {
		return nil
	}
	path := newArscParser(arsc).resolveFile(resID)
	if path == "" {
		return nil
	}
	ext := strings.ToLower(pathExt(path))
	switch ext {
	case "png", "webp", "jpg", "jpeg":
	default:
		return nil // 自适应图标 xml 等：不接受
	}
	data, ok := a.read(path)
	if !ok || len(data) == 0 {
		return nil
	}
	if _, _, ok := imageSize(data); !ok {
		return nil // 扩展名对但内容不是图（占位符 / 损坏文件）
	}
	return &iconResult{data: data, ext: ext, path: path}
}

// pathExt 等价 PHP 的 pathinfo($p, PATHINFO_EXTENSION)：取最后一个 '.' 之后的部分，
// 以点开头的文件名（如 ".png"）没有扩展名。
func pathExt(p string) string {
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[i+1:]
	}
	return ""
}
