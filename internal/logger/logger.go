package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes timestamped log lines to a daily rotating file in the logs/ directory.
// It is safe for concurrent use from multiple goroutines.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// New opens (or creates) today's log file under logsDir and returns a Logger.
// The file is named YYYY-MM-DD.log based on the local time at the moment New is called.
func New(logsDir string) (*Logger, error) {
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, fmt.Errorf("creating logs dir %s: %w", logsDir, err)
	}

	name := time.Now().Format("2006-01-02") + ".log"
	path := filepath.Join(logsDir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", path, err)
	}

	return &Logger{f: f, path: path}, nil
}

// Path returns the absolute path to the current log file.
func (l *Logger) Path() string { return l.path }

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Cleanup removes log files in logsDir that are older than keepDays days.
// Files must follow the YYYY-MM-DD.log naming convention produced by New().
// It is safe to call at startup — errors are logged to stderr but never fatal.
func Cleanup(logsDir string, keepDays int) {
	if keepDays <= 0 {
		return
	}

	cutoff := time.Now().Truncate(24 * time.Hour).AddDate(0, 0, -keepDays)

	entries, err := os.ReadDir(logsDir)
	if err != nil {
		// Directory may not exist yet on first run — that's fine.
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Expect pattern: 2006-01-02.log
		if len(name) != len("2006-01-02.log") || name[len(name)-4:] != ".log" {
			continue
		}
		t, err := time.Parse("2006-01-02", name[:10])
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			path := filepath.Join(logsDir, name)
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  log cleanup: cannot remove %s: %v\n", path, err)
			}
		}
	}
}

// Info writes an informational line.
func (l *Logger) Info(format string, args ...any) {
	l.write("INFO ", format, args...)
}

// Error writes an error line.
func (l *Logger) Error(format string, args ...any) {
	l.write("ERROR", format, args...)
}

// Step writes a domain cloning step line.
func (l *Logger) Step(domain, status, step, detail string) {
	if detail != "" {
		l.write("STEP ", "[%-36s] %-8s %-22s %s", domain, status, step, detail)
	} else {
		l.write("STEP ", "[%-36s] %-8s %s", domain, status, step)
	}
}

// DomainStart logs that a domain clone has started.
func (l *Logger) DomainStart(domain string) {
	l.write("START", "[%s] cloning started", domain)
}

// DomainDone logs the final result for a domain.
func (l *Logger) DomainDone(domain string, success bool, elapsed time.Duration, errMsg string) {
	if success {
		l.write("DONE ", "[%s] ✓ finished in %s", domain, fmtDur(elapsed))
	} else {
		l.write("FAIL ", "[%s] ✗ failed in %s — %s", domain, fmtDur(elapsed), errMsg)
	}
}

// Summary writes a final batch summary line.
func (l *Logger) Summary(total, success, failed int, elapsed time.Duration) {
	l.write("SUMRY",
		"batch finished — total=%d ok=%d err=%d elapsed=%s",
		total, success, failed, fmtDur(elapsed),
	)
}

// write is the core internal method — prepends a timestamp and flushes to file.
func (l *Logger) write(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), level, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	// Best-effort writes; also mirror to stderr on failure.
	if _, err := io.WriteString(l.f, line); err != nil {
		fmt.Fprint(os.Stderr, line)
	}
}

func fmtDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	return fmt.Sprintf("%dm%.0fs", m, d.Seconds()-float64(m)*60)
}
