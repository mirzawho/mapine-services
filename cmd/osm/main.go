package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mirzawho/mapine-services/internal/osm/downloader"
	"github.com/mirzawho/mapine-services/internal/osm/importer"
	"github.com/mirzawho/mapine-services/internal/osm/mapping"
	"github.com/mirzawho/mapine-services/internal/osm/progress"
)

const (
	defaultCategoriesPath = "configs/osm/categories.yaml"
	defaultCountriesPath  = "configs/osm/countries.yaml"
	defaultOSMDataDir     = "data/osm"
	defaultDatabasePath   = "bin/volume/data/mapine.db"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "osm:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printRootHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "download":
		return runDownload(args[1:])
	case "import":
		return runImport(args[1:])
	case "categories":
		return runCategories(args[1:])
	default:
		return fmt.Errorf("unknown command %q (run './bin/osm --help')", args[0])
	}
}

func runDownload(args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", env("OSM_DATA_DIR", defaultOSMDataDir), "directory for downloaded country snapshots")
	output := fs.String("output", "", "write the snapshot to this exact path")
	countriesPath := fs.String("countries", defaultCountriesPath, "country source registry YAML")
	timeout := fs.Duration("timeout", 4*time.Hour, "maximum total transfer duration")
	force := fs.Bool("force", false, "download even when an existing snapshot appears current")
	quiet := fs.Bool("quiet", false, "suppress live progress output")
	help := fs.Bool("help", false, "show download help")
	fs.Usage = func() { printDownloadHelp(fs.Output()) }
	countryArg, flagArgs := positionalAndFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *help {
		printDownloadHelp(os.Stdout)
		return nil
	}
	if countryArg == "" && fs.NArg() > 0 {
		countryArg = fs.Arg(0)
	}
	if countryArg == "" {
		return errors.New("download requires a country name (for example: download iran)")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args()[1:], " "))
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be a positive duration")
	}
	countries, err := downloader.LoadCountries(*countriesPath)
	if err != nil {
		return err
	}
	country, err := countries.Country(countryArg)
	if err != nil {
		return err
	}
	destination := *output
	if destination == "" {
		destination = filepath.Join(*dataDir, country.Filename)
	}
	if abs, err := filepath.Abs(destination); err == nil {
		destination = abs
	}

	fmt.Fprintf(os.Stderr, "OpenStreetMap snapshot download\nCountry: %s\nSource: %s\nFile: %s\n\n",
		title(countryArg), country.URL, destination)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	started := time.Now()
	printer := newDownloadPrinter(!*quiet, os.Stderr, progress.IsTerminal(os.Stderr), started)
	result, err := downloader.Download(ctx, downloader.Options{
		Country: countryArg, Definition: country, Destination: destination, Force: *force,
		Progress: printer.update, Timeout: *timeout,
	})
	printer.finish()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Download failed; the previous final snapshot was not replaced. A partial may be retained for a safe retry.")
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("download cancelled; any verified partial data is retained for a safe retry")
		}
		return err
	}
	if result.Skipped {
		if result.CurrentKnown && result.Current {
			fmt.Fprintf(os.Stderr, "Snapshot is current according to the remote validator (%s).\n", destination)
		} else {
			fmt.Fprintf(os.Stderr, "Existing snapshot kept at %s; remote metadata could not prove it is current. Use --force to fetch a fresh copy.\n", destination)
		}
		return nil
	}
	fmt.Fprintf(os.Stderr, "Download complete: %s (%s)", destination, progress.Bytes(result.Bytes))
	if result.ResumedFrom > 0 {
		fmt.Fprintf(os.Stderr, ", resumed from %s", progress.Bytes(result.ResumedFrom))
	}
	fmt.Fprintln(os.Stderr)
	if result.ChecksumVerified {
		fmt.Fprintln(os.Stderr, "Checksum: Geofabrik MD5 sidecar verified.")
	} else {
		fmt.Fprintln(os.Stderr, "Checksum: no authoritative sidecar available; PBF header and size were verified.")
	}
	return nil
}

func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	categoriesPath := fs.String("config", defaultCategoriesPath, "category mapping YAML")
	countriesPath := fs.String("countries", defaultCountriesPath, "country source registry YAML")
	countryName := fs.String("country", "iran", "source country snapshot")
	filePath := fs.String("file", "", "local OSM PBF file (default: data directory/country filename)")
	databasePath := fs.String("db", env("OSM_DB_PATH", defaultDatabasePath), "existing Mapine SQLite database")
	dataDir := fs.String("data-dir", env("OSM_DATA_DIR", defaultOSMDataDir), "directory containing country snapshots")
	batchSize := fs.Int("batch-size", 1000, "maximum OSM records per staging transaction")
	workers := fs.Int("workers", 0, "PBF decoder concurrency (default: up to 4 CPUs)")
	dryRun := fs.Bool("dry-run", false, "scan and validate without writing to SQLite or import history")
	quiet := fs.Bool("quiet", false, "suppress live progress output")
	verbose := fs.Bool("verbose", false, "print additional configuration details")
	help := fs.Bool("help", false, "show import help")
	fs.Usage = func() { printImportHelp(fs.Output()) }
	categoryArg, flagArgs := positionalAndFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *help {
		printImportHelp(os.Stdout)
		return nil
	}
	if categoryArg == "" && fs.NArg() > 0 {
		categoryArg = fs.Arg(0)
	}
	if categoryArg == "" {
		return errors.New("import requires a CLI category (for example: import public-restroom)")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args()[1:], " "))
	}
	if *batchSize < 1 || *batchSize > 100000 {
		return errors.New("--batch-size must be between 1 and 100000")
	}
	if *workers < 0 || *workers > 128 {
		return errors.New("--workers must be between 1 and 128 (or 0 for the default)")
	}
	config, err := mapping.Load(*categoriesPath)
	if err != nil {
		return err
	}
	category, err := config.Category(categoryArg)
	if err != nil {
		return err
	}
	countries, err := downloader.LoadCountries(*countriesPath)
	if err != nil {
		return err
	}
	country, err := countries.Country(*countryName)
	if err != nil {
		return err
	}
	input := *filePath
	if input == "" {
		input = filepath.Join(*dataDir, country.Filename)
	}
	if abs, err := filepath.Abs(input); err == nil {
		input = abs
	}
	if abs, err := filepath.Abs(*databasePath); err == nil {
		*databasePath = abs
	}
	if _, err := os.Stat(input); err != nil {
		return fmt.Errorf("PBF snapshot %q: %w (download it with './bin/osm download %s')", input, err, *countryName)
	}

	fmt.Fprintln(os.Stderr, "Mapine OSM Importer")
	fmt.Fprintf(os.Stderr, "Country:     %s\nCategory:    %s\nOSM Match:   %s\nStore As:    %s\n",
		title(*countryName), categoryArg, category.MatchDescription(), category.DatabaseCategory)
	if exclusions := category.ExcludeDescription(); exclusions != "" {
		fmt.Fprintf(os.Stderr, "Exclusions:  %s\n", exclusions)
	}
	fmt.Fprintf(os.Stderr, "Source:      %s\n", input)
	if *dryRun {
		fmt.Fprintln(os.Stderr, "Database:    (dry run; no database writes)")
	} else {
		fmt.Fprintf(os.Stderr, "Database:    %s\n", *databasePath)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "Mapping:     %s\n", *categoriesPath)
		fmt.Fprintf(os.Stderr, "Workers:     %s\n", workerDescription(*workers))
		fmt.Fprintf(os.Stderr, "Batch size:  %d\n", *batchSize)
	}
	if *dryRun {
		fmt.Fprintln(os.Stderr, "Mode:        dry run (one scan; no database or history writes)")
	} else {
		fmt.Fprintln(os.Stderr, "Counting:    exact node and category totals (PBF is read twice)")
	}
	fmt.Fprintln(os.Stderr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	started := time.Now()
	printer := newImportPrinter(!*quiet, os.Stderr, progress.IsTerminal(os.Stderr), started)
	stats, err := importer.Run(ctx, importer.Options{
		File: input, Database: *databasePath, Country: *countryName,
		Category: categoryArg, Mapping: category, BatchSize: *batchSize,
		Workers: *workers, DryRun: *dryRun, Progress: printer.update,
	})
	printer.finish()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nImport stopped: scanned=%s matched=%s rejected=%s. Production synchronization is atomic; previous complete rows remain on rollback.\n",
			comma(stats.TotalNodes), comma(stats.MatchingNodes), comma(stats.Rejected))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("import cancelled; previous complete POI data was preserved: %w", err)
		}
		return err
	}
	printImportSummary(os.Stderr, *countryName, categoryArg, category.DatabaseCategory, input,
		*databasePath, stats, time.Since(started), *dryRun)
	return nil
}

func runCategories(args []string) error {
	fs := flag.NewFlagSet("categories", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultCategoriesPath, "category mapping YAML")
	help := fs.Bool("help", false, "show categories help")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: ./bin/osm categories [--config %s]\n", defaultCategoriesPath)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *help {
		fs.Usage()
		return nil
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	cfg, err := mapping.Load(*configPath)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Categories))
	for name := range cfg.Categories {
		names = append(names, name)
	}
	sortStrings(names)
	fmt.Println("Available categories:")
	fmt.Println()
	fmt.Printf("%-20s %-24s %s\n", "NAME", "DATABASE CATEGORY", "OSM MATCH")
	for _, name := range names {
		category := cfg.Categories[name]
		fmt.Printf("%-20s %-24s %s\n", name, category.DatabaseCategory, category.MatchDescription())
	}
	return nil
}

type importPrinter struct {
	mu          sync.Mutex
	enabled     bool
	out         io.Writer
	interactive bool
	started     time.Time
	lastPrinted time.Time
	lastPhase   string
	lastWork    int64
	lastAt      time.Time
	rate        *progress.EMA
}

func newImportPrinter(enabled bool, out io.Writer, interactive bool, started time.Time) *importPrinter {
	return &importPrinter{enabled: enabled, out: out, interactive: interactive, started: started, rate: progress.NewEMA(0.25)}
}

func (p *importPrinter) update(value importer.Progress) {
	if !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	phaseChanged := p.lastPhase != value.Phase
	if phaseChanged {
		p.lastPhase, p.lastWork, p.lastAt = value.Phase, 0, now
		p.rate = progress.NewEMA(0.25)
	}
	work := value.NodesScanned
	if value.Phase == "Counting" || value.Phase == "Dry run" {
		work = value.BytesScanned
	}
	if phaseChanged {
		p.lastWork, p.lastAt = work, now
	} else if work > p.lastWork && !p.lastAt.IsZero() {
		seconds := now.Sub(p.lastAt).Seconds()
		if seconds > 0 {
			p.rate.Add(float64(work-p.lastWork) / seconds)
		}
		p.lastWork, p.lastAt = work, now
	}
	if phaseChanged || value.Done || now.Sub(p.lastPrinted) >= time.Second ||
		(value.NodesScanned == value.NodesTotal && value.TotalKnown) ||
		(value.BytesTotal > 0 && value.BytesScanned == value.BytesTotal) {
		p.render(value, now)
		p.lastPrinted = now
	}
}

func (p *importPrinter) render(value importer.Progress, now time.Time) {
	done, total, known := value.NodesScanned, value.NodesTotal, value.TotalKnown
	unit := "nodes"
	speed := p.rate.Value()
	if value.Phase == "Counting" || value.Phase == "Dry run" {
		if value.BytesTotal > 0 {
			done, total, known, unit = value.BytesScanned, value.BytesTotal, true, "bytes"
		}
	}
	percent := "Unknown"
	if pct, ok := progress.Percent(done, total); ok {
		percent = fmt.Sprintf("%.1f%%", pct)
	}
	totalText := "Unknown"
	if known {
		totalText = comma(total)
	}
	matchingTotal := "Unknown"
	if value.MatchesKnown {
		matchingTotal = comma(value.MatchesTotal)
	}
	etaText := "Unknown"
	if eta, ok := progress.ETA(total-done, speed); known && ok && now.Sub(p.started) >= 2*time.Second {
		etaText = progress.Duration(eta)
	}
	speedText := progress.Rate(speed, "nodes/sec")
	if unit == "bytes" {
		speedText = progress.Bytes(int64(speed)) + "/s"
	}
	validLabel := "valid"
	if value.Phase == "Importing" {
		validLabel = "valid/staged"
	}
	line := fmt.Sprintf("%s %s %s %s=%s/%s matches=%s/%s %s=%s rejected=%s speed=%s elapsed=%s ETA=%s",
		value.Phase, progress.Bar(done, total, 20), percent, unit, comma(done), totalText,
		comma(value.Matches), matchingTotal, validLabel, comma(value.ValidMatches), comma(value.Rejected),
		speedText, progress.Duration(now.Sub(p.started)), etaText)
	if value.Phase == "Importing" {
		line += " inserted/updated/skipped=pending final sync"
	}
	if p.interactive {
		fmt.Fprintf(p.out, "\r\033[2K%s", line)
	} else {
		fmt.Fprintln(p.out, line)
	}
}

func (p *importPrinter) finish() {
	if p.enabled && p.interactive {
		p.mu.Lock()
		fmt.Fprintln(p.out)
		p.mu.Unlock()
	}
}

type downloadPrinter struct {
	mu          sync.Mutex
	enabled     bool
	out         io.Writer
	interactive bool
	started     time.Time
	lastPrinted time.Time
	printed     bool
}

func newDownloadPrinter(enabled bool, out io.Writer, interactive bool, started time.Time) *downloadPrinter {
	return &downloadPrinter{enabled: enabled, out: out, interactive: interactive, started: started}
}

func (p *downloadPrinter) update(value downloader.Progress) {
	if !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if value.State == "Downloading" && now.Sub(p.lastPrinted) < time.Second {
		return
	}
	p.lastPrinted = now
	percent := "Unknown"
	if pct, ok := progress.Percent(value.Downloaded, value.Total); value.TotalKnown && ok {
		percent = fmt.Sprintf("%.1f%%", pct)
	}
	total := "Unknown"
	if value.TotalKnown {
		total = progress.Bytes(value.Total)
	}
	eta := "Unknown"
	if value.ETAKnown {
		eta = progress.Duration(value.ETA)
	}
	line := fmt.Sprintf("%s %s %s\nDownloaded: %s / %s (%s)\nSpeed: %s/s  Elapsed: %s  Remaining: %s",
		value.State, progress.Bar(value.Downloaded, value.Total, 24), percent,
		progress.Bytes(value.Downloaded), total,
		percent, progress.Bytes(int64(value.SpeedBytes)),
		progress.Duration(time.Since(p.started)), eta)
	if p.interactive {
		if p.printed {
			// The cursor is left on the line after the three-line block, so
			// redraw from its first row rather than leaving the old bar behind.
			fmt.Fprintf(p.out, "\033[%dA", strings.Count(line, "\n")+1)
		}
		fmt.Fprintf(p.out, "\r\033[2K%s\n", strings.ReplaceAll(line, "\n", "\n\r\033[2K"))
	} else {
		fmt.Fprintln(p.out, strings.ReplaceAll(line, "\n", " | "))
	}
	p.printed = true
}

func (p *downloadPrinter) finish() {
	if p.enabled && p.interactive {
		p.mu.Lock()
		fmt.Fprintln(p.out)
		p.mu.Unlock()
	}
}

func printImportSummary(out io.Writer, country, category, dbCategory, file, database string, stats importer.Stats, elapsed time.Duration, dryRun bool) {
	fmt.Fprintln(out, "\n==========================================")
	if dryRun {
		fmt.Fprintln(out, " OSM IMPORT DRY RUN")
	} else {
		fmt.Fprintln(out, " OSM IMPORT COMPLETED")
	}
	fmt.Fprintln(out, "==========================================")
	fmt.Fprintf(out, "Country:           %s\nCLI Category:      %s\nDatabase Category: %s\n", title(country), category, dbCategory)
	fmt.Fprintf(out, "OSM File:          %s\nNodes Scanned:     %s\nMatching Nodes:    %s\nValid Matches:     %s\n",
		file, comma(stats.TotalNodes), comma(stats.MatchingNodes), comma(stats.ValidNodes))
	if dryRun {
		fmt.Fprintf(out, "Rejected:          %s\nDatabase Writes:   0\n", comma(stats.Rejected))
		for _, example := range stats.RejectedExamples {
			fmt.Fprintf(out, "  Validation: %s\n", example)
		}
	} else {
		fmt.Fprintf(out, "Inserted:          %s\nUpdated:           %s\nSkipped:           %s\nRejected:          %s\n",
			comma(stats.Inserted), comma(stats.Updated), comma(stats.Skipped), comma(stats.Rejected))
		fmt.Fprintf(out, "SQLite Database:   %s\nR-Tree:            Verified\nStatus:            SUCCESS\n", database)
	}
	fmt.Fprintf(out, "Source SHA-256:    %s\nTime Elapsed:      %s\n", stats.SourceSHA256, progress.Duration(elapsed))
	if elapsed > 0 {
		passes := 2.0
		if dryRun {
			passes = 1
		}
		fmt.Fprintf(out, "Average Speed:     %s\n", progress.Rate(passes*float64(stats.TotalNodes)/elapsed.Seconds(), "nodes/sec"))
	}
}

func printRootHelp(out io.Writer) {
	fmt.Fprintln(out, `Mapine OpenStreetMap offline data tool

Usage:
  ./bin/osm download <country> [options]
  ./bin/osm import <category> [options]
  ./bin/osm categories [options]

Commands:
  download    Fetch a Geofabrik country PBF snapshot (does not import it)
  import      Match OSM Nodes from a local PBF and store them in Mapine SQLite
  categories  List configured CLI-to-OSM category mappings

Examples:
  ./bin/osm download iran
  ./bin/osm categories
  ./bin/osm import public-restroom --dry-run
  ./bin/osm import public-restroom`)
}

func printDownloadHelp(out io.Writer) {
	fmt.Fprintln(out, `Usage: ./bin/osm download <country> [options]

Options:
  --data-dir DIR    Snapshot directory (default: OSM_DATA_DIR or ./data/osm)
  --output PATH     Exact output filename
  --countries PATH  Country registry (default: configs/osm/countries.yaml)
  --timeout D       Maximum transfer time (default: 4h)
  --force           Fetch a fresh snapshot even when validators match
  --quiet           Suppress live progress output
  --help            Show this help

Existing snapshots are checked with remote validators. Use --force when remote
metadata cannot establish currentness. Interrupted transfers retain safe partial
data for a validated HTTP Range/If-Range retry.`)
}

func printImportHelp(out io.Writer) {
	fmt.Fprintln(out, `Usage: ./bin/osm import <category> [options]

Options:
  --country NAME    Source country (default: iran)
  --file PATH       Local PBF file (default: data directory/country filename)
  --data-dir DIR    Snapshot directory (default: OSM_DATA_DIR or ./data/osm)
  --db PATH         Existing Mapine SQLite DB (default: OSM_DB_PATH or bin/volume/data/mapine.db)
  --config PATH     Category mappings (default: configs/osm/categories.yaml)
  --countries PATH  Country registry (default: configs/osm/countries.yaml)
  --batch-size N    Staging records per SQLite transaction (default: 1000)
  --workers N       PBF decoding concurrency (default: up to 4 CPUs)
  --dry-run         Match/count in one scan without database writes or history
  --quiet           Suppress live progress output
  --verbose         Print additional configuration details
  --help            Show this help

Normal imports perform an exact counting pass followed by a staged import pass,
so the PBF is read twice. Dry runs scan once and perform no database writes.`)
}

func positionalAndFlags(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func workerDescription(workers int) string {
	if workers == 0 {
		defaultWorkers := runtime.GOMAXPROCS(0)
		if defaultWorkers > 4 {
			defaultWorkers = 4
		}
		return fmt.Sprintf("%d (default, capped)", defaultWorkers)
	}
	return fmt.Sprint(workers)
}

func comma(value int64) string {
	if value < 0 {
		return "-" + comma(-value)
	}
	s := fmt.Sprint(value)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func sortStrings(values []string) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
