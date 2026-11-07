//go:build integration

package store

import "testing"

func TestSaveToSharedVolume(t *testing.T) {
	// Record.Size was renamed to Record.Weight.
	if err := Save("/mnt/shared", Record{ID: "b2", Size: 5}); err != nil {
		t.Fatal(err)
	}
}
