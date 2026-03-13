// +build ignore

// gen_catalog_db generates the pre-populated catalog database file.
// Run: go run tools/gen_catalog_db.go
package main

import (
	"fmt"
	"os"

	"github.com/paul/photonforge/db"
)

func main() {
	path := "data/photonforge.db"

	// Remove old file to start fresh.
	os.Remove(path)
	os.MkdirAll("data", 0755)

	store, err := db.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	n, err := store.SeedBuiltinCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
	}

	total, _ := store.ObjectCount()
	fmt.Printf("Generated %s: %d objects inserted (%d total in catalog)\n", path, n, total)

	// Print summary by category.
	for _, cat := range []string{"Messier", "Star", "Caldwell", "Double Star", "Variable Star", "NGC", "IC", "Solar System"} {
		results, _ := store.SearchByCategory(cat, 500)
		fmt.Printf("  %-15s %d objects\n", cat+":", len(results))
	}

	// Print summary by difficulty.
	fmt.Println()
	for _, diff := range []string{"Naked Eye", "Binoculars", "Small Telescope", "Advanced"} {
		results, _ := store.SearchByDifficulty(diff, 500)
		fmt.Printf("  %-20s %d objects\n", diff+":", len(results))
	}
}
