package engine

import (
	"image"
	"math"
	"sort"
)

// Star represents a detected star centroid.
type Star struct {
	X, Y       float64 // Sub-pixel centroid position
	Brightness float64 // Total flux (sum of pixel values in blob)
	Radius     float64 // Approximate radius
}

// DetectStars finds bright point sources in an NRGBA image.
// It converts to grayscale, applies a threshold, finds connected blobs,
// and computes brightness-weighted centroids.
func DetectStars(img *image.NRGBA, maxStars int) []Star {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	gray := Grayscale(img)

	// Compute adaptive threshold: mean + 3*stddev.
	threshold := adaptiveThreshold(gray, 3.0)

	// Find connected bright blobs via flood fill.
	visited := make([]bool, w*h)
	var stars []Star

	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*w + x
			if visited[idx] || gray[idx] < threshold {
				continue
			}

			// Flood fill to find all connected pixels above threshold.
			blob := floodFill(gray, visited, w, h, x, y, threshold)
			if len(blob) < 2 || len(blob) > 500 {
				// Skip single pixels (noise) and very large blobs (not stars).
				continue
			}

			star := computeCentroid(gray, blob, w)
			stars = append(stars, star)
		}
	}

	// Sort by brightness (brightest first) and keep top N.
	sort.Slice(stars, func(i, j int) bool {
		return stars[i].Brightness > stars[j].Brightness
	})

	if len(stars) > maxStars {
		stars = stars[:maxStars]
	}

	return stars
}

// adaptiveThreshold computes mean + k*stddev of the image.
func adaptiveThreshold(gray []float64, k float64) float64 {
	n := float64(len(gray))
	if n == 0 {
		return 128
	}

	var sum, sumSq float64
	for _, v := range gray {
		sum += v
		sumSq += v * v
	}
	mean := sum / n
	variance := sumSq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	stddev := math.Sqrt(variance)

	return mean + k*stddev
}

type point struct{ x, y int }

// floodFill finds all connected pixels above threshold starting from (sx, sy).
func floodFill(gray []float64, visited []bool, w, h, sx, sy int, threshold float64) []point {
	stack := []point{{sx, sy}}
	var blob []point

	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		idx := p.y*w + p.x
		if p.x < 0 || p.x >= w || p.y < 0 || p.y >= h {
			continue
		}
		if visited[idx] || gray[idx] < threshold {
			continue
		}

		visited[idx] = true
		blob = append(blob, p)

		// 8-connected neighbors.
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				stack = append(stack, point{p.x + dx, p.y + dy})
			}
		}
	}

	return blob
}

// computeCentroid calculates the brightness-weighted centroid of a blob.
func computeCentroid(gray []float64, blob []point, w int) Star {
	var sumX, sumY, sumW, maxBright float64

	for _, p := range blob {
		val := gray[p.y*w+p.x]
		sumX += float64(p.x) * val
		sumY += float64(p.y) * val
		sumW += val
		if val > maxBright {
			maxBright = val
		}
	}

	// Approximate radius from blob area.
	radius := math.Sqrt(float64(len(blob)) / math.Pi)

	return Star{
		X:          sumX / sumW,
		Y:          sumY / sumW,
		Brightness: sumW,
		Radius:     radius,
	}
}
