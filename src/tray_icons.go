//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	trayHeavyOnce sync.Once
	trayHeavy     *opentype.Font
)

func trayNumFont(fs *fontSet) *opentype.Font {
	trayHeavyOnce.Do(func() {
		trayHeavy = loadFont("segoeuib.ttf", "seguisb.ttf")
	})
	if trayHeavy != nil {
		return trayHeavy
	}
	return fs.bold
}

func sdRoundBox(px, py, cx, cy, hw, hh, r float64) float64 {
	dx := math.Abs(px-cx) - (hw - r)
	dy := math.Abs(py-cy) - (hh - r)
	out := math.Hypot(math.Max(dx, 0), math.Max(dy, 0))
	in := math.Min(math.Max(dx, dy), 0)
	return out + in - r
}

func sdCircle(px, py, cx, cy, r float64) float64 {
	return math.Hypot(px-cx, py-cy) - r
}

func sdCapsule(px, py, ax, ay, bx, by, r float64) float64 {
	pax, pay := px-ax, py-ay
	bax, bay := bx-ax, by-ay
	h := math.Max(0, math.Min(1, (pax*bax+pay*bay)/(bax*bax+bay*bay)))
	return math.Hypot(pax-bax*h, pay-bay*h) - r
}

func blendPx(img *image.RGBA, x, y int, c color.NRGBA, cov float64) {
	if cov <= 0 || x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
		return
	}
	if cov > 1 {
		cov = 1
	}
	cc := c
	cc.A = uint8(math.Round(float64(c.A) * cov))
	draw.Draw(img, image.Rect(x, y, x+1, y+1), image.NewUniform(cc), image.Point{}, draw.Over)
}

// drawChargingBolt draws a crisp vector lightning bolt in the top-right corner with a subtle dark halo.
func drawChargingBolt(img *image.RGBA, s float64, offline bool) {
	u := s / 32.0
	// Two slanted strokes forming a crisp lightning bolt in top-right corner
	// Upper stroke: (27.2, 1.4) -> (22.8, 6.8), crossbar (22.8, 6.8) -> (27.2, 6.8), lower stroke (27.2, 6.8) -> (23.2, 12.2)
	distBolt := func(px, py float64) float64 {
		d1 := sdCapsule(px, py, 27.2*u, 1.6*u, 22.8*u, 6.8*u, 1.05*u)
		d2 := sdCapsule(px, py, 22.8*u, 6.8*u, 27.6*u, 6.8*u, 0.95*u)
		d3 := sdCapsule(px, py, 27.6*u, 6.8*u, 23.0*u, 12.2*u, 1.05*u)
		return math.Min(d1, math.Min(d2, d3))
	}
	halo := color.NRGBA{18, 20, 24, 230}
	bolt := color.NRGBA{255, 226, 78, 255}
	if offline {
		halo.A = uint8(float64(halo.A) * 0.55)
		bolt.A = uint8(float64(bolt.A) * 0.55)
	}
	x0 := int(math.Floor(20 * u))
	y0 := 0
	x1 := img.Bounds().Dx()
	y1 := int(math.Ceil(15 * u))
	if y1 > img.Bounds().Dy() {
		y1 = img.Bounds().Dy()
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			d := distBolt(float64(x)+0.5, float64(y)+0.5)
			hCov := math.Max(0, math.Min(1, 0.5-(d-1.25*u)))
			if hCov > 0 {
				blendPx(img, x, y, halo, hCov)
			}
			bCov := math.Max(0, math.Min(1, 0.5-d))
			if bCov > 0 {
				blendPx(img, x, y, bolt, bCov)
			}
		}
	}
}

// trayIconDevice draws a full-size vector device silhouette (mouse, keyboard, headset, gamepad,
// or battery for other devices) filled proportionally with a color-coded charge gauge
// (green >40%, amber 21-40%, red when low, bright green + bolt when charging) and the charge number inside.
func trayIconDevice(fs *fontSet, size int, kind Kind, pct int, low, charging, offline, light bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	border := color.NRGBA{235, 235, 235, 255}
	fillBg := color.NRGBA{40, 42, 48, 245}
	fg := color.NRGBA{255, 255, 255, 255}
	shadow := color.NRGBA{0, 0, 0, 175}
	if light {
		border = color.NRGBA{36, 38, 44, 255}
		fillBg = color.NRGBA{54, 56, 64, 245}
	}

	gauge := color.NRGBA{46, 158, 88, 228}
	if charging {
		if light {
			border = color.NRGBA{22, 163, 74, 255}
		} else {
			border = color.NRGBA{108, 228, 134, 255}
		}
		gauge = color.NRGBA{42, 178, 96, 232}
	} else if low {
		if light {
			border = color.NRGBA{196, 43, 28, 255}
			gauge = color.NRGBA{210, 48, 38, 235}
		} else {
			border = color.NRGBA{255, 99, 88, 255}
			gauge = color.NRGBA{220, 62, 54, 235}
		}
	} else if pct >= 0 && pct <= 40 {
		gauge = color.NRGBA{210, 148, 20, 230}
	}

	dim := func(c color.NRGBA) color.NRGBA {
		if offline {
			c.A = uint8(float64(c.A) * 0.55)
		}
		return c
	}
	border, fillBg, gauge, fg, shadow = dim(border), dim(fillBg), dim(gauge), dim(fg), dim(shadow)

	s := float64(size)
	u := s / 32.0 // scale factor relative to 32x32 design grid

	frac := 0.0
	if pct > 0 {
		frac = math.Min(1.0, float64(pct)/100.0)
	}
	str := "--"
	if pct >= 0 {
		str = fmt.Sprint(pct)
	}
	heavy := trayNumFont(fs)

	fitByBounds := func(maxW, maxH float64) (font.Face, fixed.Rectangle26_6) {
		var best font.Face
		var bestB fixed.Rectangle26_6
		for pt := maxH * 2.0; pt >= 6; pt -= 0.5 {
			fc := fs.face(heavy, "trayn", pt)
			if fc == nil {
				continue
			}
			b, _ := font.BoundString(fc, str)
			w := float64(b.Max.X-b.Min.X) / 64
			h := float64(b.Max.Y-b.Min.Y) / 64
			best, bestB = fc, b
			if w <= maxW && h <= maxH {
				break
			}
		}
		return best, bestB
	}

	drawInBox := func(fc font.Face, b fixed.Rectangle26_6, col color.NRGBA, x0, y0, bw, bh float64) {
		if fc == nil {
			return
		}
		w := float64(b.Max.X-b.Min.X) / 64
		h := float64(b.Max.Y-b.Min.Y) / 64
		x := x0 + (bw-w)/2 - float64(b.Min.X)/64
		y := y0 + (bh-h)/2 - float64(b.Min.Y)/64
		d := font.Drawer{Dst: img, Src: image.NewUniform(col), Face: fc, Dot: fixed.Point26_6{X: fx(x), Y: fx(y)}}
		d.DrawString(str)
	}

	drawNum := func(x0, y0, w, h, maxH float64) {
		fc, b := fitByBounds(w, maxH)
		drawInBox(fc, b, shadow, x0+1*u, y0+1*u, w, h)
		drawInBox(fc, b, fg, x0, y0, w, h)
	}

	switch kind {
	case KindMouse:
		w, h := s*0.80, s*0.96
		x0, y0 := (s-w)/2, (s-h)/2
		cx, cy := x0+w/2, y0+h/2
		hw, hh := w/2, h/2
		r := hw * 0.92
		thick := math.Max(1.0, 1.7*u)
		fillTop := (y0 + h - thick) - (h-2*thick)*frac
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				d := sdRoundBox(px, py, cx, cy, hw, hh, r)
				outer := math.Max(0, math.Min(1, 0.5-d))
				if outer <= 0 {
					continue
				}
				inner := math.Max(0, math.Min(1, 0.5-(d+thick)))
				if inner > 0 {
					blendPx(img, x, y, fillBg, inner)
					if frac > 0 && py >= fillTop {
						blendPx(img, x, y, gauge, inner)
					}
					dw := sdRoundBox(px, py, cx, y0+5.3*u, 1.3*u, 2.5*u, 1.2*u)
					blendPx(img, x, y, border, math.Max(0, math.Min(1, 0.5-dw)))
				}
				blendPx(img, x, y, border, outer*(1-inner))
			}
		}
		drawNum(x0+2*u, y0+10.5*u, w-4*u, h-13*u, s*0.44)

	case KindKeyboard:
		w, h := s*0.96, s*0.78
		x0, y0 := (s-w)/2, (s-h)/2
		cx, cy := x0+w/2, y0+h/2
		hw, hh := w/2, h/2
		thick := math.Max(1.0, 1.7*u)
		fillRight := (x0 + thick) + (w-2*thick)*frac
		keyCol := color.NRGBA{border.R, border.G, border.B, uint8(float64(border.A) * 0.82)}
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				d := sdRoundBox(px, py, cx, cy, hw, hh, 4.0*u)
				outer := math.Max(0, math.Min(1, 0.5-d))
				if outer <= 0 {
					continue
				}
				inner := math.Max(0, math.Min(1, 0.5-(d+thick)))
				if inner > 0 {
					blendPx(img, x, y, fillBg, inner)
					if frac > 0 && px <= fillRight {
						blendPx(img, x, y, gauge, inner)
					}
					if py > y0+3*u && py < y0+6*u {
						for i := 0; i < 5; i++ {
							kx := x0 + (5.5+float64(i)*5.2)*u
							if math.Abs(px-kx) < 1.3*u {
								blendPx(img, x, y, keyCol, 1)
							}
						}
					}
				}
				blendPx(img, x, y, border, outer*(1-inner))
			}
		}
		drawNum(x0+2*u, y0+7*u, w-4*u, h-9*u, s*0.42)

	case KindHeadset:
		cx := s / 2
		arcCy := s * 0.52
		outerR := s * 0.44
		bandW := math.Max(1.4, 2.6*u)
		cupW, cupH := 4.4*u, 13.5*u
		cupY := s * 0.67
		fillTop := (s - 2.5*u) - (s*0.56)*frac
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				dIn := sdRoundBox(px, py, cx, s*0.64, s*0.34, s*0.30, 5.0*u)
				inCov := math.Max(0, math.Min(1, 0.5-dIn))
				if inCov > 0 {
					blendPx(img, x, y, fillBg, inCov)
					if frac > 0 && py >= fillTop {
						blendPx(img, x, y, gauge, inCov)
					}
				}
				dist := math.Hypot(px-cx, py-arcCy)
				dArc := math.Abs(dist-(outerR-bandW/2)) - bandW/2
				if py > arcCy+2.5*u {
					dArc = math.Max(dArc, py-(arcCy+2.5*u))
				}
				dL := sdRoundBox(px, py, 2.7*u, cupY, cupW/2, cupH/2, 2.2*u)
				dR := sdRoundBox(px, py, s-2.7*u, cupY, cupW/2, cupH/2, 2.2*u)
				dFrame := math.Min(dArc, math.Min(dL, dR))
				blendPx(img, x, y, border, math.Max(0, math.Min(1, 0.5-dFrame)))
			}
		}
		drawNum(6.5*u, s*0.39, s-13*u, s*0.50, s*0.39)

	case KindGamepad:
		cx := s / 2
		thick := math.Max(1.0, 1.6*u)
		fillTop := (s - 4.0*u) - (s*0.68)*frac
		dotCol := color.NRGBA{border.R, border.G, border.B, uint8(float64(border.A) * 0.82)}
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				dMid := sdRoundBox(px, py, cx, s*0.44, s*0.38, s*0.28, 6.0*u)
				dGL := sdCapsule(px, py, s*0.23, s*0.42, s*0.17, s*0.79, s*0.15)
				dGR := sdCapsule(px, py, s*0.77, s*0.42, s*0.83, s*0.79, s*0.15)
				d := math.Min(dMid, math.Min(dGL, dGR))
				outer := math.Max(0, math.Min(1, 0.5-d))
				if outer <= 0 {
					continue
				}
				inner := math.Max(0, math.Min(1, 0.5-(d+thick)))
				if inner > 0 {
					blendPx(img, x, y, fillBg, inner)
					if frac > 0 && py >= fillTop {
						blendPx(img, x, y, gauge, inner)
					}
					dDot1 := sdCircle(px, py, s*0.22, s*0.28, 1.15*u)
					dDot2 := sdCircle(px, py, s*0.78, s*0.28, 1.15*u)
					blendPx(img, x, y, dotCol, math.Max(0, math.Min(1, 0.5-math.Min(dDot1, dDot2))))
				}
				blendPx(img, x, y, border, outer*(1-inner))
			}
		}
		drawNum(6.5*u, s*0.27, s-13*u, s*0.44, s*0.36)

	default:
		w, h := s*0.72, s*0.84
		nubW, nubH := s*0.34, s*0.11
		x0, y0 := (s-w)/2, s*0.14
		cx, cy := x0+w/2, y0+h/2
		hw, hh := w/2, h/2
		thick := math.Max(1.0, 1.7*u)
		fillTop := (y0 + h - thick) - (h-2*thick)*frac
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				dNub := sdRoundBox(px, py, cx, y0-nubH/2+0.5*u, nubW/2, nubH/2, 1.2*u)
				blendPx(img, x, y, border, math.Max(0, math.Min(1, 0.5-dNub)))
				d := sdRoundBox(px, py, cx, cy, hw, hh, 4.0*u)
				outer := math.Max(0, math.Min(1, 0.5-d))
				if outer <= 0 {
					continue
				}
				inner := math.Max(0, math.Min(1, 0.5-(d+thick)))
				if inner > 0 {
					blendPx(img, x, y, fillBg, inner)
					if frac > 0 && py >= fillTop {
						blendPx(img, x, y, gauge, inner)
					}
				}
				blendPx(img, x, y, border, outer*(1-inner))
			}
		}
		drawNum(x0+2*u, y0+2*u, w-4*u, h-4*u, s*0.45)
	}

	if charging {
		drawChargingBolt(img, s, offline)
	}

	return img
}
