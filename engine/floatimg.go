package engine

import (
	"image"
	"image/color"
	"math"
)

// FloatImage is a 32-bit floating-point image used for accumulation.
// Each pixel has R, G, B channels stored as float64 for precision.
type FloatImage struct {
	Width, Height int
	Pix           []float64 // len = Width * Height * 3 (RGB interleaved)
}

// NewFloatImage creates a zeroed float image.
func NewFloatImage(width, height int) *FloatImage {
	return &FloatImage{
		Width:  width,
		Height: height,
		Pix:    make([]float64, width*height*3),
	}
}

// FromNRGBA converts a standard image to a FloatImage.
func FromNRGBA(src *image.NRGBA) *FloatImage {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	fi := NewFloatImage(w, h)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.NRGBAAt(x+b.Min.X, y+b.Min.Y)
			idx := (y*w + x) * 3
			fi.Pix[idx] = float64(c.R)
			fi.Pix[idx+1] = float64(c.G)
			fi.Pix[idx+2] = float64(c.B)
		}
	}
	return fi
}

// Add accumulates another FloatImage into this one (pixel-wise addition).
func (fi *FloatImage) Add(other *FloatImage) {
	for i := range fi.Pix {
		fi.Pix[i] += other.Pix[i]
	}
}

// Stretch applies black level, gamma, and white level mapping, returning
// an 8-bit NRGBA image suitable for display.
func (fi *FloatImage) Stretch(black, gamma, white float64) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, fi.Width, fi.Height))
	invGamma := 1.0 / gamma
	rng := white - black
	if rng <= 0 {
		rng = 1
	}

	for y := 0; y < fi.Height; y++ {
		for x := 0; x < fi.Width; x++ {
			idx := (y*fi.Width + x) * 3
			r := stretchChannel(fi.Pix[idx], black, rng, invGamma)
			g := stretchChannel(fi.Pix[idx+1], black, rng, invGamma)
			b := stretchChannel(fi.Pix[idx+2], black, rng, invGamma)
			dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return dst
}

// Normalized returns the accumulator divided by frameCount as a display image.
func (fi *FloatImage) Normalized(frameCount int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, fi.Width, fi.Height))
	if frameCount <= 0 {
		return dst
	}
	scale := 1.0 / float64(frameCount)

	for y := 0; y < fi.Height; y++ {
		for x := 0; x < fi.Width; x++ {
			idx := (y*fi.Width + x) * 3
			r := clamp8(fi.Pix[idx] * scale)
			g := clamp8(fi.Pix[idx+1] * scale)
			b := clamp8(fi.Pix[idx+2] * scale)
			dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return dst
}

func stretchChannel(val, black, rng, invGamma float64) uint8 {
	// Map [black..white] → [0..1]
	normalized := (val - black) / rng
	if normalized < 0 {
		normalized = 0
	}
	if normalized > 1 {
		normalized = 1
	}
	// Apply gamma
	stretched := math.Pow(normalized, invGamma)
	return clamp8(stretched * 255)
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// Grayscale returns a single-channel brightness image from an NRGBA.
func Grayscale(src *image.NRGBA) []float64 {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.NRGBAAt(x+b.Min.X, y+b.Min.Y)
			// Standard luminance weights.
			gray[y*w+x] = 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
		}
	}
	return gray
}
