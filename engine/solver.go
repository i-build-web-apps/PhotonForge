package engine

import (
	"fmt"
	"math"
	"sort"
)

// SolveResult holds the output of a plate solve attempt.
type SolveResult struct {
	Solved    bool
	CenterRA  float64 // degrees
	CenterDec float64 // degrees
	FOVDeg    float64 // estimated field of view in degrees
	Rotation  float64 // field rotation in degrees
	Matches   int     // number of verified inlier star pairs
	Stars     int     // total catalog stars in matched region
}

// CatalogStar is a minimal star record for plate solving.
type CatalogStar struct {
	RA, Dec   float64 // degrees
	Magnitude float64
	Name      string
	CatalogID string
}

// PlateSolver matches detected image stars against a catalog of known stars.
type PlateSolver struct {
	Catalog []CatalogStar
	tiles   []skyTile
}

type skyTile struct {
	raCenter, decCenter float64
	stars               []CatalogStar
}

// NewPlateSolver creates a solver from catalog stars.
// Stars should be pre-sorted by magnitude (brightest first).
func NewPlateSolver(catalog []CatalogStar) *PlateSolver {
	ps := &PlateSolver{Catalog: catalog}
	ps.buildTiles()
	return ps
}

// buildTiles groups catalog stars into overlapping sky tiles for efficient searching.
func (ps *PlateSolver) buildTiles() {
	tileSize := 15.0 // degrees per tile
	overlap := 5.0   // degree overlap

	step := tileSize - overlap
	for dec := -90.0; dec <= 90.0; dec += step {
		decMin := dec - tileSize/2
		decMax := dec + tileSize/2
		if decMin < -90 {
			decMin = -90
		}
		if decMax > 90 {
			decMax = 90
		}

		cosDec := math.Cos(dec * math.Pi / 180)
		raStep := step
		if cosDec > 0.1 {
			raStep = step / cosDec
		} else {
			raStep = 360
		}

		for ra := 0.0; ra < 360.0; ra += raStep {
			raMin := ra - tileSize/(2*math.Max(cosDec, 0.1))
			raMax := ra + tileSize/(2*math.Max(cosDec, 0.1))

			var tileStars []CatalogStar
			for _, s := range ps.Catalog {
				if s.Dec >= decMin && s.Dec <= decMax && raInRange(s.RA, raMin, raMax) {
					tileStars = append(tileStars, s)
				}
			}
			if len(tileStars) >= 4 {
				if len(tileStars) > 40 {
					tileStars = tileStars[:40]
				}
				ps.tiles = append(ps.tiles, skyTile{
					raCenter:  ra,
					decCenter: dec,
					stars:     tileStars,
				})
			}
		}
	}
}

func raInRange(ra, min, max float64) bool {
	for min < 0 {
		min += 360
	}
	for max > 360 {
		max -= 360
	}
	if min <= max {
		return ra >= min && ra <= max
	}
	return ra >= min || ra <= max
}

// Solve attempts to match detected image stars against the catalog.
// tryFOVs is a list of field-of-view values (degrees) to attempt.
// Returns the best match, or a result with Solved=false.
func (ps *PlateSolver) Solve(imageStars []Star, tryFOVs []float64) SolveResult {
	if len(imageStars) < 4 || len(ps.tiles) == 0 {
		return SolveResult{}
	}

	// Use brightest stars for matching
	maxImg := 20
	if len(imageStars) > maxImg {
		imageStars = imageStars[:maxImg]
	}

	// Normalize image star positions to [-1, 1] range for scale-invariant matching.
	// Find bounding box of image stars.
	var minX, minY, maxX, maxY float64
	minX, minY = imageStars[0].X, imageStars[0].Y
	maxX, maxY = minX, minY
	for _, s := range imageStars[1:] {
		if s.X < minX {
			minX = s.X
		}
		if s.X > maxX {
			maxX = s.X
		}
		if s.Y < minY {
			minY = s.Y
		}
		if s.Y > maxY {
			maxY = s.Y
		}
	}
	imgSpan := maxX - minX
	if maxY-minY > imgSpan {
		imgSpan = maxY - minY
	}
	if imgSpan < 1 {
		return SolveResult{}
	}
	imgCenterX := (minX + maxX) / 2
	imgCenterY := (minY + maxY) / 2

	// Normalized image stars (centered, scaled to unit size)
	normImg := make([]Star, len(imageStars))
	for i, s := range imageStars {
		normImg[i] = Star{
			X:          (s.X - imgCenterX) / imgSpan,
			Y:          (s.Y - imgCenterY) / imgSpan,
			Brightness: s.Brightness,
		}
	}

	imgTris := buildTriDescriptors(normImg)
	if len(imgTris) == 0 {
		return SolveResult{}
	}

	if len(tryFOVs) == 0 {
		tryFOVs = []float64{0.5, 1.0, 2.0, 3.0, 5.0, 8.0, 12.0, 20.0}
	}

	bestResult := SolveResult{}

	for _, tile := range ps.tiles {
		if len(tile.stars) < 4 {
			continue
		}

		for _, fov := range tryFOVs {
			projected := projectStars(tile.stars, tile.raCenter, tile.decCenter, fov, 1000)
			if len(projected) < 4 {
				continue
			}

			var pMinX, pMinY, pMaxX, pMaxY float64
			pMinX, pMinY = projected[0].X, projected[0].Y
			pMaxX, pMaxY = pMinX, pMinY
			for _, s := range projected[1:] {
				if s.X < pMinX {
					pMinX = s.X
				}
				if s.X > pMaxX {
					pMaxX = s.X
				}
				if s.Y < pMinY {
					pMinY = s.Y
				}
				if s.Y > pMaxY {
					pMaxY = s.Y
				}
			}
			pSpan := pMaxX - pMinX
			if pMaxY-pMinY > pSpan {
				pSpan = pMaxY - pMinY
			}
			if pSpan < 1 {
				continue
			}
			pCenterX := (pMinX + pMaxX) / 2
			pCenterY := (pMinY + pMaxY) / 2

			normCat := make([]Star, len(projected))
			for i, s := range projected {
				normCat[i] = Star{
					X:          (s.X - pCenterX) / pSpan,
					Y:          (s.Y - pCenterY) / pSpan,
					Brightness: s.Brightness,
				}
			}

			catTris := buildTriDescriptors(normCat)

			// Find matching triangles and extract star correspondences
			pairs := matchTriCorrespondences(imgTris, catTris, 0.01)
			if len(pairs) < 5 {
				continue
			}

			// Compute similarity transform from correspondences
			tx, ok := fitSimilarityTransform(normImg, normCat, pairs)
			if !ok {
				continue
			}

			// Count inliers with tight threshold and compute residual error.
			inlierThreshold := 0.015 // in normalized coords (~1.5% of field span)
			inliers, meanResidual := countInliersWithResidual(normImg, normCat, tx, inlierThreshold)

			// Require at least 50% of image stars to be inliers
			minInliers := len(normImg) / 2
			if minInliers < 8 {
				minInliers = 8
			}

			if inliers >= minInliers && meanResidual < 0.01 {
				fmt.Printf("SOLVE candidate: tile RA=%.1f Dec=%.1f FOV=%.1f° pairs=%d inliers=%d/%d residual=%.4f (best=%d)\n",
					tile.raCenter, tile.decCenter, fov, len(pairs), inliers, len(normImg), meanResidual, bestResult.Matches)
			}

			if inliers > bestResult.Matches && inliers >= minInliers && meanResidual < 0.01 {
				centerRA, centerDec := refineSolveCenter(
					tx, imgCenterX, imgCenterY, imgSpan,
					pCenterX, pCenterY, pSpan,
					tile.raCenter, tile.decCenter, fov,
				)

				bestResult = SolveResult{
					Solved:    true,
					CenterRA:  centerRA,
					CenterDec: centerDec,
					FOVDeg:    fov,
					Rotation:  math.Atan2(tx.sin, tx.cos) * 180 / math.Pi,
					Matches:   inliers,
					Stars:     len(tile.stars),
				}
			}
		}
	}

	if bestResult.Solved {
		fmt.Printf("SOLVE result: RA=%.3f Dec=%.3f FOV=%.1f° rot=%.1f° inliers=%d\n",
			bestResult.CenterRA, bestResult.CenterDec, bestResult.FOVDeg, bestResult.Rotation, bestResult.Matches)
	} else {
		fmt.Println("SOLVE: no valid match found")
	}
	return bestResult
}

// starPair maps an image star index to a catalog star index.
type starPair struct {
	imgIdx, catIdx int
}

// similarityTransform represents a 2D similarity: rotation + uniform scale + translation.
// x' = scale*(cos*x - sin*y) + tx
// y' = scale*(sin*x + cos*y) + ty
type similarityTransform struct {
	cos, sin float64 // rotation components (unit vector)
	scale    float64
	tx, ty   float64
}

func (t *similarityTransform) apply(x, y float64) (float64, float64) {
	rx := t.scale*(t.cos*x-t.sin*y) + t.tx
	ry := t.scale*(t.sin*x+t.cos*y) + t.ty
	return rx, ry
}

// triDescriptor is a scale-invariant triangle shape descriptor.
type triDescriptor struct {
	ratios [2]float64 // two normalized side ratios (shortest/longest, middle/longest)
	idxs   [3]int     // indices into the star array
	// order: vertex indices sorted by opposite side length (shortest opp first).
	// This gives a canonical vertex ordering invariant to coordinate system,
	// so matching triangles can establish vertex correspondence.
	order [3]int
}

func buildTriDescriptors(stars []Star) []triDescriptor {
	n := len(stars)
	if n < 3 {
		return nil
	}
	maxN := 12
	if n > maxN {
		n = maxN
	}

	var descs []triDescriptor
	for i := 0; i < n-2; i++ {
		for j := i + 1; j < n-1; j++ {
			for k := j + 1; k < n; k++ {
				// Side opposite vertex i is (j,k), opposite j is (i,k), opposite k is (i,j)
				sideOppI := starDist(stars[j], stars[k])
				sideOppJ := starDist(stars[i], stars[k])
				sideOppK := starDist(stars[i], stars[j])

				sides := [3]float64{sideOppI, sideOppJ, sideOppK}
				verts := [3]int{i, j, k}

				// Sort by opposite side length (bubble sort for 3 elements)
				if sides[0] > sides[1] {
					sides[0], sides[1] = sides[1], sides[0]
					verts[0], verts[1] = verts[1], verts[0]
				}
				if sides[1] > sides[2] {
					sides[1], sides[2] = sides[2], sides[1]
					verts[1], verts[2] = verts[2], verts[1]
				}
				if sides[0] > sides[1] {
					sides[0], sides[1] = sides[1], sides[0]
					verts[0], verts[1] = verts[1], verts[0]
				}

				if sides[2] < 1e-6 {
					continue
				}
				descs = append(descs, triDescriptor{
					ratios: [2]float64{sides[0] / sides[2], sides[1] / sides[2]},
					idxs:   [3]int{i, j, k},
					order:  verts,
				})
			}
		}
	}
	return descs
}

func starDist(a, b Star) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// matchTriCorrespondences finds matching triangles and extracts star pair votes.
// Returns the most-voted unique star correspondences.
func matchTriCorrespondences(imgTris, catTris []triDescriptor, tol float64) []starPair {
	type pairKey struct{ i, c int }
	votes := make(map[pairKey]int)

	for _, it := range imgTris {
		for _, ct := range catTris {
			if math.Abs(it.ratios[0]-ct.ratios[0]) < tol &&
				math.Abs(it.ratios[1]-ct.ratios[1]) < tol {
				// Matched triangles — use canonical vertex ordering
				// (sorted by opposite side length) to establish correspondence.
				for k := 0; k < 3; k++ {
					votes[pairKey{it.order[k], ct.order[k]}]++
				}
			}
		}
	}

	if len(votes) == 0 {
		return nil
	}

	type votedPair struct {
		pair  pairKey
		count int
	}
	var vp []votedPair
	for k, v := range votes {
		vp = append(vp, votedPair{k, v})
	}
	sort.Slice(vp, func(i, j int) bool {
		return vp[i].count > vp[j].count
	})

	// Greedy assignment: pick highest-voted pairs, each star used at most once.
	usedImg := make(map[int]bool)
	usedCat := make(map[int]bool)
	var pairs []starPair
	for _, v := range vp {
		if usedImg[v.pair.i] || usedCat[v.pair.c] {
			continue
		}
		if v.count < 3 {
			break
		}
		pairs = append(pairs, starPair{v.pair.i, v.pair.c})
		usedImg[v.pair.i] = true
		usedCat[v.pair.c] = true
	}
	return pairs
}

// fitSimilarityTransform computes the best-fit similarity transform mapping
// image star positions to catalog star positions using matched pairs.
// Uses the closed-form solution (Umeyama's method simplified for similarity).
func fitSimilarityTransform(imgStars, catStars []Star, pairs []starPair) (similarityTransform, bool) {
	n := len(pairs)
	if n < 2 {
		return similarityTransform{}, false
	}

	// Compute centroids
	var imgCX, imgCY, catCX, catCY float64
	for _, p := range pairs {
		imgCX += imgStars[p.imgIdx].X
		imgCY += imgStars[p.imgIdx].Y
		catCX += catStars[p.catIdx].X
		catCY += catStars[p.catIdx].Y
	}
	imgCX /= float64(n)
	imgCY /= float64(n)
	catCX /= float64(n)
	catCY /= float64(n)

	// Compute cross-covariance and variance
	var sxx, sxy, syx, syy, varImg float64
	for _, p := range pairs {
		ix := imgStars[p.imgIdx].X - imgCX
		iy := imgStars[p.imgIdx].Y - imgCY
		cx := catStars[p.catIdx].X - catCX
		cy := catStars[p.catIdx].Y - catCY

		sxx += ix * cx
		sxy += ix * cy
		syx += iy * cx
		syy += iy * cy
		varImg += ix*ix + iy*iy
	}

	if varImg < 1e-12 {
		return similarityTransform{}, false
	}

	// For a similarity transform, the rotation angle is:
	// theta = atan2(sxy - syx, sxx + syy)
	// and scale = sqrt((sxx+syy)^2 + (sxy-syx)^2) / varImg
	a := sxx + syy
	b := sxy - syx
	mag := math.Sqrt(a*a + b*b)
	if mag < 1e-12 {
		return similarityTransform{}, false
	}

	cosTheta := a / mag
	sinTheta := b / mag
	scale := mag / varImg

	tx := catCX - scale*(cosTheta*imgCX-sinTheta*imgCY)
	ty := catCY - scale*(sinTheta*imgCX+cosTheta*imgCY)

	return similarityTransform{
		cos:   cosTheta,
		sin:   sinTheta,
		scale: scale,
		tx:    tx,
		ty:    ty,
	}, true
}

// countInliersWithResidual counts inliers and returns mean residual distance.
func countInliersWithResidual(imgStars, catStars []Star, tx similarityTransform, threshold float64) (int, float64) {
	inliers := 0
	totalResidual := 0.0
	for _, is := range imgStars {
		px, py := tx.apply(is.X, is.Y)
		bestDist := threshold + 1
		for _, cs := range catStars {
			dx := px - cs.X
			dy := py - cs.Y
			d := math.Sqrt(dx*dx + dy*dy)
			if d < bestDist {
				bestDist = d
			}
		}
		if bestDist <= threshold {
			inliers++
			totalResidual += bestDist
		}
	}
	meanResidual := 0.0
	if inliers > 0 {
		meanResidual = totalResidual / float64(inliers)
	}
	return inliers, meanResidual
}

// refineSolveCenter computes the actual RA/Dec of the image center by
// mapping the image center through the fitted transform back to the
// gnomonic projection, then inverting to sky coordinates.
func refineSolveCenter(
	tx similarityTransform,
	imgCenterX, imgCenterY, imgSpan float64,
	pCenterX, pCenterY, pSpan float64,
	tileRA, tileDec, fovDeg float64,
) (ra, dec float64) {
	// The transform maps normalized image coords to normalized catalog coords.
	// Image center in normalized coords is (0, 0).
	catNormX, catNormY := tx.apply(0, 0)

	// Convert back to projected pixel coords
	catPx := catNormX*pSpan + pCenterX
	catPy := catNormY*pSpan + pCenterY

	// Inverse gnomonic projection: pixel → RA/Dec
	projSize := 1000.0
	fovRad := fovDeg * math.Pi / 180
	scale := projSize / fovRad

	// Tangent plane coordinates
	x := (catPx - projSize/2) / scale
	y := (projSize/2 - catPy) / scale

	ra0 := tileRA * math.Pi / 180
	dec0 := tileDec * math.Pi / 180
	sinDec0 := math.Sin(dec0)
	cosDec0 := math.Cos(dec0)

	rho := math.Sqrt(x*x + y*y)
	if rho < 1e-12 {
		return tileRA, tileDec
	}
	c := math.Atan(rho)
	sinC := math.Sin(c)
	cosC := math.Cos(c)

	decR := math.Asin(cosC*sinDec0 + y*sinC*cosDec0/rho)
	raR := ra0 + math.Atan2(x*sinC, rho*cosDec0*cosC-y*sinDec0*sinC)

	ra = raR * 180 / math.Pi
	dec = decR * 180 / math.Pi
	for ra < 0 {
		ra += 360
	}
	for ra >= 360 {
		ra -= 360
	}
	return ra, dec
}

// projectStars converts catalog RA/Dec to virtual pixel positions using a
// gnomonic (tangent plane) projection centered on (ra0, dec0) with the
// given FOV mapped to imgSize pixels.
func projectStars(catalog []CatalogStar, ra0, dec0, fovDeg float64, imgSize float64) []Star {
	ra0r := ra0 * math.Pi / 180
	dec0r := dec0 * math.Pi / 180
	sinDec0 := math.Sin(dec0r)
	cosDec0 := math.Cos(dec0r)
	scale := imgSize / (fovDeg * math.Pi / 180)

	var stars []Star
	for _, cs := range catalog {
		rar := cs.RA * math.Pi / 180
		decr := cs.Dec * math.Pi / 180
		sinDec := math.Sin(decr)
		cosDec := math.Cos(decr)
		dra := rar - ra0r

		cosC := sinDec0*sinDec + cosDec0*cosDec*math.Cos(dra)
		if cosC < 0.1 {
			continue
		}
		x := (cosDec * math.Sin(dra)) / cosC
		y := (cosDec0*sinDec - sinDec0*cosDec*math.Cos(dra)) / cosC

		px := imgSize/2 + x*scale
		py := imgSize/2 - y*scale

		if px >= 0 && px < imgSize && py >= 0 && py < imgSize {
			stars = append(stars, Star{
				X:          px,
				Y:          py,
				Brightness: 1000 - cs.Magnitude*100,
			})
		}
	}

	sort.Slice(stars, func(i, j int) bool {
		return stars[i].Brightness > stars[j].Brightness
	})

	return stars
}
