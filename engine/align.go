package engine

import (
	"fmt"
	"image"
	"image/color"
	"sync"

	"gocv.io/x/gocv"
)

// Aligner performs ORB-based feature matching to register each incoming frame
// against a reference frame. The heavy lifting runs in a goroutine worker pool
// so the capture loop stays at 30fps.
type Aligner struct {
	orb          gocv.ORB
	matcher      gocv.BFMatcher
	refKeypoints []gocv.KeyPoint
	refDesc      gocv.Mat
	refSet       bool

	debug bool // when true, draw matched keypoints on output

	mu sync.Mutex
}

// NewAligner creates an ORB aligner. Set debug=true to overlay keypoints.
func NewAligner(debug bool) *Aligner {
	return &Aligner{
		orb:     gocv.NewORB(),
		matcher: gocv.NewBFMatcherWithParams(gocv.NormHamming, false),
		debug:   debug,
	}
}

// Close releases the ORB detector and matcher resources.
func (a *Aligner) Close() {
	a.orb.Close()
	a.matcher.Close()
	if a.refSet {
		a.refDesc.Close()
	}
}

// Reset clears the reference frame so the next frame becomes the new reference.
func (a *Aligner) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refSet {
		a.refDesc.Close()
	}
	a.refSet = false
	a.refKeypoints = nil
}

// Align warps the input frame to match the reference frame. The first frame
// received after creation or Reset() becomes the reference. Returns the
// aligned frame (caller must Close it) and the number of matched features.
func (a *Aligner) Align(frame gocv.Mat) (aligned gocv.Mat, matches int, err error) {
	gray := gocv.NewMat()
	defer gray.Close()
	if frame.Channels() > 1 {
		gocv.CvtColor(frame, &gray, gocv.ColorBGRToGray)
	} else {
		frame.CopyTo(&gray)
	}

	kp, desc := a.orb.DetectAndCompute(gray, gocv.NewMat())
	defer desc.Close()

	a.mu.Lock()
	defer a.mu.Unlock()

	// First frame becomes the reference.
	if !a.refSet {
		a.refKeypoints = kp
		a.refDesc = gocv.NewMat()
		desc.CopyTo(&a.refDesc)
		a.refSet = true

		result := gocv.NewMat()
		frame.CopyTo(&result)
		if a.debug {
			a.drawKeypoints(&result, kp, nil)
		}
		return result, len(kp), nil
	}

	// Match current frame descriptors against reference.
	if desc.Empty() || a.refDesc.Empty() {
		result := gocv.NewMat()
		frame.CopyTo(&result)
		return result, 0, nil
	}

	rawMatches := a.matcher.KnnMatch(desc, a.refDesc, 2)

	// Lowe's ratio test — keep only good matches.
	var goodSrc, goodDst []gocv.KeyPoint
	for _, m := range rawMatches {
		if len(m) < 2 {
			continue
		}
		if m[0].Distance < 0.75*m[1].Distance {
			goodSrc = append(goodSrc, kp[m[0].QueryIdx])
			goodDst = append(goodDst, a.refKeypoints[m[0].TrainIdx])
		}
	}

	matches = len(goodSrc)

	// Need at least 4 points for homography.
	if matches < 4 {
		result := gocv.NewMat()
		frame.CopyTo(&result)
		if a.debug {
			a.drawKeypoints(&result, kp, nil)
		}
		return result, matches, fmt.Errorf("insufficient matches: %d (need 4+)", matches)
	}

	// Build point vectors for FindHomography.
	srcPts := gocv.NewMatWithSize(matches, 1, gocv.MatTypeCV64FC2)
	dstPts := gocv.NewMatWithSize(matches, 1, gocv.MatTypeCV64FC2)
	defer srcPts.Close()
	defer dstPts.Close()

	for i := 0; i < matches; i++ {
		srcPts.SetDoubleAt(i, 0, float64(goodSrc[i].X))
		srcPts.SetDoubleAt(i, 1, float64(goodSrc[i].Y))
		dstPts.SetDoubleAt(i, 0, float64(goodDst[i].X))
		dstPts.SetDoubleAt(i, 1, float64(goodDst[i].Y))
	}

	// RANSAC homography to reject outliers.
	H := gocv.FindHomography(srcPts, &dstPts, gocv.HomograpyMethodRANSAC, 3.0)
	defer H.Close()

	if H.Empty() {
		result := gocv.NewMat()
		frame.CopyTo(&result)
		return result, matches, fmt.Errorf("homography computation failed")
	}

	// Warp the frame to align with the reference.
	result := gocv.NewMat()
	gocv.WarpPerspective(frame, &result, H, frame.Size())

	if a.debug {
		a.drawKeypoints(&result, goodSrc, goodDst)
	}

	return result, matches, nil
}

// drawKeypoints overlays detected features on the image.
// Green circles = matched source keypoints, red circles = matched destination.
func (a *Aligner) drawKeypoints(img *gocv.Mat, src []gocv.KeyPoint, dst []gocv.KeyPoint) {
	green := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}

	for _, kp := range src {
		pt := image.Pt(int(kp.X), int(kp.Y))
		gocv.Circle(img, pt, 4, green, 1)
	}
	for _, kp := range dst {
		pt := image.Pt(int(kp.X), int(kp.Y))
		gocv.Circle(img, pt, 3, red, 1)
	}
}
