package main

// metered reports whether the connection is metered. Windows is not asked
// yet; data saver can still be turned on from the toolbar.
func metered() (bool, error) { return false, nil }
