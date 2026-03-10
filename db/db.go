package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// CelestialObject represents a star or deep-sky object from the catalog.
type CelestialObject struct {
	ID         int     `db:"id"`
	Name       string  `db:"name"`
	CatalogID  string  `db:"catalog_id"`
	Type       string  `db:"type"`
	RADeg      float64 `db:"ra_deg"`
	DecDeg     float64 `db:"dec_deg"`
	Magnitude  float64 `db:"magnitude"`
	SizeArcMin float64 `db:"size_arcmin"`
}

// CaptureSession records metadata for a completed stacking session.
type CaptureSession struct {
	ID         int
	StartedAt  string
	EndedAt    string
	FrameCount int
	ObjectName string
	RADeg      float64
	DecDeg     float64
	Notes      string
}

// Store wraps the SQLite connection and provides query methods.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database and ensures the schema exists.
func Open(path string) (*Store, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	s := &Store{db: conn}
	if err := s.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// Close shuts down the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS celestial_objects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		catalog_id TEXT UNIQUE,
		type TEXT,
		ra_deg REAL NOT NULL,
		dec_deg REAL NOT NULL,
		magnitude REAL,
		size_arcmin REAL
	);
	CREATE INDEX IF NOT EXISTS idx_coords ON celestial_objects(ra_deg, dec_deg);
	CREATE INDEX IF NOT EXISTS idx_name ON celestial_objects(name);
	CREATE INDEX IF NOT EXISTS idx_catalog ON celestial_objects(catalog_id);

	CREATE TABLE IF NOT EXISTS capture_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		ended_at TEXT,
		frame_count INTEGER DEFAULT 0,
		object_name TEXT,
		ra_deg REAL,
		dec_deg REAL,
		notes TEXT
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// SearchByName finds objects whose name or catalog_id matches the query (case-insensitive).
func (s *Store) SearchByName(query string, limit int) ([]CelestialObject, error) {
	if limit <= 0 {
		limit = 20
	}
	q := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT id, COALESCE(name,''), COALESCE(catalog_id,''), COALESCE(type,''),
		       ra_deg, dec_deg, COALESCE(magnitude,99), COALESCE(size_arcmin,0)
		FROM celestial_objects
		WHERE name LIKE ? OR catalog_id LIKE ?
		ORDER BY magnitude ASC
		LIMIT ?
	`, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObjects(rows)
}

// SearchByCoords finds objects within a rectangular region of sky.
func (s *Store) SearchByCoords(raMin, raMax, decMin, decMax float64, limit int) ([]CelestialObject, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT id, COALESCE(name,''), COALESCE(catalog_id,''), COALESCE(type,''),
		       ra_deg, dec_deg, COALESCE(magnitude,99), COALESCE(size_arcmin,0)
		FROM celestial_objects
		WHERE ra_deg BETWEEN ? AND ? AND dec_deg BETWEEN ? AND ?
		ORDER BY magnitude ASC
		LIMIT ?
	`, raMin, raMax, decMin, decMax, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObjects(rows)
}

// ObjectCount returns the total number of objects in the catalog.
func (s *Store) ObjectCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM celestial_objects").Scan(&count)
	return count, err
}

// InsertObject adds a single object to the catalog.
func (s *Store) InsertObject(obj CelestialObject) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO celestial_objects (name, catalog_id, type, ra_deg, dec_deg, magnitude, size_arcmin)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, obj.Name, obj.CatalogID, obj.Type, obj.RADeg, obj.DecDeg, obj.Magnitude, obj.SizeArcMin)
	return err
}

// BulkInsert efficiently inserts many objects in a single transaction.
func (s *Store) BulkInsert(objects []CelestialObject) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO celestial_objects (name, catalog_id, type, ra_deg, dec_deg, magnitude, size_arcmin)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, obj := range objects {
		res, err := stmt.Exec(obj.Name, obj.CatalogID, obj.Type, obj.RADeg, obj.DecDeg, obj.Magnitude, obj.SizeArcMin)
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// SaveSession records a completed capture session.
func (s *Store) SaveSession(sess CaptureSession) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO capture_sessions (started_at, ended_at, frame_count, object_name, ra_deg, dec_deg, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, sess.StartedAt, sess.EndedAt, sess.FrameCount, sess.ObjectName, sess.RADeg, sess.DecDeg, sess.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecentSessions returns the last N capture sessions.
func (s *Store) RecentSessions(limit int) ([]CaptureSession, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`
		SELECT id, started_at, COALESCE(ended_at,''), frame_count,
		       COALESCE(object_name,''), COALESCE(ra_deg,0), COALESCE(dec_deg,0), COALESCE(notes,'')
		FROM capture_sessions
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []CaptureSession
	for rows.Next() {
		var sess CaptureSession
		if err := rows.Scan(&sess.ID, &sess.StartedAt, &sess.EndedAt, &sess.FrameCount,
			&sess.ObjectName, &sess.RADeg, &sess.DecDeg, &sess.Notes); err != nil {
			continue
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

func scanObjects(rows *sql.Rows) ([]CelestialObject, error) {
	var objects []CelestialObject
	for rows.Next() {
		var obj CelestialObject
		if err := rows.Scan(&obj.ID, &obj.Name, &obj.CatalogID, &obj.Type,
			&obj.RADeg, &obj.DecDeg, &obj.Magnitude, &obj.SizeArcMin); err != nil {
			continue
		}
		objects = append(objects, obj)
	}
	return objects, nil
}
