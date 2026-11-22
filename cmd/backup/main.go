package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	source := flag.String("database", "./data/mapine.db", "live SQLite database")
	destination := flag.String("destination", "", "new consistent snapshot path")
	flag.Parse()
	if *destination == "" {
		fmt.Fprintln(os.Stderr, "--destination is required")
		os.Exit(2)
	}
	if _, err := os.Stat(*destination); err == nil {
		fmt.Fprintln(os.Stderr, "destination already exists")
		os.Exit(2)
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*destination), 0750); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// VACUUM INTO takes a transactionally consistent snapshot, including WAL
	// content, without copying the live DB/WAL files independently.
	if err := backup(*source, *destination); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("consistent SQLite backup written to %s\n", *destination)
}

func backup(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat source database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source database is not a regular file")
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return err
	}
	quoted := "'" + strings.ReplaceAll(destination, "'", "''") + "'"
	if _, err = db.Exec("VACUUM INTO " + quoted); err != nil {
		return err
	}
	if err = os.Chmod(destination, 0600); err != nil {
		return fmt.Errorf("restrict backup file permissions: %w", err)
	}
	return nil
}
