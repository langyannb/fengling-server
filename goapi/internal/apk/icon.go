package apk

import (
	"archive/zip"
	"bytes"
	"image"
	"image/png"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"

	xdraw "golang.org/x/image/draw"

	// 图标解码器注册：与 PHP 的 getimagesizefromstring / imagecreatefromstring 支持的主要格式对齐。
	// webp 由 x/image 提供（PHP 侧要 GD 编了 webp 才认，线上就是这么跑的）。
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
)

// apkMaxEntryBytes 是单个 zip 条目的读取上限。PHP 侧没有这个限制，但 APK 里的图标与
// resources.arsc 都远小于它，加一道闸能挡住恶意 zip 的极端解压尺寸。
const apkMaxEntryBytes = 64 << 20

// ---------------------------------------------------------------- zip 访问

// apkArchive 包装 zip.Reader，提供与 ZipArchive 相同的两个原语：
// 顺序枚举条目名（numFiles / getNameIndex）与按名取内容（getFromName）。
type apkArchive struct {
	rc    *zip.ReadCloser
	names []string
	index map[string]*zip.File
}

// openAPK 打开 APK；对非 zip / 被截断的 zip 返回 error，对应 PHP 里 ZipArchive::open() !== true。
func openAPK(path string) (*apkArchive, error) {
	rc, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	a := &apkArchive{
		rc:    rc,
		names: make([]string, 0, len(rc.File)),
		index: make(map[string]*zip.File, len(rc.File)),
	}
	for _, f := range rc.File {
		a.names = append(a.names, f.Name)
		// 重名条目取第一个：libzip 的 zip_name_locate 就是取首个匹配
		if _, dup := a.index[f.Name]; !dup {
			a.index[f.Name] = f
		}
	}
	return a, nil
}

func (a *apkArchive) Close() error {
	if a.rc == nil {
		return nil
	}
	return a.rc.Close()
}

// read 等价 ZipArchive::getFromName：不存在（或读失败、条目是目录）时 ok=false。
// 空文件返回 (空切片, true)，与 PHP 里「读到 ”」区分开。
func (a *apkArchive) read(name string) ([]byte, bool) {
	f, ok := a.index[name]
	if !ok || f.FileInfo().IsDir() {
		return nil, false
	}
	rc, err := f.Open()
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	if f.UncompressedSize64 > apkMaxEntryBytes {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(rc, apkMaxEntryBytes+1))
	if err != nil || int64(len(data)) > apkMaxEntryBytes {
		return nil, false
	}
	return data, true
}

// ---------------------------------------------------------------- 图片工具

// imageSize 等价 PHP 的 getimagesizefromstring（只看文件头，不解整图）。
func imageSize(data []byte) (w, h int, ok bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// decodeImage 等价 imagecreatefromstring：解不出来就 ok=false。
func decodeImage(data []byte) (image.Image, bool) {
	if len(data) == 0 {
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	return img, true
}

// ---------------------------------------------------------------- 图标挑选

// iconResult 与 PHP 里 pickIcon/composeAdaptive 返回的数组同构。
type iconResult struct {
	data []byte
	ext  string // 直取是原扩展名；合成的一律 png
	path string // 直取是 zip 内路径；合成是 "fore + back [合成裁切]"
}

// iconCandidate 是目录里扫到的一个位图候选。
type iconCandidate struct {
	path string
	ext  string
	dens int
}

// densityWeights 的顺序就是 PHP 数组的顺序：dens 判定取「第一个命中的键」，
// 所以 xxxhdpi 必须排在 hdpi 之前（路径里同时出现两段时以先命中者为准）。
var densityWeights = []struct {
	key    string
	weight int
}{
	{"xxxhdpi", 60},
	{"xxhdpi", 50},
	{"xhdpi", 40},
	{"hdpi", 30},
	{"mdpi", 20},
	{"nodpi", 10},
}

// iconPathRe 是文件名兜底的正则（PHP 侧带 /i，这里用 (?i)）。
var iconPathRe = regexp.MustCompile(`(?i)^res/(mipmap|drawable)[^/]*/([^/]+)\.(png|webp|jpg)$`)

// iconGroupLimit 是每组最多尝试解码的候选数（PHP 里 array_slice($grp, 0, 6)）。
const iconGroupLimit = 6

// pickIcon 对齐 apk.php::pickIcon：先按文件名分组找完整图标，实在没有才合成自适应图标。
func pickIcon(a *apkArchive) *iconResult {
	var exact, round, other, fore, back []iconCandidate

	for _, n := range a.names {
		m := iconPathRe.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		base := strings.ToLower(m[2])
		dens := 0
		for _, d := range densityWeights {
			// 注意：密度是在整条路径上做**大小写敏感**的子串查找（PHP strpos 语义），
			// 和上面正则的 (?i) 不一致 —— 这是 PHP 侧既有行为，保持不动。
			if strings.Contains(n, d.key) {
				dens = d.weight
				break
			}
		}
		item := iconCandidate{path: n, ext: strings.ToLower(m[3]), dens: dens}

		// 自适应图标的分层资源不能单独当图标用
		if strings.Contains(base, "monochrome") {
			continue
		}
		if strings.Contains(base, "fore") {
			fore = append(fore, item)
			continue
		}
		if strings.Contains(base, "back") {
			back = append(back, item)
			continue
		}

		isLauncher := strings.Contains(base, "ic_launcher") || strings.Contains(base, "app_icon") ||
			strings.Contains(base, "ic_icon") || base == "icon"
		if !isLauncher {
			continue
		}
		if base == "ic_launcher" || base == "app_icon" || base == "icon" {
			exact = append(exact, item)
			continue
		}
		if strings.Contains(base, "round") {
			round = append(round, item)
			continue
		}
		other = append(other, item)
	}

	// 1) 完整图标：ic_launcher.png 优先级最高，其次圆形，再其它命名
	for _, grp := range [][]iconCandidate{exact, round, other} {
		if got := bestOfGroup(a, grp); got != nil {
			return got
		}
	}
	// 2) 只有自适应分层时：背景 + 前景合成，再按 2/3 边长裁切
	return composeAdaptive(a, fore, back)
}

// bestOfGroup 在同组候选里取「能解码且像素最多」的那张：density 降序后只看前 6 个，
// 像素数严格大于才替换（同像素时保留密度更高者）。
func bestOfGroup(a *apkArchive, grp []iconCandidate) *iconResult {
	if len(grp) == 0 {
		return nil
	}
	// PHP 的 usort 作用在值传递的副本上；这里也先拷贝再排序，别改调用方的切片
	sorted := append([]iconCandidate(nil), grp...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].dens > sorted[j].dens })
	if len(sorted) > iconGroupLimit {
		sorted = sorted[:iconGroupLimit]
	}

	var best *iconResult
	bestPx := -1
	for _, c := range sorted {
		data, ok := a.read(c.path)
		if !ok || len(data) == 0 {
			continue
		}
		w, h, ok := imageSize(data)
		if !ok {
			continue
		}
		px := w * h
		if px > bestPx {
			bestPx = px
			best = &iconResult{data: data, ext: c.ext, path: c.path}
		}
	}
	return best
}

// composeAdaptive 对齐 apk.php::composeAdaptive：背景铺满画布 → 前景叠加 →
// 按 Android 规范取中央 round(w*0.6667) 作为最终图标，输出 PNG。
func composeAdaptive(a *apkArchive, fore, back []iconCandidate) *iconResult {
	if len(fore) == 0 {
		return nil
	}
	fs := append([]iconCandidate(nil), fore...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].dens > fs[j].dens })
	f := fs[0]

	// 背景优先取与前景同密度的；没有就取密度最高的那张
	var b *iconCandidate
	for i := range back {
		if back[i].dens == f.dens {
			b = &back[i]
			break
		}
	}
	if b == nil && len(back) > 0 {
		bs := append([]iconCandidate(nil), back...)
		sort.SliceStable(bs, func(i, j int) bool { return bs[i].dens > bs[j].dens })
		b = &bs[0]
	}

	fdata, ok := a.read(f.path)
	if !ok {
		return nil
	}
	fi, ok := decodeImage(fdata)
	if !ok {
		return nil
	}
	fb := fi.Bounds()
	w, h := fb.Dx(), fb.Dy()
	if w < 8 || h < 8 {
		return nil
	}

	// 画布零值就是全透明（等价 PHP 的 imagefill 透明底 + alpha=127）
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	if b != nil {
		if bdata, ok := a.read(b.path); ok {
			if bi, ok := decodeImage(bdata); ok {
				scaleOver(canvas, bi, w, h)
			}
		}
	}
	scaleOver(canvas, fi, w, h)

	side := int(math.Round(float64(w) * 0.6667))
	if side <= 0 {
		return nil
	}
	x := int(math.Round(float64(w-side) / 2))
	y := int(math.Round(float64(h-side) / 2))
	crop := image.NewRGBA(image.Rect(0, 0, side, side))
	xdraw.Draw(crop, crop.Bounds(), canvas, image.Pt(x, y), xdraw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, crop); err != nil || buf.Len() == 0 {
		return nil
	}

	backPath := "(无背景)"
	if b != nil {
		backPath = b.path
	}
	return &iconResult{
		data: buf.Bytes(),
		ext:  "png",
		path: f.path + " + " + backPath + " [合成裁切]",
	}
}

// scaleOver 等价 PHP 的 imagecopyresampled($dst, $src, 0,0,0,0, w,h, sw,sh) 配 alphablending(true)：
// 双线性重采样后按 source-over 叠加。
func scaleOver(dst *image.RGBA, src image.Image, w, h int) {
	xdraw.BiLinear.Scale(dst, image.Rect(0, 0, w, h), src, src.Bounds(), xdraw.Over, nil)
}
