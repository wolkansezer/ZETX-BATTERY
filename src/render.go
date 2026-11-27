//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type fontSet struct {
	text, bold, icon *opentype.Font
	hasIcons         bool
	cache            map[string]font.Face
}

func loadFont(names ...string) *opentype.Font {
	dir := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	for _, n := range names {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			if f, err := opentype.Parse(b); err == nil {
				return f
			}
		}
	}
	return nil
}

func loadFonts() *fontSet {
	fs := &fontSet{cache: map[string]font.Face{}}
	fs.text = loadFont("segoeui.ttf")
	fs.bold = loadFont("seguisb.ttf", "segoeuib.ttf")
	fs.icon = loadFont("SegoeIcons.ttf", "segmdl2.ttf")
	if fs.text == nil {
		fs.text, _ = opentype.Parse(goregular.TTF)
	}
	if fs.bold == nil {
		fs.bold, _ = opentype.Parse(gobold.TTF)
	}
	fs.hasIcons = fs.icon != nil
	return fs
}

func (fs *fontSet) face(f *opentype.Font, tag string, size float64) font.Face {
	key := fmt.Sprintf("%s-%.2f", tag, size)
	if fc, ok := fs.cache[key]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil
	}
	fs.cache[key] = fc
	return fc
}

const (
	glyphHeadset  = ''
	glyphMouse    = ''
	glyphKeyboard = ''
	glyphOther    = ''
	glyphBolt     = ''
)

func kindGlyph(k Kind) rune {
	switch k {
	case KindHeadset:
		return glyphHeadset
	case KindMouse:
		return glyphMouse
	case KindKeyboard:
		return glyphKeyboard
	}
	return glyphOther
}

type renderOpts struct {
	Height    int
	Scale     float64
	Light     bool
	Bg        bool
	Shadow    bool
	Clock     bool
	Threshold int
}

func fx(f float64) fixed.Int26_6 { return fixed.Int26_6(math.Round(f * 64)) }

func measure(face font.Face, s string) float64 { return float64(font.MeasureString(face, s)) / 64 }

func renderBar(fs *fontSet, devs []Device, o renderOpts) *image.RGBA {
	s := o.Scale
	h := o.Height
	textFace := fs.face(fs.text, "t", 13.5*s)
	subFace := fs.face(fs.bold, "b", 8.5*s)
	clockFace := fs.face(fs.text, "c", 12*s)
	var iconFace, boltFace font.Face
	if fs.hasIcons {
		iconFace = fs.face(fs.icon, "i", 16*s)
		boltFace = fs.face(fs.icon, "bo", 10*s)
	}

	fg := color.NRGBA{255, 255, 255, 255}
	low := color.NRGBA{255, 99, 88, 255}
	green := color.NRGBA{108, 203, 95, 255}
	if o.Light {
		fg = color.NRGBA{20, 20, 20, 255}
		low = color.NRGBA{209, 52, 56, 255}
		green = color.NRGBA{16, 124, 16, 255}
	}
	dim := func(c color.NRGBA) color.NRGBA { c.A = 110; return c }

	type item struct {
		dev                Device
		iconW, subW, boltW float64
		label              string
		w                  float64
	}
	pad := 6 * s
	if o.Bg {
		pad = 12 * s
	}
	gap := 16 * s
	items := []item{}
	total := pad
	for i, d := range devs {
		it := item{dev: d, label: fmt.Sprintf("%d%%", d.Percent)}
		if d.Percent < 0 {
			it.label = "--"
		}
		if iconFace != nil {
			it.iconW = measure(iconFace, string(kindGlyph(d.Kind)))
		} else {
			it.iconW = measure(subFace, d.Kind.Label()[:1]) + 2*s
		}
		if d.Index > 0 {
			it.subW = measure(subFace, fmt.Sprint(d.Index)) + 1*s
		}
		if d.Charging && boltFace != nil {
			it.boltW = measure(boltFace, string(glyphBolt)) + 1*s
		}
		it.w = it.iconW + it.subW + 5*s + it.boltW + measure(textFace, it.label)
		if i > 0 {
			total += gap
		}
		total += it.w
		items = append(items, it)
	}
	now := time.Now()
	tStr, dStr := now.Format("15:04"), now.Format("02.01.2006")
	clockW := 0.0
	if o.Clock {
		clockW = math.Max(measure(clockFace, tStr), measure(clockFace, dStr))
		if len(items) > 0 {
			total += gap + 4*s
		}
		total += clockW
	}
	if len(items) == 0 && !o.Clock {
		total += measure(textFace, "Cihaz yok")
	}
	total += pad
	w := int(math.Ceil(total))
	if w < 1 {
		w = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	if o.Bg {
		bg := color.NRGBA{24, 24, 24, 215}
		if o.Light {
			bg = color.NRGBA{243, 243, 243, 225}
		}
		fillRoundRect(img, bg, 8*s)
	}

	capH := float64(textFace.Metrics().CapHeight) / 64
	if capH <= 0 {
		capH = 0.7 * 13.5 * s
	}
	mid := float64(h) / 2
	baseline := mid + capH/2

	drawStr := func(face font.Face, c color.NRGBA, x, y float64, str string) {
		if o.Shadow {
			sh := color.NRGBA{0, 0, 0, uint8(float64(c.A) * 0.6)}
			d := font.Drawer{Dst: img, Src: image.NewUniform(sh), Face: face, Dot: fixed.Point26_6{X: fx(x + 1), Y: fx(y + 1)}}
			d.DrawString(str)
		}
		d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face, Dot: fixed.Point26_6{X: fx(x), Y: fx(y)}}
		d.DrawString(str)
	}

	x := pad
	if len(items) == 0 && !o.Clock {
		drawStr(textFace, dim(fg), x, baseline, "Cihaz yok")
	}
	for i, it := range items {
		if i > 0 {
			x += gap
		}
		d := it.dev
		col := fg
		if o.Threshold > 0 && d.Percent >= 0 && d.Percent <= o.Threshold && !d.Charging {
			col = low
		}
		if !d.Online {
			col = dim(fg)
		}
		// icon
		iconBottom := mid + 7*s
		if iconFace != nil {
			g := string(kindGlyph(d.Kind))
			b, _ := font.BoundString(iconFace, g)
			cy := float64(b.Min.Y+b.Max.Y) / 128
			drawStr(iconFace, col, x, mid-cy, g)
			iconBottom = mid - cy + float64(b.Max.Y)/64
		} else {
			drawStr(subFace, col, x, baseline, d.Kind.Label()[:1])
		}
		x += it.iconW
		if d.Index > 0 {
			drawStr(subFace, col, x-1*s, iconBottom+1.5*s, fmt.Sprint(d.Index))
			x += it.subW
		}
		x += 5 * s
		if it.boltW > 0 {
			bc := green
			if !d.Online {
				bc = dim(green)
			}
			b, _ := font.BoundString(boltFace, string(glyphBolt))
			cy := float64(b.Min.Y+b.Max.Y) / 128
			drawStr(boltFace, bc, x, mid-cy, string(glyphBolt))
			x += it.boltW
		}
		drawStr(textFace, col, x, baseline, it.label)
		x += measure(textFace, it.label)
	}
	if o.Clock {
		if len(items) > 0 {
			x += gap + 4*s
		}
		ch := float64(clockFace.Metrics().CapHeight) / 64
		if ch <= 0 {
			ch = 8.5 * s
		}
		right := x + clockW
		y1 := mid - 3*s
		y2 := mid + ch + 4*s
		drawStr(clockFace, fg, right-measure(clockFace, tStr), y1, tStr)
		drawStr(clockFace, fg, right-measure(clockFace, dStr), y2, dStr)
	}
	return img
}

func fillRoundRect(img *image.RGBA, c color.NRGBA, r float64) {
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			cx := math.Max(r, math.Min(px, w-r))
			cy := math.Max(r, math.Min(py, h-r))
			dist := math.Hypot(px-cx, py-cy)
			a := math.Max(0, math.Min(1, r-dist+0.5))
			if a <= 0 {
				continue
			}
			cc := c
			cc.A = uint8(float64(c.A) * a)
			draw.Draw(img, image.Rect(x, y, x+1, y+1), image.NewUniform(cc), image.Point{}, draw.Over)
		}
	}
}

// renderTrayIcon draws a small battery with the lowest charge level.
func renderTrayIcon(pct int, any bool, light bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	outline := color.NRGBA{255, 255, 255, 255}
	if light {
		outline = color.NRGBA{30, 30, 30, 255}
	}
	fill := func(x0, y0, x1, y1 int, c color.NRGBA) {
		draw.Draw(img, image.Rect(x0, y0, x1, y1), image.NewUniform(c), image.Point{}, draw.Over)
	}
	// body outline
	fill(1, 8, 28, 10, outline)
	fill(1, 22, 28, 24, outline)
	fill(1, 8, 3, 24, outline)
	fill(26, 8, 28, 24, outline)
	fill(28, 13, 31, 19, outline)
	if any {
		c := color.NRGBA{108, 203, 95, 255}
		if pct <= 20 {
			c = color.NRGBA{232, 72, 60, 255}
		} else if pct <= 40 {
			c = color.NRGBA{255, 185, 0, 255}
		}
		wFill := int(math.Round(float64(21) * float64(pct) / 100))
		if wFill < 2 {
			wFill = 2
		}
		fill(4, 11, 4+wFill, 21, c)
	}
	return img
}
