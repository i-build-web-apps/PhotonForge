// +build ignore

// gen_test_stars generates synthetic starfield images for testing PhotonForge.
// Run: go run tools/gen_test_stars.go
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
)

const (
	width    = 640
	height   = 480
	numStars = 40
	numFrames = 10
)

type star struct {
	x, y       float64
	brightness uint8
	radius     float64
}

func main() {
	outDir := "sim"
	os.MkdirAll(outDir, 0755)

	// Generate a fixed starfield.
	stars := make([]star, numStars)
	for i := range stars {
		stars[i] = star{
			x:          rand.Float64() * width,
			y:          rand.Float64() * height,
			brightness: uint8(100 + rand.Intn(156)),
			radius:     1.0 + rand.Float64()*2.0,
		}
	}

	for f := 0; f < numFrames; f++ {
		img := image.NewNRGBA(image.Rect(0, 0, width, height))

		// Dark sky background with slight noise.
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				noise := uint8(rand.Intn(8))
				img.SetNRGBA(x, y, color.NRGBA{R: noise, G: noise, B: noise, A: 255})
			}
		}

		// Draw stars as gaussian-ish blobs.
		for _, s := range stars {
			r := int(s.radius*3) + 1
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					dist := math.Sqrt(float64(dx*dx + dy*dy))
					if dist > float64(r) {
						continue
					}
					intensity := float64(s.brightness) * math.Exp(-(dist*dist)/(2*s.radius*s.radius))
					px := int(s.x) + dx
					py := int(s.y) + dy
					if px < 0 || px >= width || py < 0 || py >= height {
						continue
					}
					existing := img.NRGBAAt(px, py)
					v := uint8(math.Min(255, float64(existing.R)+intensity))
					img.SetNRGBA(px, py, color.NRGBA{R: v, G: v, B: v, A: 255})
				}
			}
		}

		path := filepath.Join(outDir, fmt.Sprintf("stars_%03d.png", f))
		file, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating %s: %v\n", path, err)
			os.Exit(1)
		}
		png.Encode(file, img)
		file.Close()
		fmt.Printf("Generated %s\n", path)
	}
}
