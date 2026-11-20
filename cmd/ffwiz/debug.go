package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// openDebugLog opens the debug log described in DECISIONS D11:
//
//	<user cache dir>/ffwiz/ffwiz.log
//
// It returns a nil logger (and no error) when logging is off, so the caller can
// treat "off" and "failed" differently. Logging must never stop ffwiz from
// starting: the TUI is useful without it, and a read-only cache directory is not
// the user's problem to solve before they can convert a video.
func openDebugLog(enabled bool) (*log.Logger, io.Closer, error) {
	if !enabled {
		return nil, nil, nil
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, nil, fmt.Errorf("locating the cache directory: %w", err)
	}
	dir = filepath.Join(dir, "ffwiz")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating %s: %w", dir, err)
	}

	path := filepath.Join(dir, "ffwiz.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// A fixed timestamp format makes the log greppable without a parser.
	logger := log.New(file, "", log.LstdFlags)
	logger.Printf("---- ffwiz debug log at %s ----", path)
	return logger, file, nil
}
