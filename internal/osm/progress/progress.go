package progress

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d > 0 && d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	seconds := int64(d.Round(time.Second).Seconds())
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	seconds %= 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func Bytes(v int64) string {
	if v < 0 {
		return "Unknown"
	}
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	value := float64(v)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

func Rate(value float64, unit string) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func Percent(done, total int64) (float64, bool) {
	if total <= 0 {
		return 0, false
	}
	value := float64(done) / float64(total) * 100
	return math.Max(0, math.Min(100, value)), true
}

func Bar(done, total int64, width int) string {
	if width < 1 {
		return ""
	}
	percent, known := Percent(done, total)
	if !known {
		return "[" + strings.Repeat("░", width) + "]"
	}
	filled := int(math.Round(percent / 100 * float64(width)))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// EMA is a small exponential moving average used to smooth live throughput.
type EMA struct {
	alpha float64
	value float64
	ready bool
}

func NewEMA(alpha float64) *EMA {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.25
	}
	return &EMA{alpha: alpha}
}

func (e *EMA) Add(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return e.value
	}
	if !e.ready {
		e.value, e.ready = value, true
	} else {
		e.value = e.alpha*value + (1-e.alpha)*e.value
	}
	return e.value
}

func (e *EMA) Value() float64 {
	if e == nil || !e.ready {
		return 0
	}
	return e.value
}

func ETA(remaining int64, perSecond float64) (time.Duration, bool) {
	if remaining <= 0 {
		return 0, true
	}
	if perSecond <= 0 || math.IsNaN(perSecond) || math.IsInf(perSecond, 0) {
		return 0, false
	}
	seconds := float64(remaining) / perSecond
	if seconds < 0 || math.IsInf(seconds, 0) || seconds > float64((time.Duration(1<<63-1))/time.Second) {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}
