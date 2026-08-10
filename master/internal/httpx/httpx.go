// Package httpx provides small HTTP helper functions for handlers.
package httpx

import (
	"encoding/json"
	"net/http"
)

// JSON writes a JSON response.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// OK writes a 200 JSON response.
func OK(w http.ResponseWriter, v any) { JSON(w, http.StatusOK, v) }

// ReadJSON decodes a JSON body into v.
func ReadJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// Err writes an {"error": msg} JSON response.
func Err(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// InternalErr logs and writes a 500.
func InternalErr(w http.ResponseWriter, msg string) {
	Err(w, http.StatusInternalServerError, msg)
}
