package main

import (
	"image"
	"image/color"
	"math"
	"strconv"
)

// the direction wedge thingy under a signal, which shows the direction a signal faces.
//
// openrailwaymap.app draws it from the sprite sheet too, rotated by the signals azimuth, in the
// flat map:
//   - a normal signal uses a wedge that is pinned by the middle of its top edge
//   - a signal with direction_both uses a two-wedge icon pinned by its center
//   - from zoom 16 "large" icons are used, these are a little bit transparent

// directionIcon returns the name of the wedge icon to use at this zoom
func directionIcon(both bool, z int) string {
	name := "general/signal-direction"
	if z >= 16 {
		name += "-large"
	}
	if both {
		name += "-both"
	}
	return name
}

// directionOpacity is 0.85 up to zoom 15 and 0.7 from zoom 16, like ORM
func directionOpacity(z int) float64 {
	if z >= 16 {
		return 0.7
	}
	return 0.85
}

// hasIcon returns whether a given signal has at least one icon
func hasIcon(s signal) bool {
	for i := 0; i < maxFeatureIcons; i++ {
		if s.props["feature"+strconv.Itoa(i)] != "" {
			return true
		}
	}
	return false
}

// drawDirection draws the wedge for one signal. it draws nothing for signals without an
// azimuth or without an icon, and below zoom 16 only for real signals (not derails and such).
func drawDirection(img *image.NRGBA, s signal, spr *sprite, z int) {
	azimuth, err := strconv.ParseFloat(s.props["azimuth"], 64)
	if err != nil || !hasIcon(s) {
		return
	}
	if z < 16 && s.props["railway"] != "signal" {
		return
	}

	both := s.props["direction_both"] == "true"
	icon := spr.icon(directionIcon(both, z))
	if icon == nil {
		return
	}

	pivotX := float64(icon.Rect.Dx()) / 2
	pivotY := 0.0 // the middle of the top edge
	if both {
		pivotY = float64(icon.Rect.Dy()) / 2 // the centre
	}
	drawRotated(img, icon, s.x, s.y, pivotX, pivotY, azimuth, directionOpacity(z))
}

// drawRotated draws icon so that its point (pivotX, pivotY) lands on (x, y), turned clockwise
// by degrees. opacity is 0 to 1. it samples the icon smoothly so edges are not jagged.
func drawRotated(img *image.NRGBA, icon *image.NRGBA, x, y, pivotX, pivotY, degrees, opacity float64) {
	sin, cos := math.Sincos(degrees * math.Pi / 180)

	// the icon can reach at most this far from the pivot, whatever the rotation
	w, h := float64(icon.Rect.Dx()), float64(icon.Rect.Dy())
	reach := math.Max(math.Hypot(pivotX, pivotY), math.Hypot(w-pivotX, pivotY))
	reach = math.Max(reach, math.Max(math.Hypot(pivotX, h-pivotY), math.Hypot(w-pivotX, h-pivotY)))

	for py := int(math.Floor(y - reach)); py <= int(math.Ceil(y+reach)); py++ {
		for px := int(math.Floor(x - reach)); px <= int(math.Ceil(x+reach)); px++ {
			if !(image.Point{X: px, Y: py}).In(img.Rect) {
				continue
			}

			// where this tile pixel comes from in the icon: undo the rotation
			dx, dy := float64(px)+0.5-x, float64(py)+0.5-y
			sx := dx*cos + dy*sin + pivotX
			sy := -dx*sin + dy*cos + pivotY

			r, g, b, a := sampleIcon(icon, sx, sy)
			a *= opacity
			if a <= 0 {
				continue
			}

			// put it over what is already there
			under := img.NRGBAAt(px, py)
			ua := float64(under.A) / 255
			outA := a + ua*(1-a)
			mix := func(top, bottom uint8) uint8 {
				return uint8(math.Round((float64(top)*a + float64(bottom)*ua*(1-a)) / outA))
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: mix(uint8(math.Round(r)), under.R),
				G: mix(uint8(math.Round(g)), under.G),
				B: mix(uint8(math.Round(b)), under.B),
				A: uint8(math.Round(outA * 255)),
			})
		}
	}
}

// sampleIcon reads the icon at the point (sx, sy), where (0, 0) is the top left corner of the
// icon, blending the four nearest pixels. outside the icon it is fully transparent.
// it returns the colour (0 to 255) and the alpha (0 to 1).
func sampleIcon(icon *image.NRGBA, sx, sy float64) (r, g, b, a float64) {
	fx, fy := sx-0.5, sy-0.5
	x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
	tx, ty := fx-float64(x0), fy-float64(y0)

	var sumR, sumG, sumB, sumA float64
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			weight := (1 - tx)
			if i == 1 {
				weight = tx
			}
			if j == 1 {
				weight *= ty
			} else {
				weight *= 1 - ty
			}

			px, py := x0+i, y0+j
			if px < 0 || py < 0 || px >= icon.Rect.Dx() || py >= icon.Rect.Dy() {
				continue // outside the icon: transparent
			}
			c := icon.NRGBAAt(px, py)
			ca := float64(c.A) / 255
			sumR += float64(c.R) * ca * weight
			sumG += float64(c.G) * ca * weight
			sumB += float64(c.B) * ca * weight
			sumA += ca * weight
		}
	}
	if sumA == 0 {
		return 0, 0, 0, 0
	}
	return sumR / sumA, sumG / sumA, sumB / sumA, sumA
}
