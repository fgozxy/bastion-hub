package store

import "github.com/google/uuid"

// newID returns a random id string.
func newID() string { return uuid.NewString() }
