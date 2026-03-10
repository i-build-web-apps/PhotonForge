package db

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// IngestHYG parses the HYG v4.2 star catalog CSV and inserts objects into the database.
// Expected columns: proper (name), ra (decimal hours), dec (decimal degrees), mag.
func (s *Store) IngestHYG(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("ingest HYG: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true

	headers, err := reader.Read()
	if err != nil {
		return 0, fmt.Errorf("ingest HYG: reading headers: %w", err)
	}

	colIdx := indexColumns(headers)

	// Required columns.
	raIdx, ok := colIdx["ra"]
	if !ok {
		return 0, fmt.Errorf("ingest HYG: missing 'ra' column")
	}
	decIdx, ok := colIdx["dec"]
	if !ok {
		return 0, fmt.Errorf("ingest HYG: missing 'dec' column")
	}

	// Optional columns.
	properIdx := colIdx["proper"]  // -1 if missing
	magIdx := colIdx["mag"]        // -1 if missing
	hipIdx := colIdx["hip"]        // Hipparcos ID for catalog_id
	hdIdx := colIdx["hd"]          // Henry Draper as fallback

	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("ingest HYG: reading records: %w", err)
	}

	batch := make([]CelestialObject, 0, len(records))
	for _, row := range records {
		raHours, err := strconv.ParseFloat(strings.TrimSpace(row[raIdx]), 64)
		if err != nil {
			continue
		}
		dec, err := strconv.ParseFloat(strings.TrimSpace(row[decIdx]), 64)
		if err != nil {
			continue
		}

		raDeg := raHours * 15.0 // Convert hours → degrees.

		obj := CelestialObject{
			Type:  "Star",
			RADeg: raDeg,
			DecDeg: dec,
		}

		// Name: prefer proper name, fall back to catalog IDs.
		if properIdx >= 0 && strings.TrimSpace(row[properIdx]) != "" {
			obj.Name = strings.TrimSpace(row[properIdx])
		}

		// Catalog ID: prefer HIP, then HD.
		if hipIdx >= 0 && strings.TrimSpace(row[hipIdx]) != "" {
			obj.CatalogID = "HIP " + strings.TrimSpace(row[hipIdx])
		} else if hdIdx >= 0 && strings.TrimSpace(row[hdIdx]) != "" {
			obj.CatalogID = "HD " + strings.TrimSpace(row[hdIdx])
		} else {
			// Generate a synthetic ID from coordinates to avoid UNIQUE conflicts.
			obj.CatalogID = fmt.Sprintf("HYG_%.4f_%+.4f", raDeg, dec)
		}

		if magIdx >= 0 {
			if m, err := strconv.ParseFloat(strings.TrimSpace(row[magIdx]), 64); err == nil {
				obj.Magnitude = m
			}
		}

		batch = append(batch, obj)
	}

	fmt.Printf("HYG: parsed %d stars, inserting...\n", len(batch))
	return s.BulkInsert(batch)
}

// IngestOpenNGC parses the OpenNGC CSV and inserts deep-sky objects.
// RA format: "HH:MM:SS.SS", Dec format: "+/-DD:MM:SS.SS".
func (s *Store) IngestOpenNGC(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("ingest OpenNGC: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.Comma = ';' // OpenNGC uses semicolons.
	reader.LazyQuotes = true

	headers, err := reader.Read()
	if err != nil {
		return 0, fmt.Errorf("ingest OpenNGC: reading headers: %w", err)
	}

	colIdx := indexColumns(headers)

	nameIdx, ok := colIdx["Name"]
	if !ok {
		// Try lowercase.
		nameIdx, ok = colIdx["name"]
		if !ok {
			return 0, fmt.Errorf("ingest OpenNGC: missing 'Name' column")
		}
	}
	raIdx, ok := colIdx["RA"]
	if !ok {
		raIdx, ok = colIdx["ra"]
		if !ok {
			return 0, fmt.Errorf("ingest OpenNGC: missing 'RA' column")
		}
	}
	decIdx, ok := colIdx["Dec"]
	if !ok {
		decIdx, ok = colIdx["dec"]
		if !ok {
			return 0, fmt.Errorf("ingest OpenNGC: missing 'Dec' column")
		}
	}

	typeIdx := lookupCol(colIdx, "Type", "type")
	vmagIdx := lookupCol(colIdx, "V-Mag", "v-mag")
	majAxIdx := lookupCol(colIdx, "MajAx", "majax")
	commonIdx := lookupCol(colIdx, "Common names", "common names")
	messierIdx := lookupCol(colIdx, "M", "m")

	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("ingest OpenNGC: reading records: %w", err)
	}

	batch := make([]CelestialObject, 0, len(records))
	for _, row := range records {
		raStr := strings.TrimSpace(row[raIdx])
		decStr := strings.TrimSpace(row[decIdx])
		if raStr == "" || decStr == "" {
			continue
		}

		raDeg, err := parseSexagesimalRA(raStr)
		if err != nil {
			continue
		}
		decDeg, err := parseSexagesimalDec(decStr)
		if err != nil {
			continue
		}

		catalogID := strings.TrimSpace(row[nameIdx])

		obj := CelestialObject{
			CatalogID: catalogID,
			RADeg:     raDeg,
			DecDeg:    decDeg,
		}

		// Use Messier designation as the display name if available, else common name.
		if messierIdx >= 0 && strings.TrimSpace(row[messierIdx]) != "" {
			obj.Name = "M" + strings.TrimSpace(row[messierIdx])
		} else if commonIdx >= 0 && strings.TrimSpace(row[commonIdx]) != "" {
			// Take first common name if multiple are listed.
			names := strings.Split(row[commonIdx], ",")
			obj.Name = strings.TrimSpace(names[0])
		} else {
			obj.Name = catalogID
		}

		if typeIdx >= 0 {
			obj.Type = strings.TrimSpace(row[typeIdx])
		}
		if vmagIdx >= 0 {
			if m, err := strconv.ParseFloat(strings.TrimSpace(row[vmagIdx]), 64); err == nil {
				obj.Magnitude = m
			}
		}
		if majAxIdx >= 0 {
			if sz, err := strconv.ParseFloat(strings.TrimSpace(row[majAxIdx]), 64); err == nil {
				obj.SizeArcMin = sz
			}
		}

		batch = append(batch, obj)
	}

	fmt.Printf("OpenNGC: parsed %d objects, inserting...\n", len(batch))
	return s.BulkInsert(batch)
}

// parseSexagesimalRA converts "HH:MM:SS.SS" → decimal degrees.
func parseSexagesimalRA(s string) (float64, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid RA format: %q", s)
	}
	h, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, err
	}
	m, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, err
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil {
		return 0, err
	}
	// RA in hours → degrees: (h + m/60 + s/3600) * 15
	return (h + m/60.0 + sec/3600.0) * 15.0, nil
}

// parseSexagesimalDec converts "+/-DD:MM:SS.SS" → decimal degrees.
func parseSexagesimalDec(s string) (float64, error) {
	s = strings.TrimSpace(s)
	sign := 1.0
	if strings.HasPrefix(s, "-") {
		sign = -1.0
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}

	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid Dec format: %q", s)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, err
	}
	m, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, err
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil {
		return 0, err
	}
	return sign * (d + m/60.0 + sec/3600.0), nil
}

// indexColumns builds a map from column name → index.
func indexColumns(headers []string) map[string]int {
	m := make(map[string]int, len(headers))
	for i, h := range headers {
		m[strings.TrimSpace(h)] = i
	}
	return m
}

// lookupCol tries multiple case variants for a column name. Returns -1 if not found.
func lookupCol(idx map[string]int, names ...string) int {
	for _, n := range names {
		if i, ok := idx[n]; ok {
			return i
		}
	}
	return -1
}
