package graph

// Fetches the prebuilt Ladybug native library into <module-root>/lib-ladybug
// before build. Run `go generate ./...` with network access. The target dir is
// resolved from `go env GOMOD` so it works from any CWD and without a .git dir
// (the build containers mount only stack/backend).
//go:generate sh -c "curl -fsSL https://raw.githubusercontent.com/LadybugDB/ladybug/refs/heads/main/scripts/download-liblbug.sh | LBUG_TARGET_DIR=\"$(dirname \"$(go env GOMOD)\")/lib-ladybug\" bash"
