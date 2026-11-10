package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// dayWriter appends NDJSON lines to one file per local day.
type dayWriter struct {
	mu     sync.Mutex
	dir    string
	prefix string
	day    string
	file   *os.File
}

func newDayWriter(dir, prefix string) *dayWriter {
	return &dayWriter{dir: dir, prefix: prefix}
}

// writeLine appends one complete line to the file for the given local day.
func (w *dayWriter) writeLine(day string, line []byte) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil || w.day != day {
		if w.file != nil {
			_ = w.file.Close()
			w.file = nil
		}
		if errMkdir := os.MkdirAll(w.dir, 0o755); errMkdir != nil {
			return errMkdir
		}
		path := filepath.Join(w.dir, w.prefix+"-"+day+".ndjson")
		file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if errOpen != nil {
			return errOpen
		}
		w.file = file
		w.day = day
	}
	_, errWrite := w.file.Write(line)
	return errWrite
}

// close releases the underlying file handle.
func (w *dayWriter) close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	w.day = ""
}

var ownedDayFile = regexp.MustCompile(`^(raw|key-usage|account-quota|quota-headers)-(\d{4}-\d{2}-\d{2})\.ndjson$`)

// pruneOwnedFiles removes this plugin's own day files older than the cutoff day.
// Only files matching the three known prefixes are touched.
func pruneOwnedFiles(dir string, cutoff time.Time) {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := ownedDayFile.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		day, errParse := time.ParseInLocation("2006-01-02", match[2], cutoff.Location())
		if errParse != nil {
			continue
		}
		if day.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
