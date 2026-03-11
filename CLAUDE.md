# PhotonForge

**Created:** 2026-03-10 | **Last updated:** 2026-03-11

High-performance Go-based astronomical live stacker. Cross-platform (macOS, Windows, Linux).

## Stack
- **Language:** Pure Go (no CGO for core logic)
- **Image Processing:** Custom star centroid detection + triangle pattern matching
- **Webcam Capture:** ffmpeg pipe (avfoundation/dshow/v4l2 per OS)
- **UI:** Fyne v2
- **Database:** SQLite via go-sqlite3
- **Hardware:** Any webcam (built-in, USB, modified for prime focus)

## Project Structure
```
main.go              — Entry point, CLI flags, wiring
provider/
  provider.go        — ImageProvider interface (returns *image.NRGBA)
  webcam.go          — WebcamProvider (ffmpeg pipe, cross-platform)
  directory.go       — DirectoryProvider (test images with jitter simulation)
engine/
  floatimg.go        — FloatImage type for 64-bit accumulation + stretch
  stars.go           — Star centroid detection (threshold → blob → centroid)
  align.go           — Triangle pattern matching, affine transform, bilinear warp
  stacker.go         — Accumulator with goroutine worker
db/
  db.go              — SQLite store, schema, search/insert queries, session history
  ingest.go          — CSV parsers for HYG (stars) and OpenNGC (DSOs)
ui/
  window.go          — Fyne window, stretch sliders, live preview
tools/
  gen_test_stars.go  — Synthetic starfield generator
data/                — Catalog CSVs (HYG, OpenNGC)
sim/                 — Test images directory
```

## Running
```bash
# Live webcam mode (default)
go run .

# Test mode with simulated images
go run . -mode=test -dir=sim

# List available cameras
go run . -list

# Custom resolution/fps
go run . -width=1280 -height=720 -fps=30

# Debug mode (show detected stars + match lines)
go run . -mode=test -dir=sim -debug
```

## Dependencies
- **Runtime:** ffmpeg (for webcam capture)
- **Go modules:** Fyne v2, go-sqlite3, golang.org/x/image
- No OpenCV, no GoCV, no CGO for image processing

## Development Phases
1. **Foundation** — ImageProvider interface, webcam/directory providers, Fyne display ✅
2. **Alignment** — Star centroid detection, triangle matching, affine warp ✅
3. **Stacking & Stretch** — Float64 accumulator, histogram stretch (Black/Gamma/White) ✅
4. **Database** — SQLite catalog (HYG stars + OpenNGC DSOs), session history ✅

## Cross-Platform Notes
- **macOS:** ffmpeg via `brew install ffmpeg`, uses avfoundation
- **Windows:** ffmpeg in PATH, uses dshow
- **Linux:** ffmpeg via package manager, uses v4l2

## Conventions
- Default branch: `master`
- Red-on-black UI theme for night vision preservation (planned)
- Goroutine worker for CPU-heavy alignment
