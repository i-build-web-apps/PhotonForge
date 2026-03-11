package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/paul/photonforge/db"
	"github.com/paul/photonforge/provider"
	"github.com/paul/photonforge/ui"
)

func main() {
	mode := flag.String("mode", "webcam", "Input mode: 'webcam', 'test', or 'ingest'")
	device := flag.String("device", "0", "Camera device ID (0 on Mac/Linux, device name on Windows)")
	width := flag.Int("width", 640, "Capture width")
	height := flag.Int("height", 480, "Capture height")
	fps := flag.Int("fps", 30, "Capture framerate")
	dir := flag.String("dir", "sim", "Directory of test images (used with -mode=test)")
	jitter := flag.Bool("jitter", true, "Add random pixel jitter in test mode")
	debug := flag.Bool("debug", false, "Show star detection overlay for alignment debugging")
	listCams := flag.Bool("list", false, "List available cameras and exit")
	dbPath := flag.String("db", "photonforge.db", "Path to SQLite database")
	hygCSV := flag.String("hyg", "", "Path to HYG v4.2 CSV (used with -mode=ingest)")
	ngcCSV := flag.String("ngc", "", "Path to OpenNGC CSV (used with -mode=ingest)")
	search := flag.String("search", "", "Search the catalog for an object by name, then exit")
	flag.Parse()

	// List cameras and exit.
	if *listCams {
		output, err := provider.ListDevices()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing devices: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(output)
		return
	}

	// Ingest mode.
	if *mode == "ingest" {
		runIngest(*dbPath, *hygCSV, *ngcCSV)
		return
	}

	// Search mode.
	if *search != "" {
		runSearch(*dbPath, *search)
		return
	}

	// Live/test mode.
	var prov provider.ImageProvider

	switch *mode {
	case "webcam":
		fmt.Printf("PhotonForge — Webcam mode (device %s, %dx%d@%dfps)\n", *device, *width, *height, *fps)
		prov = provider.NewWebcamProvider(*device, *width, *height, *fps)

	case "test":
		fmt.Println("PhotonForge — Test mode (dir:", *dir, ")")
		prov = provider.NewDirectoryProvider(*dir, *jitter, true)

	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s (use 'webcam', 'test', or 'ingest')\n", *mode)
		os.Exit(1)
	}

	win := ui.New(prov, *debug)
	win.Run()
}

func runIngest(dbPath, hygPath, ngcPath string) {
	store, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if hygPath == "" && ngcPath == "" {
		fmt.Fprintln(os.Stderr, "Ingest mode requires -hyg and/or -ngc CSV paths.")
		fmt.Fprintln(os.Stderr, "Example: go run . -mode=ingest -hyg=data/hygdata_v42.csv -ngc=data/NGC.csv")
		os.Exit(1)
	}

	if hygPath != "" {
		fmt.Printf("Importing HYG catalog from %s...\n", hygPath)
		n, err := store.IngestHYG(hygPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "HYG import failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("HYG: inserted %d stars.\n", n)
	}

	if ngcPath != "" {
		fmt.Printf("Importing OpenNGC catalog from %s...\n", ngcPath)
		n, err := store.IngestOpenNGC(ngcPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "OpenNGC import failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("OpenNGC: inserted %d objects.\n", n)
	}

	total, _ := store.ObjectCount()
	fmt.Printf("Database ready: %d total objects in catalog.\n", total)
}

func runSearch(dbPath, query string) {
	store, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	results, err := store.SearchByName(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Search failed: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Printf("No objects found matching %q\n", query)
		return
	}

	fmt.Printf("Results for %q:\n", query)
	for _, obj := range results {
		name := obj.Name
		if name == "" {
			name = obj.CatalogID
		}
		fmt.Printf("  %-20s  %-12s  RA: %7.3f°  Dec: %+7.3f°  Mag: %.1f  %s\n",
			name, obj.CatalogID, obj.RADeg, obj.DecDeg, obj.Magnitude, obj.Type)
	}
}
