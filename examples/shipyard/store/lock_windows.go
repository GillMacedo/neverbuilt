package store

// lockDir still calls the old helper, which no longer exists.
func lockDir(dir string) error { return writeFile(dir+"/.lock", nil) }
