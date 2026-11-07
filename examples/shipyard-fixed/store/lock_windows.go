package store

// lockDir takes the lock file on Windows.
func lockDir(dir string) error { return writeAtomic(dir+"/.lock", nil) }
