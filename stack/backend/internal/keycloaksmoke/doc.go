// Package keycloaksmoke holds the local-Keycloak login smoke test, guarded by
// the keycloak_smoke build tag (see smoke_test.go). This untagged file gives the
// package a buildable Go source in every configuration so the normal
// `go test ./...` run treats it as an ordinary (test-only, no-op) package rather
// than failing with "build constraints exclude all Go files". The smoke test
// itself runs only under the build tag, in the dedicated compose-stack CI job.
package keycloaksmoke
