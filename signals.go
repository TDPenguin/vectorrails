package main

// rails signals to raster tiles
//
// NIMBY can't draw icons from vector tiles, but rasters work, so convert/use these instead.
// draw them on top of the map. this serves /signals/{z}/{x}/{y}.png, this fetches the railway_signals
// vector tile from the upstreame server, decodes the points in it, and then draws these onto a
// transparent 256 x 256 png.
//
// this file/module draws each signal's icons from the sprite sheet, and then passes them on

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	tileSize      = 256
	minSignalZoom = 13 // openrailwaymap.app does not draw signals below this zoom
	maxSignalZoom = 25
	signalLayer   = "railway_signals"
)

var errNotMVT = errors.New("tile is not MVT")

// signal is one point from the railway_signals layer
type signal struct {
	x, y  float64           // pos inside the tile, in pixels (0 to 256)
	props map[string]string // every property of the point as txt
}

// parseTilePath turns "/signals/15/29574/20103.png" into z, x, y for NIMBY
func parseTilePath(path string) (z, x, y int, ok bool) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(path, "/signals/"), ".png"), "/")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}

	var err error
	if z, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if x, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if y, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, false
	}

	return z, x, y, true
}

// the parts of an MVT tile that matter for signals:
//
//	Tile : layers = 3
//	Layer : name = 1, features = 2, keys = 3, values = 4, extent = 5
//	Feature : tags = 2, type = 3 (1 = point), geometry = 4
//	Value : string = 1, float = 2, double = 3, int = 4, uint = 5, sint = 6, bool = 7
//
// feature's tags are like tuples, (index into keys, index into values).

// unpackVarints reads a packed run of varints, for other use
func unpackVarints(b []byte) ([]uint64, error) {
	var out []uint64
	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errBadProto
		}
		out = append(out, v)
		b = b[n:]
	}
	return out, nil
}

// zigzag (encoding, today i learnt) turns the unsigned number MVT stores back into a signed one
func zigzag(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
}

// valueText turns one Value message into text, whatever type it holds.
func valueText(value []byte) (string, error) {
	fields, err := splitFields(value)
	if err != nil {
		return "", err
	}

	for _, f := range fields {
		switch {
		case f.num == 1 && f.wire == 2:
			return string(f.body), nil
		case f.num == 2 && f.wire == 5 && len(f.body) == 4:
			return strconv.FormatFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(f.body))), 'f', -1, 32), nil
		case f.num == 3 && f.wire == 1 && len(f.body) == 8:
			return strconv.FormatFloat(math.Float64frombits(binary.LittleEndian.Uint64(f.body)), 'f', -1, 64), nil
		case (f.num == 4 || f.num == 5) && f.wire == 0:
			v, _ := binary.Uvarint(f.body)
			if f.num == 4 {
				return strconv.FormatInt(int64(v), 10), nil
			}
			return strconv.FormatUint(v, 10), nil
		case f.num == 6 && f.wire == 0:
			v, _ := binary.Uvarint(f.body)
			return strconv.FormatInt(zigzag(v), 10), nil
		case f.num == 7 && f.wire == 0:
			v, _ := binary.Uvarint(f.body)
			if v != 0 {
				return "true", nil
			}

			return "false", nil
		}
	}

	return "", nil
}

// decodeFeature reads one point feature. other geometry types give no signals.
func decodeFeature(feature []byte, keys, values []string, extent float64) ([]signal, error) {
	fields, err := splitFields(feature)
	if err != nil {
		return nil, err
	}

	var tags, geometry []uint64
	geomType := uint64(0)

	for _, f := range fields {
		switch {
		case f.num == 2 && f.wire == 2:
			if tags, err = unpackVarints(f.body); err != nil {
				return nil, err
			}
		case f.num == 3 && f.wire == 0:
			geomType, _ = binary.Uvarint(f.body)
		case f.num == 4 && f.wire == 2:
			if geometry, err = unpackVarints(f.body); err != nil {
				return nil, err
			}
		}
	}
	if geomType != 1 {
		return nil, nil
	}

	props := map[string]string{}
	for i := 0; i+1 < len(tags); i += 2 {
		if tags[i] < uint64(len(keys)) && tags[i+1] < uint64(len(values)) {
			props[keys[tags[i]]] = values[tags[i+1]]
		}
	}

	// point geometry is a list of "move to" cmds, command number, then x y pairs, each cmd is
	// relative to the previous point
	var out []signal
	var cx, cy int64

	for i := 0; i < len(geometry); {
		cmd := geometry[i]
		i++

		if cmd&7 != 1 { // 1 = move to, nothing else
			return nil, errBadProto
		}

		for n := int(cmd >> 3); n > 0; n-- {
			if i+1 >= len(geometry) {
				return nil, errBadProto
			}
			cx += zigzag(geometry[i])
			cy += zigzag(geometry[i+1])

			i += 2

			out = append(out, signal{
				x:     float64(cx) * tileSize / extent,
				y:     float64(cy) * tileSize / extent,
				props: props,
			})
		}
	}

	return out, nil
}

// decodeSignalLayer returns the signals of one Layer msg, or nothing if it is within another layer
func decodeSignalLayer(layer []byte) ([]signal, error) {
	fields, err := splitFields(layer)
	if err != nil {
		return nil, err
	}

	var name string
	var keys, values []string
	var features [][]byte
	extent := 4096.0

	for _, f := range fields {
		switch {
		case f.num == 1 && f.wire == 2:
			name = string(f.body)
		case f.num == 2 && f.wire == 2:
			features = append(features, f.body)
		case f.num == 3 && f.wire == 2:
			keys = append(keys, string(f.body))
		case f.num == 4 && f.wire == 2:
			v, err := valueText(f.body)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		case f.num == 5 && f.wire == 0:
			e, _ := binary.Uvarint(f.body)
			extent = float64(e)
		}
	}
	if name != signalLayer || extent == 0 {
		return nil, nil
	}

	var out []signal

	for _, feature := range features {
		got, err := decodeFeature(feature, keys, values, extent)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}

	return out, nil
}

// decodeSignals reads every signal in a whole given MVT tile
func decodeSignals(tile []byte) ([]signal, error) {
	layers, err := splitFields(tile)
	if err != nil {
		return nil, err
	}

	var out []signal
	for _, l := range layers {
		if l.num != 3 || l.wire != 2 {
			continue
		}
		got, err := decodeSignalLayer(l.body)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}

	return out, nil
}

// fetchSignals fetches the railway_signals tile from the upstream server and decodes it
func fetchSignals(client *http.Client, base string, z, x, y int) ([]signal, error) {
	url := fmt.Sprintf("%s/%s/%d/%d/%d", base, signalLayer, z, x, y)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/x-protobuf")
	setHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil // empty tile
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream answered %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	if body[0] != 0x1a { // a real MVT tile starts with byte 0x1a
		return nil, errNotMVT
	}

	return decodeSignals(body)
}

// fillCircle draws a filled circle, clipped to the img
// never make me do this shit again, i hate circles
func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	for py := int(math.Floor(cy - r)); py <= int(math.Ceil(cy+r)); py++ {
		for px := int(math.Floor(cx - r)); px <= int(math.Ceil(cx+r)); px++ {
			if !(image.Point{X: px, Y: py}).In(img.Rect) {
				continue
			}

			dx, dy := float64(px)+0.5-cx, float64(py)+0.5-cy

			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

// maxFeatureIcons is how many icons one signal can have, max of 6 in regular ORM
const maxFeatureIcons = 6

// drawDot draws signal placeholder
// once again, never again
func drawDot(img *image.NRGBA, s signal) {
	fillCircle(img, s.x, s.y, 8, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	fillCircle(img, s.x, s.y, 5, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
}

// drawSignal draws the icons of one signal and returns the icon names that were not in the
// sprite. the first icon is centred on the signal's position and the next ones are stacked
// above it with a 4 pixel gap (2 on the website, which is half our size), the same way the openrailwaymap.app style places them.
// a signal with no usable icon is drawn as a dot.

// drawSignal draws the icons of one signal and returns the icon names that were not in the
// spritesheet, the first icon is centered on the signal's post, and the next ones are stacked above
// with a 4 px gap (2 on website, half the size here), the same way the openrailwaymap.app
// style places them for consistency, a signal with no usable icon is drawn as placeholder
func drawSignal(img *image.NRGBA, s signal, spr *sprite) (missing []string) {
	drawn := 0
	stacked := 0.0     // height of the icons already drawn
	firstHeight := 0.0 // height of the first icon

	for i := range maxFeatureIcons {
		name := s.props["feature"+strconv.Itoa(i)]
		if name == "" {
			continue
		}

		icon := spr.icon(name)
		if icon == nil {
			missing = append(missing, name)
			continue
		}

		w, h := float64(icon.Rect.Dx()), float64(icon.Rect.Dy())
		if drawn == 0 {
			firstHeight = h
		}

		// how far above the signal's pos the mid of this given icon is, for calc
		up := stacked + h/2 - firstHeight/2 + 4*float64(drawn)

		left := int(math.Round(s.x - w/2))
		top := int(math.Round(s.y - up - h/2))
		draw.Draw(img, image.Rect(left, top, left+icon.Rect.Dx(), top+icon.Rect.Dy()),
			icon, icon.Rect.Min, draw.Over)

		drawn++
		stacked += h

		// this is such an ugly loop
	}

	if drawn == 0 {
		drawDot(img, s)
	}

	return missing
}

// unique returns the distinct names, sorted
func unique(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// missingTile is like, placeholder, source engine ass "missing texture" shown when a tile can't
// be fetched/displayed, i put too much effort into this
func missingTile() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))

	magenta := color.NRGBA{R: 255, G: 0, B: 255, A: 255}
	black := color.NRGBA{R: 0, G: 0, B: 0, A: 255}

	const square = 16 // pixels
	for py := range tileSize {
		for px := range tileSize {
			if (px/square+py/square)%2 == 0 {
				img.SetNRGBA(px, py, magenta)
			} else {
				img.SetNRGBA(px, py, black)
			}
		}
	}

	return img
}

// describe shows a signal's props as one log line, keys in order
func describe(s signal) string {
	keys := make([]string, 0, len(s.props))
	for k := range s.props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := s.props[k]
		if len(v) > 40 {
			v = v[:40] + "..."
		}
		parts = append(parts, k+"="+v)
	}

	return strings.Join(parts, " ")
}

// newSignalsHandler serves /signals/{z}/{x}/{y}.png, fetching signals from base and drawing their
// icons from spr (the spritesheet), spr can be nil, then signals are drawn as placeholders
func newSignalsHandler(client *http.Client, base string, spr *sprite) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		z, x, y, ok := parseTilePath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		img := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize)) // transparent :shock:

		if z >= minSignalZoom && z <= maxSignalZoom {
			signals, err := fetchSignals(client, base, z, x, y)

			switch {
			case errors.Is(err, errNotMVT):
				// the server sent another format (MLT), can't be read, draw nothing
				log.Printf("WARN signals z=%d x=%d y=%d: %v, drawing nothing", z, x, y, err)
			case err != nil:
				log.Printf("ERR  signals z=%d x=%d y=%d: %v", z, x, y, err)
				img = missingTile()
			default:
				for _, s := range signals {
					drawDirection(img, s, spr, z)
				}
				var missing []string
				for _, s := range signals {
					missing = append(missing, drawSignal(img, s, spr)...)
				}

				log.Printf("200  signals z=%d x=%d y=%d: %d signals", z, x, y, len(signals))

				if names := unique(missing); len(names) > 0 {
					if len(names) > 5 {
						names = names[:5]
					}
					log.Printf("WARN signals z=%d x=%d y=%d: no sprite for %s", z, x, y, strings.Join(names, ", "))
				}

				for i := 0; i < len(signals) && i < 3; i++ {
					log.Printf("     example: %s", describe(signals[i]))
				}
			}
		}

		w.Header().Set("Content-Type", "image/png")
		// w.Header().Set("Cache-Control", "no-store") /* for testing */

		if err := png.Encode(w, img); err != nil {
			log.Printf("ERR  %s: %v", r.URL.Path, err)
		}
	}
}
