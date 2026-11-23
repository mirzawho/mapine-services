package progress

import (
	"testing"
	"time"
)

func TestFormattingAndPercentages(t *testing.T) {
	if got := Duration(45 * time.Second); got != "00:45" {
		t.Fatalf("Duration = %q", got)
	}
	if got := Duration(400 * time.Millisecond); got != "0.4s" {
		t.Fatalf("subsecond Duration = %q", got)
	}
	if got := Duration(72*time.Minute + 18*time.Second); got != "01:12:18" {
		t.Fatalf("Duration = %q", got)
	}
	if _, ok := Percent(1, 0); ok {
		t.Fatal("zero total should be unknown")
	}
	if got, ok := Percent(0, 100); !ok || got != 0 {
		t.Fatalf("zero-work percent = %v, %v", got, ok)
	}
	if got, ok := Percent(1, 4); !ok || got != 25 {
		t.Fatalf("Percent = %v, %v", got, ok)
	}
	if got := Bar(0, 0, 4); got != "[░░░░]" {
		t.Fatalf("unknown bar = %q", got)
	}
}

func TestETAAndEMA(t *testing.T) {
	if _, ok := ETA(10, 0); ok {
		t.Fatal("zero rate should produce unknown ETA")
	}
	if got, ok := ETA(10, 2); !ok || got != 5*time.Second {
		t.Fatalf("ETA = %s, %v", got, ok)
	}
	if got, ok := ETA(0, 0); !ok || got != 0 {
		t.Fatalf("completed ETA = %s, %v", got, ok)
	}
	ema := NewEMA(0.5)
	ema.Add(10)
	if got := ema.Add(20); got != 15 {
		t.Fatalf("EMA = %v", got)
	}
}
