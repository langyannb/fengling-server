// Package captcha 复刻 api.php:3412 captcha_generate() 与 api.php:3432 captcha_image()。
//
// 说明：GD 画出来的 PNG 与 Go 的光栅化结果**不可能逐字节相同**（字符角度、干扰像素、
// 椭圆弧本身每次都是随机的，PHP 自己的两次调用也不会相同）。这里保证的是同一套
// 可见结构与可读性：280x100、背景 #F6F7FB、5 条椭圆弧、140 个随机噪点、字符
// 26 + i*step 的横向排布、字号 34、随机 -12..12 度旋转，以及同一组随机取值范围。
// token / code / expires_in 的语义与 PHP 完全一致（code 仍由 captcha_check 校验）。
package captcha

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Charset 对齐 api.php:3414：30 个字符，刻意剔除了 I O Z 0 1 2 等易混字符。
const Charset = "ABCDEFGHJKLMNPQRSTUVWXY3456789"

// Length 是验证码位数（api.php 里固定 4）。
const Length = 4

// DefaultFont 对齐 api.php:47 的 CAPTCHA_FONT。
const DefaultFont = "/usr/share/fonts/truetype/lato/Lato-Bold.ttf"

// ImageWidth / ImageHeight 对齐 api.php:3436-3437 的 280x100。
const (
	ImageWidth  = 280
	ImageHeight = 100
)

// NewCode 生成 4 位验证码（每个字符都在 Charset 内等概率，同 random_int）。
func NewCode() string {
	b := make([]byte, 0, Length)
	for i := 0; i < Length; i++ {
		b = append(b, Charset[rand.IntN(len(Charset))])
	}
	return string(b)
}

// ImagePNG 画一张验证码 PNG，返回字节（与 captcha_image() 的 PNG 内容对应）。
func ImagePNG(code string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, ImageWidth, ImageHeight))
	fillRect(img, 0, 0, ImageWidth, ImageHeight, color.RGBA{R: 246, G: 247, B: 251, A: 255})

	// 干扰曲线（浅色，不遮挡字符）
	for i := 0; i < 5; i++ {
		c := color.RGBA{
			R: uint8(rnd(200, 232)),
			G: uint8(rnd(200, 232)),
			B: uint8(rnd(215, 245)),
			A: 255,
		}
		drawEllipseArc(img, rnd(0, ImageWidth), rnd(0, ImageHeight), rnd(80, 240), rnd(50, 130), c)
	}
	// 噪点
	for i := 0; i < 140; i++ {
		c := color.RGBA{
			R: uint8(rnd(170, 235)),
			G: uint8(rnd(170, 235)),
			B: uint8(rnd(170, 235)),
			A: 255,
		}
		img.Set(rnd(0, ImageWidth-1), rnd(0, ImageHeight-1), c)
	}

	face, scale := loadFace()
	n := len(code)
	if n < 1 {
		n = 1
	}
	step := (ImageWidth - 56) / n
	for i := 0; i < len(code); i++ {
		// 深色字符，与浅色背景/干扰形成强对比
		c := color.RGBA{
			R: uint8(rnd(15, 80)),
			G: uint8(rnd(15, 80)),
			B: uint8(rnd(105, 185)),
			A: 255,
		}
		drawGlyph(img, face, scale, rune(code[i]), 26+i*step, rnd(62, 76), float64(rnd(-12, 12)), c)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rnd 对齐 random_int($min, $max)：闭区间。
func rnd(min, max int) int {
	if max <= min {
		return min
	}
	return min + rand.IntN(max-min+1)
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.Set(x, y, c)
		}
	}
}

// drawEllipseArc 近似 GD 的 imagearc($im, $cx, $cy, $w, $h, 0, 360, $c)：整圈椭圆描边。
func drawEllipseArc(img *image.RGBA, cx, cy, w, h int, c color.RGBA) {
	rx, ry := float64(w)/2, float64(h)/2
	steps := 720
	for i := 0; i < steps; i++ {
		t := 2 * math.Pi * float64(i) / float64(steps)
		x := int(math.Round(float64(cx) + rx*math.Cos(t)))
		y := int(math.Round(float64(cy) + ry*math.Sin(t)))
		if x < 0 || y < 0 || x >= ImageWidth || y >= ImageHeight {
			continue
		}
		img.Set(x, y, c)
	}
}

// glyphScale 是把内置位图字体放大到与 PHP「字号 34」相当视觉大小的系数：
// Face7x13 的字身高 13px，13 * 2.6 ≈ 34px。
//
// 为什么不用线上那个 TrueType（/usr/share/fonts/truetype/lato/Lato-Bold.ttf）：
// 解析 TTF 需要 golang.org/x/image/font/opentype，而它依赖 golang.org/x/text，
// 本机模块缓存里没有 x/text（离线无法编译、无法自证 go test 全绿）。
// 代价只是字形观感：位图字体放大后是硬边像素，而 GD 的 imagettftext 是抗锯齿的；
// 尺寸、颜色、字符位置/角度、干扰元素与 PHP 同一套参数。若父 agent 想完全对齐观感，
// 在 CI（有网）加 `golang.org/x/text` 后把这里换成 opentype.NewFace(…Size:34…) 即可。
const glyphScale = 2.6

func loadFace() (font.Face, float64) {
	return basicfont.Face7x13, glyphScale
}

// drawGlyph 把单个字符以随机角度、可能放大的方式贴到 (x, y)（y 是字符中心线）。
func drawGlyph(dst *image.RGBA, face font.Face, scale float64, ch rune, x, y int, angleDeg float64, c color.RGBA) {
	if face == nil {
		return
	}
	adv := font.MeasureString(face, string(ch)).Ceil()
	m := face.Metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()
	gw, gh := adv+4, ascent+descent+4
	if gw <= 4 || gh <= 4 {
		return
	}
	tmp := image.NewRGBA(image.Rect(0, 0, gw, gh))
	d := &font.Drawer{
		Dst:  tmp,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(2, 2+ascent),
	}
	d.DrawString(string(ch))

	cw, chh := float64(gw)*scale, float64(gh)*scale
	rad := angleDeg * math.Pi / 180
	sin, cos := math.Sincos(rad)
	half := int(math.Ceil(0.5*math.Hypot(cw, chh))) + 1
	cxf, cyf := float64(x), float64(y)
	scx, scy := cw/2, chh/2
	for dy := y - half; dy <= y+half; dy++ {
		if dy < 0 || dy >= ImageHeight {
			continue
		}
		for dx := x - half; dx <= x+half; dx++ {
			if dx < 0 || dx >= ImageWidth {
				continue
			}
			// 逆向旋转回源图坐标（最近邻采样）。
			rx, ry := float64(dx)-cxf, float64(dy)-cyf
			sx := (rx*cos+ry*sin)/scale + scx
			sy := (-rx*sin+ry*cos)/scale + scy
			ix, iy := int(math.Floor(sx)), int(math.Floor(sy))
			if ix < 0 || iy < 0 || ix >= gw || iy >= gh {
				continue
			}
			_, _, _, a := tmp.At(ix, iy).RGBA()
			if a == 0 {
				continue
			}
			blend(dst, dx, dy, c, uint32(a))
		}
	}
}

// blend 按 alpha 把颜色混到目标像素上（GD 的 imagettftext 也是抗锯齿混合）。
func blend(dst *image.RGBA, x, y int, c color.RGBA, a uint32) {
	old := dst.RGBAAt(x, y)
	f := float64(a) / 65535
	nr := float64(c.R)*f + float64(old.R)*(1-f)
	ng := float64(c.G)*f + float64(old.G)*(1-f)
	nb := float64(c.B)*f + float64(old.B)*(1-f)
	dst.SetRGBA(x, y, color.RGBA{R: uint8(nr + 0.5), G: uint8(ng + 0.5), B: uint8(nb + 0.5), A: 255})
}
