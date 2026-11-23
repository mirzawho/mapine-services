package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/mirzawho/mapine-services/internal/osm/downloader"
	"github.com/mirzawho/mapine-services/internal/osm/importer"
)

func TestCommandHelpAndArgumentPosition(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := runDownload([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := runImport([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := runCategories([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	category, rest := positionalAndFlags([]string{"public-restroom", "--dry-run"})
	if category != "public-restroom" || len(rest) != 1 || rest[0] != "--dry-run" {
		t.Fatalf("positionalAndFlags = %q, %v", category, rest)
	}
}

func TestImportProgressShowsKnownTotalsAndNoEscapeWhenRedirected(t *testing.T) {
	var output bytes.Buffer
	started := time.Now().Add(-5 * time.Second)
	printer := newImportPrinter(true, &output, false, started)
	printer.update(importer.Progress{
		Phase: "Importing", NodesScanned: 70, NodesTotal: 100, TotalKnown: true,
		Matches: 8, MatchesTotal: 10, MatchesKnown: true, ValidMatches: 7,
		Rejected: 1, Started: started,
	})
	line := output.String()
	for _, expected := range []string{"70.0%", "70/100", "matches=8/10", "valid/staged=7", "rejected=1", "pending final sync"} {
		if !strings.Contains(line, expected) {
			t.Errorf("progress output %q missing %q", line, expected)
		}
	}
	if strings.Contains(line, "\x1b") {
		t.Fatal("redirected progress contains ANSI escape codes")
	}
}

func TestInteractiveProgressUsesTerminalUpdates(t *testing.T) {
	started := time.Now().Add(-time.Second)
	var importOutput bytes.Buffer
	importDisplay := newImportPrinter(true, &importOutput, true, started)
	importDisplay.update(importer.Progress{
		Phase: "Importing", NodesScanned: 1, NodesTotal: 1, TotalKnown: true,
		Matches: 1, MatchesTotal: 1, MatchesKnown: true, Done: true,
	})
	importDisplay.finish()
	if !strings.Contains(importOutput.String(), "\x1b[2K") {
		t.Fatalf("interactive import output lacks terminal line update: %q", importOutput.String())
	}

	var downloadOutput bytes.Buffer
	downloadDisplay := newDownloadPrinter(true, &downloadOutput, true, started)
	downloadDisplay.update(downloader.Progress{
		State: "Downloading", Downloaded: 25, Total: 100, TotalKnown: true,
	})
	downloadDisplay.lastPrinted = time.Now().Add(-2 * time.Second)
	downloadDisplay.update(downloader.Progress{
		State: "Downloading", Downloaded: 50, Total: 100, TotalKnown: true,
	})
	downloadDisplay.finish()
	if !strings.Contains(downloadOutput.String(), "\x1b[3A") ||
		!strings.Contains(downloadOutput.String(), "25.0%") ||
		!strings.Contains(downloadOutput.String(), "50.0%") {
		t.Fatalf("interactive download output lacks terminal line update: %q", downloadOutput.String())
	}
}

func TestDownloadProgressUnknownTotalsStayUnknown(t *testing.T) {
	var output bytes.Buffer
	printer := newDownloadPrinter(true, &output, false, time.Now().Add(-time.Second))
	printer.update(downloader.Progress{
		State: "Complete", Downloaded: 1024, TotalKnown: false, ETAKnown: false,
	})
	line := output.String()
	if !strings.Contains(line, "Unknown") || !strings.Contains(line, "Remaining: Unknown") {
		t.Fatalf("unknown total/ETA not rendered honestly: %q", line)
	}
	if strings.Contains(line, "\x1b") {
		t.Fatal("redirected progress contains ANSI escape codes")
	}
}

func TestFlagHelpReturnsNormally(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("help", false, "help")
	if err := fs.Parse([]string{"-h"}); err != flag.ErrHelp {
		t.Fatalf("Parse help error = %v", err)
	}
}
