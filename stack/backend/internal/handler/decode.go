package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// decodeJSONBody decodes a size-bounded JSON request body into dst, writing
// the error response itself on failure so every JSON endpoint reports the
// same 413/400 contract. It reports whether decoding succeeded.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes)).Decode(dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
