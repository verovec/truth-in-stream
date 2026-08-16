//go:build localinference

// Package onnxrt owns the process-wide ONNX Runtime environment shared by
// every local inference package (the check-worthiness scorer, the NLI stance
// scorer). The runtime's shared-library path is global and must be set before
// the environment initializes, and initialization may happen only once per
// process, so the sync.Once lives here rather than in any one consumer.
package onnxrt

import (
	"log/slog"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var (
	initOnce sync.Once
	initErr  error
	initPath string
)

// Init loads the ONNX Runtime shared library once per process. The first
// caller's non-empty path wins; a later caller passing a different non-empty
// path gets the already-initialized runtime and a warning, because the
// library cannot be swapped after initialization. An empty path falls back to
// the loader's platform default lookup. The first call's error is memoized so
// every consumer degrades identically.
func Init(libraryPath string, logger *slog.Logger) error {
	initOnce.Do(func() {
		initPath = libraryPath
		if libraryPath != "" {
			ort.SetSharedLibraryPath(libraryPath)
		}
		initErr = ort.InitializeEnvironment()
	})
	if initErr == nil && libraryPath != "" && initPath != libraryPath && logger != nil {
		logger.Warn("onnx runtime already initialized with a different library path; keeping the first",
			slog.String("initialized", initPath), slog.String("requested", libraryPath))
	}
	return initErr
}
