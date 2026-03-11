# PhotonForge

![PhotonForge Logo](PhotonForge_Logo.jpg)

**High-performance astronomical live stacker — pure Go, cross-platform.**

PhotonForge captures frames from any webcam, aligns them using star centroid detection and triangle pattern matching, and accumulates them into a high-precision float stack — pulling faint deep-sky detail out of noisy single exposures in real time.

## Features

- **Live Stacking** — 64-bit float accumulator with additive summation
- **Star Centroid Alignment** — Adaptive threshold, blob detection, triangle pattern matching, affine warp with bilinear interpolation
- **Histogram Stretch** — Black level, gamma, and white point sliders for real-time visibility
- **Simulator Mode** — Stream test images with synthetic jitter for daytime development
- **Star Catalog** — SQLite database with HYG (stars) and OpenNGC (DSOs) import
- **Session History** — Log every stacking session with metadata
- **Debug Overlay** — Visualize detected stars and match lines
- **Cross-Platform** — macOS, Windows, and Linux

## Stack

- **Pure Go** with goroutine-based alignment worker
- **Fyne v2** for native cross-platform UI
- **SQLite** for object catalog and session storage
- **ffmpeg** for webcam capture (avfoundation / dshow / v4l2)

## Quick Start

```bash
# Prerequisites: Go 1.21+ and ffmpeg
brew install ffmpeg    # macOS
# apt install ffmpeg   # Linux
# choco install ffmpeg # Windows

# Run with webcam
go run .

# Run with test images
go run tools/gen_test_stars.go   # generate synthetic starfields
go run . -mode=test -dir=sim

# List available cameras
go run . -list

# Import star catalogs
go run . -mode=ingest -hyg=data/hygdata_v42.csv -ngc=data/NGC.csv

# Search the catalog
go run . -search="Orion"

# Debug mode (show star detection + alignment)
go run . -mode=test -dir=sim -debug
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-mode` | `webcam` | Input mode: `webcam`, `test`, or `ingest` |
| `-device` | `0` | Camera device ID (name on Windows) |
| `-width` | `640` | Capture width |
| `-height` | `480` | Capture height |
| `-fps` | `30` | Capture framerate |
| `-dir` | `sim` | Test image directory |
| `-jitter` | `true` | Simulate tracking drift in test mode |
| `-debug` | `false` | Show star detection overlay |
| `-list` | `false` | List available cameras and exit |
| `-db` | `photonforge.db` | SQLite database path |
| `-hyg` | | HYG star catalog CSV path |
| `-ngc` | | OpenNGC deep-sky catalog CSV path |
| `-search` | | Search catalog by name and exit |

## License

MIT
