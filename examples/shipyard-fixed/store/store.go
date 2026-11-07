// Package store keeps made-up shipment records in files.
package store

import "os"

// Record is one shipment.
type Record struct {
	ID     string
	Weight int
}

// Save writes r to dir.
func Save(dir string, r Record) error {
	return writeAtomic(dir+"/"+r.ID, []byte(r.ID))
}

// writeAtomic replaced writeFile in a refactor.
func writeAtomic(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
