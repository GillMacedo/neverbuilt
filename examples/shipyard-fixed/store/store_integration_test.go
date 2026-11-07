//go:build integration

package store

import "testing"

func TestSaveToSharedVolume(t *testing.T) {
	// Needs a shared volume, so CI runs it with -tags integration.
	if err := Save("/mnt/shared", Record{ID: "b2", Weight: 5}); err != nil {
		t.Fatal(err)
	}
}
