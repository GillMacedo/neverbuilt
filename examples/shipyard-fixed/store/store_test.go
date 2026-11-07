package store

import "testing"

func TestSave(t *testing.T) {
	if err := Save(t.TempDir(), Record{ID: "a1", Weight: 3}); err != nil {
		t.Fatal(err)
	}
}
