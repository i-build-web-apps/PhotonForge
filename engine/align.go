package engine

import (
	"image"
	"image/color"
	"math"
)

// Aligner registers incoming frames against a reference frame using
// star centroid matching. It detects stars, matches triangle patterns,
// and computes an affine transform to warp each frame into alignment.
type Aligner struct {
	refStars []Star
	refSet   bool
	debug    bool
	maxStars int
}

func NewAligner(debug bool) *Aligner {
	return &Aligner{
		debug:    debug,
		maxStars: 80,
	}
}

func (a *Aligner) Reset() {
	a.refSet = false
	a.refStars = nil
}

// AlignResult holds the output of an alignment pass.
type AlignResult struct {
	Image   *image.NRGBA
	Matches int
	Err     error
}

// Align warps the input frame to match the reference. The first frame
// after creation or Reset() becomes the reference.
func (a *Aligner) Align(frame *image.NRGBA) AlignResult {
	stars := DetectStars(frame, a.maxStars)

	if !a.refSet {
		a.refStars = stars
		a.refSet = true
		result := cloneImage(frame)
		if a.debug {
			drawStars(result, stars, color.NRGBA{R: 0, G: 255, B: 0, A: 255})
		}
		return AlignResult{Image: result, Matches: len(stars)}
	}

	// Match stars between current frame and reference using triangle voting.
	pairs := matchStars(stars, a.refStars)

	if a.debug {
		debugImg := cloneImage(frame)
		drawStars(debugImg, stars, color.NRGBA{R: 0, G: 255, B: 0, A: 255})
		for _, p := range pairs {
			drawLine(debugImg, int(stars[p[0]].X), int(stars[p[0]].Y),
				int(a.refStars[p[1]].X), int(a.refStars[p[1]].Y),
				color.NRGBA{R: 255, G: 255, B: 0, A: 255})
		}
		if len(pairs) < 3 {
			return AlignResult{Image: debugImg, Matches: len(pairs)}
		}
		tx := computeAffine(stars, a.refStars, pairs)
		result := warpAffine(debugImg, tx)
		return AlignResult{Image: result, Matches: len(pairs)}
	}

	if len(pairs) < 3 {
		return AlignResult{Image: cloneImage(frame), Matches: len(pairs)}
	}

	tx := computeAffine(stars, a.refStars, pairs)
	result := warpAffine(frame, tx)
	return AlignResult{Image: result, Matches: len(pairs)}
}

// matchStars pairs stars between two frames using triangle similarity voting.
// Returns slice of [srcIdx, dstIdx] pairs.
func matchStars(src, dst []Star) [][2]int {
	if len(src) < 3 || len(dst) < 3 {
		return nil
	}

	// Limit to brightest stars for triangle matching.
	maxTri := 20
	if len(src) < maxTri {
		maxTri = len(src)
	}
	maxTriDst := 20
	if len(dst) < maxTriDst {
		maxTriDst = len(dst)
	}

	// Vote matrix: votes[srcIdx][dstIdx] counts how many triangles agree.
	votes := make([][]int, maxTri)
	for i := range votes {
		votes[i] = make([]int, maxTriDst)
	}

	// Compare all triangles in src with all triangles in dst.
	for i := 0; i < maxTri-2; i++ {
		for j := i + 1; j < maxTri-1; j++ {
			for k := j + 1; k < maxTri; k++ {
				srcTri := triangle{src[i], src[j], src[k]}
				srcRatios := srcTri.sideRatios()

				for di := 0; di < maxTriDst-2; di++ {
					for dj := di + 1; dj < maxTriDst-1; dj++ {
						for dk := dj + 1; dk < maxTriDst; dk++ {
							dstTri := triangle{dst[di], dst[dj], dst[dk]}
							dstRatios := dstTri.sideRatios()

							perm := matchRatios(srcRatios, dstRatios, 0.05)
							if perm < 0 {
								continue
							}
							// Map src vertices to dst vertices based on permutation.
							srcIdxs := [3]int{i, j, k}
							dstIdxs := [3]int{di, dj, dk}
							perms := [][3]int{
								{0, 1, 2}, {0, 2, 1}, {1, 0, 2},
								{1, 2, 0}, {2, 0, 1}, {2, 1, 0},
							}
							p := perms[perm]
							for v := 0; v < 3; v++ {
								votes[srcIdxs[v]][dstIdxs[p[v]]]++
							}
						}
					}
				}
			}
		}
	}

	// Extract best matches from vote matrix.
	var pairs [][2]int
	usedSrc := make(map[int]bool)
	usedDst := make(map[int]bool)

	for {
		bestVote := 0
		bestS, bestD := -1, -1
		for s := 0; s < maxTri; s++ {
			if usedSrc[s] {
				continue
			}
			for d := 0; d < maxTriDst; d++ {
				if usedDst[d] {
					continue
				}
				if votes[s][d] > bestVote {
					bestVote = votes[s][d]
					bestS = s
					bestD = d
				}
			}
		}
		if bestVote < 2 {
			break
		}
		pairs = append(pairs, [2]int{bestS, bestD})
		usedSrc[bestS] = true
		usedDst[bestD] = true
	}

	return pairs
}

type triangle struct {
	A, B, C Star
}

// sideRatios returns the three side lengths of the triangle, sorted and
// normalized by the longest side, giving a scale-invariant shape descriptor.
func (t triangle) sideRatios() [3]float64 {
	ab := dist(t.A, t.B)
	bc := dist(t.B, t.C)
	ca := dist(t.C, t.A)

	sides := [3]float64{ab, bc, ca}
	// Sort ascending.
	if sides[0] > sides[1] {
		sides[0], sides[1] = sides[1], sides[0]
	}
	if sides[1] > sides[2] {
		sides[1], sides[2] = sides[2], sides[1]
	}
	if sides[0] > sides[1] {
		sides[0], sides[1] = sides[1], sides[0]
	}

	// Normalize by longest side.
	if sides[2] > 0 {
		sides[0] /= sides[2]
		sides[1] /= sides[2]
		sides[2] = 1.0
	}
	return sides
}

func dist(a, b Star) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// matchRatios checks if two sets of side ratios are similar within tolerance.
// Returns the permutation index (0-5) or -1 if no match.
func matchRatios(a, b [3]float64, tol float64) int {
	// Since both are sorted, just compare directly.
	if math.Abs(a[0]-b[0]) < tol && math.Abs(a[1]-b[1]) < tol {
		return 0
	}
	return -1
}

// computeAffine finds the best-fit affine transform mapping src stars to dst stars.
// Uses least-squares over matched pairs to find the 2x3 affine matrix.
// Returns [a, b, tx, c, d, ty] where:
//   dst_x = a*src_x + b*src_y + tx
//   dst_y = c*src_x + d*src_y + ty
func computeAffine(src, dst []Star, pairs [][2]int) [6]float64 {
	n := len(pairs)
	if n < 3 {
		return [6]float64{1, 0, 0, 0, 1, 0} // identity
	}

	// Build normal equations: A^T * A * x = A^T * b
	// For each pair: [sx, sy, 1, 0, 0, 0] * [a,b,tx,c,d,ty]^T = dx
	//                [0, 0, 0, sx, sy, 1]                       = dy
	var ata [6][6]float64
	var atb [6]float64

	for _, p := range pairs {
		sx := src[p[0]].X
		sy := src[p[0]].Y
		dx := dst[p[1]].X
		dy := dst[p[1]].Y

		row1 := [6]float64{sx, sy, 1, 0, 0, 0}
		row2 := [6]float64{0, 0, 0, sx, sy, 1}

		for i := 0; i < 6; i++ {
			atb[i] += row1[i]*dx + row2[i]*dy
			for j := 0; j < 6; j++ {
				ata[i][j] += row1[i]*row1[j] + row2[i]*row2[j]
			}
		}
	}

	// Solve 6x6 system via Gaussian elimination.
	return solveLinear6(ata, atb)
}

// solveLinear6 solves a 6x6 linear system using Gaussian elimination with partial pivoting.
func solveLinear6(a [6][6]float64, b [6]float64) [6]float64 {
	// Augmented matrix.
	var aug [6][7]float64
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			aug[i][j] = a[i][j]
		}
		aug[i][6] = b[i]
	}

	// Forward elimination with partial pivoting.
	for col := 0; col < 6; col++ {
		maxRow := col
		maxVal := math.Abs(aug[col][col])
		for row := col + 1; row < 6; row++ {
			if math.Abs(aug[row][col]) > maxVal {
				maxVal = math.Abs(aug[row][col])
				maxRow = row
			}
		}
		aug[col], aug[maxRow] = aug[maxRow], aug[col]

		if math.Abs(aug[col][col]) < 1e-12 {
			// Singular — return identity.
			return [6]float64{1, 0, 0, 0, 1, 0}
		}

		for row := col + 1; row < 6; row++ {
			factor := aug[row][col] / aug[col][col]
			for j := col; j < 7; j++ {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	// Back substitution.
	var x [6]float64
	for i := 5; i >= 0; i-- {
		x[i] = aug[i][6]
		for j := i + 1; j < 6; j++ {
			x[i] -= aug[i][j] * x[j]
		}
		x[i] /= aug[i][i]
	}

	return x
}

// warpAffine applies an affine transform to an image using bilinear interpolation.
func warpAffine(src *image.NRGBA, tx [6]float64) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))

	// Invert the affine transform so we can sample src for each dst pixel.
	inv := invertAffine(tx)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Map destination pixel back to source.
			sx := inv[0]*float64(x) + inv[1]*float64(y) + inv[2]
			sy := inv[3]*float64(x) + inv[4]*float64(y) + inv[5]

			dst.SetNRGBA(x, y, bilinearSample(src, sx, sy))
		}
	}

	return dst
}

// invertAffine computes the inverse of a 2x3 affine matrix.
func invertAffine(tx [6]float64) [6]float64 {
	a, b, txv := tx[0], tx[1], tx[2]
	c, d, tyv := tx[3], tx[4], tx[5]

	det := a*d - b*c
	if math.Abs(det) < 1e-12 {
		return [6]float64{1, 0, 0, 0, 1, 0}
	}

	invDet := 1.0 / det
	return [6]float64{
		d * invDet, -b * invDet, (b*tyv - d*txv) * invDet,
		-c * invDet, a * invDet, (c*txv - a*tyv) * invDet,
	}
}

// bilinearSample reads a pixel from src at fractional coordinates using bilinear interpolation.
func bilinearSample(src *image.NRGBA, sx, sy float64) color.NRGBA {
	b := src.Bounds()
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	x1 := x0 + 1
	y1 := y0 + 1

	if x0 < b.Min.X || y0 < b.Min.Y || x1 >= b.Max.X || y1 >= b.Max.Y {
		return color.NRGBA{A: 255}
	}

	fx := sx - float64(x0)
	fy := sy - float64(y0)

	c00 := src.NRGBAAt(x0, y0)
	c10 := src.NRGBAAt(x1, y0)
	c01 := src.NRGBAAt(x0, y1)
	c11 := src.NRGBAAt(x1, y1)

	lerp := func(a, b uint8, t float64) uint8 {
		return uint8(float64(a)*(1-t) + float64(b)*t)
	}

	topR := float64(lerp(c00.R, c10.R, fx))
	topG := float64(lerp(c00.G, c10.G, fx))
	topB := float64(lerp(c00.B, c10.B, fx))
	botR := float64(lerp(c01.R, c11.R, fx))
	botG := float64(lerp(c01.G, c11.G, fx))
	botB := float64(lerp(c01.B, c11.B, fx))

	return color.NRGBA{
		R: uint8(topR*(1-fy) + botR*fy),
		G: uint8(topG*(1-fy) + botG*fy),
		B: uint8(topB*(1-fy) + botB*fy),
		A: 255,
	}
}

func cloneImage(src *image.NRGBA) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	copy(dst.Pix, src.Pix)
	return dst
}

func drawStars(img *image.NRGBA, stars []Star, c color.NRGBA) {
	for _, s := range stars {
		drawCircle(img, int(s.X), int(s.Y), int(s.Radius)+2, c)
	}
}

func drawCircle(img *image.NRGBA, cx, cy, r int, c color.NRGBA) {
	b := img.Bounds()
	for angle := 0.0; angle < 2*math.Pi; angle += 0.1 {
		x := cx + int(float64(r)*math.Cos(angle))
		y := cy + int(float64(r)*math.Sin(angle))
		if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
			img.SetNRGBA(x, y, c)
		}
	}
}

func drawLine(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	b := img.Bounds()
	dx := math.Abs(float64(x1 - x0))
	dy := math.Abs(float64(y1 - y0))
	steps := int(math.Max(dx, dy))
	if steps == 0 {
		return
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := int(float64(x0)*(1-t) + float64(x1)*t)
		y := int(float64(y0)*(1-t) + float64(y1)*t)
		if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
			img.SetNRGBA(x, y, c)
		}
	}
}
