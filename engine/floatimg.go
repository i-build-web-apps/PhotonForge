package engine

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
)

// numWorkers returns the number of goroutines to use for parallel pixel ops.
var numWorkers = runtime.NumCPU()

// parallelRows splits rows [0, height) across goroutines.
// fn receives (startRow, endRow) for each band.
func parallelRows(height int, fn func(y0, y1 int)) {
	n := numWorkers
	if n > height {
		n = height
	}
	if n <= 1 {
		fn(0, height)
		return
	}
	var wg sync.WaitGroup
	band := (height + n - 1) / n
	for i := 0; i < n; i++ {
		y0 := i * band
		y1 := y0 + band
		if y1 > height {
			y1 = height
		}
		if y0 >= y1 {
			break
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			fn(y0, y1)
		}(y0, y1)
	}
	wg.Wait()
}

// parallelPix splits a pixel slice across goroutines for bulk operations.
func parallelPix(n int, fn func(start, end int)) {
	w := numWorkers
	if w > n {
		w = n
	}
	if w <= 1 {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	band := (n + w - 1) / w
	for i := 0; i < w; i++ {
		s := i * band
		e := s + band
		if e > n {
			e = n
		}
		if s >= e {
			break
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			fn(s, e)
		}(s, e)
	}
	wg.Wait()
}

// FloatImage is a 32-bit floating-point image used for accumulation.
// Each pixel has R, G, B channels stored as float32 for memory efficiency.
// float32 provides more than enough precision for accumulating 8-bit webcam
// frames (max value ~7650 for 30 frames × 255).
type FloatImage struct {
	Width, Height int
	Pix           []float32 // len = Width * Height * 3 (RGB interleaved)
}

// NewFloatImage creates a zeroed float image.
func NewFloatImage(width, height int) *FloatImage {
	return &FloatImage{
		Width:  width,
		Height: height,
		Pix:    make([]float32, width*height*3),
	}
}

// FromNRGBA converts a standard image to a FloatImage.
func FromNRGBA(src *image.NRGBA) *FloatImage {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	fi := NewFloatImage(w, h)

	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				c := src.NRGBAAt(x+b.Min.X, y+b.Min.Y)
				idx := (y*w + x) * 3
				fi.Pix[idx] = float32(c.R)
				fi.Pix[idx+1] = float32(c.G)
				fi.Pix[idx+2] = float32(c.B)
			}
		}
	})
	return fi
}

// Add accumulates another FloatImage into this one (pixel-wise addition).
func (fi *FloatImage) Add(other *FloatImage) {
	parallelPix(len(fi.Pix), func(s, e int) {
		for i := s; i < e; i++ {
			fi.Pix[i] += other.Pix[i]
		}
	})
}

// Sub removes another FloatImage from this one (pixel-wise subtraction).
func (fi *FloatImage) Sub(other *FloatImage) {
	parallelPix(len(fi.Pix), func(s, e int) {
		for i := s; i < e; i++ {
			fi.Pix[i] -= other.Pix[i]
		}
	})
}

// Clone returns an independent copy of the FloatImage.
func (fi *FloatImage) Clone() *FloatImage {
	clone := &FloatImage{
		Width:  fi.Width,
		Height: fi.Height,
		Pix:    make([]float32, len(fi.Pix)),
	}
	copy(clone.Pix, fi.Pix)
	return clone
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

	parallelRows(fi.Height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < fi.Width; x++ {
				idx := (y*fi.Width + x) * 3
				r := stretchChannel(float64(fi.Pix[idx]), black, rng, invGamma)
				g := stretchChannel(float64(fi.Pix[idx+1]), black, rng, invGamma)
				b := stretchChannel(float64(fi.Pix[idx+2]), black, rng, invGamma)
				dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
			}
		}
	})
	return dst
}

// Normalized returns the accumulator divided by frameCount as a display image.
func (fi *FloatImage) Normalized(frameCount int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, fi.Width, fi.Height))
	if frameCount <= 0 {
		return dst
	}
	scale := 1.0 / float64(frameCount)

	parallelRows(fi.Height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < fi.Width; x++ {
				idx := (y*fi.Width + x) * 3
				r := clamp8(float64(fi.Pix[idx]) * scale)
				g := clamp8(float64(fi.Pix[idx+1]) * scale)
				b := clamp8(float64(fi.Pix[idx+2]) * scale)
				dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
			}
		}
	})
	return dst
}

// Stretch16 applies the same stretch as Stretch but returns a 16-bit NRGBA64
// image suitable for high-quality TIFF export.
func (fi *FloatImage) Stretch16(black, gamma, white float64) *image.NRGBA64 {
	dst := image.NewNRGBA64(image.Rect(0, 0, fi.Width, fi.Height))
	invGamma := 1.0 / gamma
	rng := white - black
	if rng <= 0 {
		rng = 1
	}

	parallelRows(fi.Height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < fi.Width; x++ {
				idx := (y*fi.Width + x) * 3
				r := stretchChannel16(float64(fi.Pix[idx]), black, rng, invGamma)
				g := stretchChannel16(float64(fi.Pix[idx+1]), black, rng, invGamma)
				b := stretchChannel16(float64(fi.Pix[idx+2]), black, rng, invGamma)
				dst.SetNRGBA64(x, y, color.NRGBA64{R: r, G: g, B: b, A: 0xFFFF})
			}
		}
	})
	return dst
}

// StretchAsinh applies an arcsinh stretch using a precomputed LUT for speed.
// Compresses bright stars while revealing faint nebulosity.
func (fi *FloatImage) StretchAsinh(black, beta, white float64) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, fi.Width, fi.Height))
	rng := white - black
	if rng <= 0 {
		rng = 1
	}

	// Build lookup table — maps normalised [0,1] → output [0,255].
	const lutSize = 4096
	var lut [lutSize + 1]uint8
	asinhBeta := math.Asinh(beta)
	if asinhBeta == 0 {
		asinhBeta = 1
	}
	for i := 0; i <= lutSize; i++ {
		t := float64(i) / lutSize
		stretched := math.Asinh(beta*t) / asinhBeta
		lut[i] = clamp8(stretched * 255)
	}

	invRng := 1.0 / rng
	parallelRows(fi.Height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < fi.Width; x++ {
				idx := (y*fi.Width + x) * 3
				r := lutLookup(&lut, (float64(fi.Pix[idx])-black)*invRng, lutSize)
				g := lutLookup(&lut, (float64(fi.Pix[idx+1])-black)*invRng, lutSize)
				b := lutLookup(&lut, (float64(fi.Pix[idx+2])-black)*invRng, lutSize)
				dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
			}
		}
	})
	return dst
}

// lutLookup does fast LUT-based channel mapping with linear interpolation.
func lutLookup(lut *[4097]uint8, normalized float64, size int) uint8 {
	if normalized <= 0 {
		return lut[0]
	}
	if normalized >= 1 {
		return lut[size]
	}
	pos := normalized * float64(size)
	lo := int(pos)
	frac := pos - float64(lo)
	a := float64(lut[lo])
	b := float64(lut[lo+1])
	return uint8(a + frac*(b-a))
}

// STFParams holds cached Auto STF parameters so they don't need recomputing every frame.
type STFParams struct {
	BlackPt float64
	Mid     float64
	Valid   bool
}

// ComputeSTFParams analyses the image to find optimal black point and midtone.
// Call this once when the accumulator changes, not every frame.
func (fi *FloatImage) ComputeSTFParams(frameScale float64) STFParams {
	n := len(fi.Pix)
	if n == 0 {
		return STFParams{}
	}

	// Sample up to 500k pixels for speed — sufficient for accurate stats.
	sampleN := n
	stride := 1
	if sampleN > 500000 {
		stride = sampleN / 500000
		sampleN = n / stride
	}

	scale := 1.0 / (frameScale * 255.0)
	samples := make([]float64, 0, sampleN)
	for i := 0; i < n; i += stride {
		samples = append(samples, float64(fi.Pix[i])*scale)
	}

	// Sort for median.
	sortFloat64sLarge(samples)
	median := samples[len(samples)/2]

	// MAD (median absolute deviation).
	for i, v := range samples {
		samples[i] = math.Abs(v - median)
	}
	sortFloat64sLarge(samples)
	mad := samples[len(samples)/2] * 1.4826

	blackPt := median - 2.8*mad
	if blackPt < 0 {
		blackPt = 0
	}

	return STFParams{BlackPt: blackPt, Mid: 0.25, Valid: true}
}

// StretchAutoSTF applies a Screen Transfer Function using precomputed params + LUT.
func (fi *FloatImage) StretchAutoSTF(frameScale float64, params STFParams) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, fi.Width, fi.Height))
	if !params.Valid {
		return dst
	}

	// Build LUT for the MTF: maps normalised [0,1] → output [0,255].
	const lutSize = 4096
	var lut [lutSize + 1]uint8
	for i := 0; i <= lutSize; i++ {
		x := float64(i) / lutSize
		lut[i] = stfValue(x, params.Mid)
	}

	scale := 1.0 / (frameScale * 255.0)
	denom := 1.0 - params.BlackPt
	if denom <= 0 {
		denom = 1
	}
	invDenom := 1.0 / denom

	parallelRows(fi.Height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < fi.Width; x++ {
				idx := (y*fi.Width + x) * 3
				r := lutLookup(&lut, (float64(fi.Pix[idx])*scale-params.BlackPt)*invDenom, lutSize)
				g := lutLookup(&lut, (float64(fi.Pix[idx+1])*scale-params.BlackPt)*invDenom, lutSize)
				b := lutLookup(&lut, (float64(fi.Pix[idx+2])*scale-params.BlackPt)*invDenom, lutSize)
				dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
			}
		}
	})
	return dst
}

func stfValue(x, mid float64) uint8 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 255
	}
	denom := (2*mid-1)*x - mid
	if denom == 0 {
		return clamp8(x * 255)
	}
	stretched := (mid - 1) * x / denom
	if stretched < 0 {
		stretched = 0
	}
	if stretched > 1 {
		stretched = 1
	}
	return clamp8(stretched * 255)
}

// sortFloat64sLarge uses shellsort — good for medium-size arrays (up to ~500k).
func sortFloat64sLarge(a []float64) {
	if len(a) <= 64 {
		sortFloat64s(a)
		return
	}
	n := len(a)
	for gap := n / 2; gap > 0; gap /= 2 {
		for i := gap; i < n; i++ {
			v := a[i]
			j := i
			for j >= gap && a[j-gap] > v {
				a[j] = a[j-gap]
				j -= gap
			}
			a[j] = v
		}
	}
}

func stretchChannel16(val, black, rng, invGamma float64) uint16 {
	normalized := (val - black) / rng
	if normalized < 0 {
		normalized = 0
	}
	if normalized > 1 {
		normalized = 1
	}
	stretched := math.Pow(normalized, invGamma)
	v := stretched * 65535
	if v < 0 {
		return 0
	}
	if v > 65535 {
		return 65535
	}
	return uint16(v)
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

// MaxFrom recomputes this FloatImage as the per-pixel maximum of the given frames.
func (fi *FloatImage) MaxFrom(frames []*FloatImage) {
	if len(frames) == 0 {
		return
	}
	copy(fi.Pix, frames[0].Pix)
	nf := len(frames)
	parallelPix(len(fi.Pix), func(s, e int) {
		for i := 1; i < nf; i++ {
			for j := s; j < e; j++ {
				if frames[i].Pix[j] > fi.Pix[j] {
					fi.Pix[j] = frames[i].Pix[j]
				}
			}
		}
	})
}

// MedianFrom recomputes this FloatImage as the per-pixel median of the given frames.
func (fi *FloatImage) MedianFrom(frames []*FloatImage) {
	n := len(frames)
	if n == 0 {
		return
	}
	if n == 1 {
		copy(fi.Pix, frames[0].Pix)
		return
	}
	parallelPix(len(fi.Pix), func(s, e int) {
		buf := make([]float32, n)
		for j := s; j < e; j++ {
			for k := 0; k < n; k++ {
				buf[k] = frames[k].Pix[j]
			}
			sortFloat32s(buf)
			if n%2 == 1 {
				fi.Pix[j] = buf[n/2]
			} else {
				fi.Pix[j] = (buf[n/2-1] + buf[n/2]) / 2
			}
		}
	})
}

// SigmaClipFrom recomputes this FloatImage as the sigma-clipped mean of the
// given frames. Pixels beyond sigma standard deviations from the mean are
// rejected, then the remaining values are averaged. Two iterations.
func (fi *FloatImage) SigmaClipFrom(frames []*FloatImage, sigma float64) {
	n := len(frames)
	if n == 0 {
		return
	}
	if n < 3 {
		// Not enough frames for meaningful clipping — just average.
		fi.SumFrom(frames)
		scale := float32(1.0 / float64(n))
		parallelPix(len(fi.Pix), func(s, e int) {
			for j := s; j < e; j++ {
				fi.Pix[j] *= scale
			}
		})
		return
	}
	sigmaF := float32(sigma)
	parallelPix(len(fi.Pix), func(s, e int) {
		// Each goroutine gets its own buffer to avoid aliasing issues.
		vals := make([]float32, n)
		for j := s; j < e; j++ {
			for k := 0; k < n; k++ {
				vals[k] = frames[k].Pix[j]
			}
			// Two iterations of sigma clipping.
			clipped := vals[:n]
			for iter := 0; iter < 2; iter++ {
				mean, stddev := meanStdDev32(clipped)
				lo := mean - sigmaF*stddev
				hi := mean + sigmaF*stddev
				kept := clipped[:0]
				for _, v := range clipped {
					if v >= lo && v <= hi {
						kept = append(kept, v)
					}
				}
				if len(kept) < 2 {
					break
				}
				clipped = kept
			}
			var sum float32
			for _, v := range clipped {
				sum += v
			}
			fi.Pix[j] = sum / float32(len(clipped))
		}
	})
}

// SumFrom recomputes this FloatImage as the sum of the given frames.
func (fi *FloatImage) SumFrom(frames []*FloatImage) {
	if len(frames) == 0 {
		return
	}
	copy(fi.Pix, frames[0].Pix)
	nf := len(frames)
	parallelPix(len(fi.Pix), func(s, e int) {
		for i := 1; i < nf; i++ {
			for j := s; j < e; j++ {
				fi.Pix[j] += frames[i].Pix[j]
			}
		}
	})
}

func meanStdDev32(vals []float32) (float32, float32) {
	n := float32(len(vals))
	if n == 0 {
		return 0, 0
	}
	var sum float32
	for _, v := range vals {
		sum += v
	}
	mean := sum / n
	var variance float32
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	return mean, float32(math.Sqrt(float64(variance / n)))
}

// sortFloat32s is a simple insertion sort — efficient for small N (≤300 ring frames).
func sortFloat32s(a []float32) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

// sortFloat64s is insertion sort for float64 slices (used by ComputeSTFParams).
func sortFloat64s(a []float64) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
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
