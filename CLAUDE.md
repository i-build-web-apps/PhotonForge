# PhotonForge

**Created:** 2026-03-10 | **Last updated:** 2026-03-10

High-performance Go-based astronomical live stacker for macOS.

## Stack
- **Language:** Go 1.25
- **Vision:** GoCV (OpenCV bindings)
- **UI:** Fyne v2
- **Database:** SQLite (planned — Phase 4)
- **Hardware:** Logitech C310 (modified for prime focus)

## Project Structure
```
main.go              — Entry point, CLI flags, wiring
provider/
  provider.go        — ImageProvider interface
  webcam.go          — WebcamProvider (live camera via GoCV)
  directory.go       — DirectoryProvider (test images with jitter simulation)
engine/
  align.go           — ORB feature matching, homography, frame warping
  stacker.go         — 32-bit float accumulator, histogram stretch, goroutine worker
db/
  db.go              — SQLite store, schema, search/insert queries, session history
  ingest.go          — CSV parsers for HYG (stars) and OpenNGC (DSOs)
ui/
  window.go          — Fyne window, stretch sliders, Mat→Image conversion
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

# Disable jitter simulation
go run . -mode=test -dir=sim -jitter=false
```

## Dependencies
- OpenCV 4.x via Homebrew: `brew install opencv`
- GoCV: `gocv.io/x/gocv`
- Fyne: `fyne.io/fyne/v2`

## Development Phases
1. **Foundation** — ImageProvider interface, webcam/directory providers, Fyne display ✅
2. **Alignment** — ORB feature matching, homography warp, debug overlay ✅
3. **Stacking & Stretch** — 32-bit accumulator, histogram stretch sliders (Black/Gamma/White) ✅
4. **Database** — SQLite catalog (HYG stars + OpenNGC DSOs), session history ✅

## Conventions
- Default branch: `master`
- Red-on-black UI theme for night vision preservation
- Goroutines for CPU-heavy alignment work
