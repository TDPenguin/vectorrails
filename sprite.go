package main

// the icon spritesheet from openrailwaymap.app (fetched first call for a signal)
//
// symbols@2x.png is the spritesheet with every icon in it, and symbols@2x.json is the "manifest",
// for example: "name": {"x": 1368, "y": 398, "width": 26, "height": 48, "pixelRatio": 2}.
// pixelRatio 2 means that the icon was drawn at double size, so on the openrailwaymap.app website
// it is shown half as big (13 x 24 px here). not shrinked by this, each icon is cut out of the
// spritesheet and drawn with the sheet's own pixels, so signals are twice the size of the
// website's, which leads to more clarity than the other approaches i tried

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"sync"

	xdraw "golang.org/x/image/draw"
)

// iconScale sizes every signal icon and direction wedge. 1 is the size on the openrailwaymap.app
// website, 2 uses the sheet's own pixels (sharpest), and anything in between is smoothed
const (
	iconScale = 1.5
)

type spriteEntry struct {
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	PixelRatio float64 `json:"pixelRatio"`
}

type sprite struct {
	// a sprite made by loadSprite starts emtpy and is filled in by load, once, the first time
	// an icon is asked for. a sprite from newSprite is already filled in and has no load
	once sync.Once
	load func() (*sprite, error)

	sheet   *image.NRGBA
	entries map[string]spriteEntry

	mu    sync.Mutex
	cache map[string]*image.NRGBA // icons already cut out
}

// newSprite builds a sprite from the png and json file contents, just providing manifest details
func newSprite(pngData, jsonData []byte) (*sprite, error) {
	decoded, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("sprite png: %w", err)
	}

	sheet := image.NewNRGBA(decoded.Bounds())
	draw.Draw(sheet, sheet.Bounds(), decoded, decoded.Bounds().Min, draw.Src)

	entries := map[string]spriteEntry{}
	if err := json.Unmarshal(jsonData, &entries); err != nil {
		return nil, fmt.Errorf("sprite json: %w", err)
	}

	return &sprite{sheet: sheet, entries: entries, cache: map[string]*image.NRGBA{}}, nil
}

// get fetches the spritesheet and the manifest from the upstream url
func get(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	setHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}

	return io.ReadAll(resp.Body)
}

// fethSprite dowmloads symbols@2x.png and symbols@2x.json from base/folder/, where folder is
// "sprite" (normal icons) or "sdf_sprite" (direction arrows)
func fetchSprite(client *http.Client, base, folder string) (*sprite, error) {
	pngData, err := get(client, base+"/"+folder+"/symbols@2x.png")
	if err != nil {
		return nil, err
	}

	jsonData, err := get(client, base+"/"+folder+"/symbols@2x.json")
	if err != nil {
		return nil, err
	}

	return newSprite(pngData, jsonData)
}

// loadSprite returns a sprite that downloads the sheet the first time an icon is needed, if the
// download fails, this is logged, and every signal is drawn as a placeholder until the proxy is
// restarted
func loadSprite(client *http.Client, base string) *sprite {
	return &sprite{
		cache: map[string]*image.NRGBA{},
		load: func() (*sprite, error) {
			return fetchSprite(client, base, "sprite")
		},
	}
}

// ready fills in the sprite if that has not occured yet, false means no sheet is provided/exists
func (s *sprite) ready() bool {
	s.once.Do(func() {
		if s.load == nil {
			return
		}

		loaded, err := s.load()
		if err != nil {
			log.Printf("WARN sprite not loaded, signals will be plain dots: %v", err)
			return
		}

		s.sheet, s.entries = loaded.sheet, loaded.entries

		log.Printf("loaded sprite: %d icons", len(s.entries))
	})
	return s.sheet != nil
}

// icon returns the icon with the given name, as it is in the sheet, or nil if there is none that
// exists. this is safe to call on a nil sprite
func (s *sprite) icon(name string) *image.NRGBA {
	if s == nil || !s.ready() {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if icon, ok := s.cache[name]; ok {
		return icon
	}

	var icon *image.NRGBA
	if e, ok := s.entries[name]; ok && e.Width > 0 && e.Height > 0 {
		rect := image.Rect(e.X, e.Y, e.X+e.Width, e.Y+e.Height)
		if rect.In(s.sheet.Rect) {
			ratio := e.PixelRatio
			if ratio < 1 {
				ratio = 1
			}
			icon = resample(s.sheet, rect, ratio/iconScale)
		}
	}
	s.cache[name] = icon
	return icon
}

// resample cuts rect out of src and rescales it. step is how many source pixels go into one
// output pixel (2 = half size, 1 = unchanged, below 1 = bigger). when the size does not change
// the pixels are just copied.
func resample(src *image.NRGBA, rect image.Rectangle, step float64) *image.NRGBA {
	outW := int(math.Ceil(float64(rect.Dx())/step - 1e-9))
	outH := int(math.Ceil(float64(rect.Dy())/step - 1e-9))
	out := image.NewNRGBA(image.Rect(0, 0, outW, outH))

	if outW == rect.Dx() && outH == rect.Dy() {
		draw.Draw(out, out.Rect, src, rect.Min, draw.Src)
		return out
	}
	xdraw.CatmullRom.Scale(out, out.Rect, src, rect, xdraw.Src, nil)
	return out
}
