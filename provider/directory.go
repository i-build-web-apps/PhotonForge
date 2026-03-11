package provider

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// DirectoryProvider streams images from a folder, simulating a live camera feed.
// When jitter is enabled it applies a random 1-3 pixel shift to each frame,
// letting you verify that the alignment engine can correct for tracking drift.
type DirectoryProvider struct {
	Dir    string
	Jitter bool
	Loop   bool

	files []string
	index int
}

func NewDirectoryProvider(dir string, jitter, loop bool) *DirectoryProvider {
	return &DirectoryProvider{Dir: dir, Jitter: jitter, Loop: loop}
}

func (d *DirectoryProvider) Open() error {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		return fmt.Errorf("directory provider: %w", err)
	}

	exts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true,
		".tif": true, ".tiff": true, ".bmp": true,
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if exts[ext] {
			d.files = append(d.files, filepath.Join(d.Dir, e.Name()))
		}
	}
	sort.Strings(d.files)

	if len(d.files) == 0 {
		return fmt.Errorf("directory provider: no images found in %s", d.Dir)
	}

	d.index = 0
	return nil
}

func (d *DirectoryProvider) Read() *image.NRGBA {
	if len(d.files) == 0 {
		return nil
	}

	if d.index >= len(d.files) {
		if !d.Loop {
			return nil
		}
		d.index = 0
	}

	img, err := loadImage(d.files[d.index])
	if err != nil {
		return nil
	}
	d.index++

	if d.Jitter {
		img = applyJitter(img)
	}

	return img
}

func (d *DirectoryProvider) Close() error {
	d.files = nil
	d.index = 0
	return nil
}

// loadImage reads an image file and converts it to NRGBA.
func loadImage(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(path))

	var img image.Image
	switch ext {
	case ".png":
		img, err = png.Decode(f)
	case ".jpg", ".jpeg":
		img, err = jpeg.Decode(f)
	default:
		// Use generic decoder for bmp, tiff, etc.
		img, _, err = image.Decode(f)
	}
	if err != nil {
		return nil, err
	}

	return toNRGBA(img), nil
}

// toNRGBA converts any image.Image to *image.NRGBA.
func toNRGBA(src image.Image) *image.NRGBA {
	if nrgba, ok := src.(*image.NRGBA); ok {
		return nrgba
	}
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}

// applyJitter shifts the image by a random 1-3 pixel offset, simulating
// telescope tracking error.
func applyJitter(img *image.NRGBA) *image.NRGBA {
	dx := 1 + rand.Intn(3)
	dy := 1 + rand.Intn(3)
	if rand.Intn(2) == 0 {
		dx = -dx
	}
	if rand.Intn(2) == 0 {
		dy = -dy
	}

	b := img.Bounds()
	shifted := image.NewNRGBA(b)

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			sx := x - dx
			sy := y - dy
			if sx >= b.Min.X && sx < b.Max.X && sy >= b.Min.Y && sy < b.Max.Y {
				shifted.SetNRGBA(x, y, img.NRGBAAt(sx, sy))
			} else {
				shifted.SetNRGBA(x, y, color.NRGBA{A: 255})
			}
		}
	}

	return shifted
}
