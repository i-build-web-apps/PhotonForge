package provider

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"

	"gocv.io/x/gocv"
)

// DirectoryProvider streams images from a folder, simulating a live camera feed.
// When jitter is enabled it applies a random 1-3 pixel shift to each frame,
// letting you verify that the alignment engine can correct for tracking drift.
type DirectoryProvider struct {
	Dir    string
	Jitter bool // add random sub-pixel shifts to simulate tracking error
	Loop   bool // restart from the beginning after the last image

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

	exts := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true, ".bmp": true}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
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

func (d *DirectoryProvider) Read(dst *gocv.Mat) bool {
	if len(d.files) == 0 {
		return false
	}

	if d.index >= len(d.files) {
		if !d.Loop {
			return false
		}
		d.index = 0
	}

	img := gocv.IMRead(d.files[d.index], gocv.IMReadColor)
	if img.Empty() {
		return false
	}
	d.index++

	if d.Jitter {
		d.applyJitter(&img)
	}

	img.CopyTo(dst)
	img.Close()
	return true
}

func (d *DirectoryProvider) Close() error {
	d.files = nil
	d.index = 0
	return nil
}

// applyJitter shifts the image by a random 1-3 pixel offset in X and Y using
// an affine warp, simulating telescope tracking error.
func (d *DirectoryProvider) applyJitter(mat *gocv.Mat) {
	dx := float64(1 + rand.Intn(3))
	dy := float64(1 + rand.Intn(3))
	// Randomly negate to shift in any direction.
	if rand.Intn(2) == 0 {
		dx = -dx
	}
	if rand.Intn(2) == 0 {
		dy = -dy
	}

	// 2x3 affine translation matrix: [[1, 0, dx], [0, 1, dy]]
	warpMat := gocv.NewMatWithSize(2, 3, gocv.MatTypeCV64F)
	defer warpMat.Close()
	warpMat.SetDoubleAt(0, 0, 1)
	warpMat.SetDoubleAt(0, 1, 0)
	warpMat.SetDoubleAt(0, 2, dx)
	warpMat.SetDoubleAt(1, 0, 0)
	warpMat.SetDoubleAt(1, 1, 1)
	warpMat.SetDoubleAt(1, 2, dy)

	dst := gocv.NewMat()
	gocv.WarpAffine(*mat, &dst, warpMat, mat.Size())
	dst.CopyTo(mat)
	dst.Close()
}
