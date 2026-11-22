package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"testing"

	_ "modernc.org/sqlite"
)

func BenchmarkSpatialQuery(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		for _, method := range []string{"RTree", "FullScan"} {
			b.Run(fmt.Sprintf("%s/%d", method, size), func(b *testing.B) {
				db := benchmarkDB(b, size)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if method == "RTree" {
						_, err := nearby(context.Background(), db, 35.7, 51.4, 2500, "mosque")
						if err != nil {
							b.Fatal(err)
						}
					} else {
						benchmarkFullScan(b, db, 35.7, 51.4, 2500)
					}
				}
			})
		}
	}
}

func benchmarkDB(b *testing.B, count int) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO places(source,source_id,category,latitude,longitude,name) VALUES('bench',?,'mosque',?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		// Deterministic global points create both nearby and distant candidates.
		lat := math.Mod(float64(i)*0.61803398875+35.7+90, 180) - 90
		lon := math.Mod(float64(i)*0.754877666+51.4+180, 360) - 180
		if _, err = stmt.Exec(fmt.Sprintf("%d", i), lat, lon, "fixture"); err != nil {
			b.Fatal(err)
		}
	}
	if err = stmt.Close(); err != nil {
		b.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	return db
}

func benchmarkFullScan(b *testing.B, db *sql.DB, lat, lon, radius float64) {
	rows, err := db.Query(`SELECT latitude,longitude FROM places WHERE category='mosque'`)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var pLat, pLon float64
		if err = rows.Scan(&pLat, &pLon); err != nil {
			b.Fatal(err)
		}
		_ = distanceKM(lat, lon, pLat, pLon)*1000 <= radius
	}
	if err = rows.Err(); err != nil {
		b.Fatal(err)
	}
}
