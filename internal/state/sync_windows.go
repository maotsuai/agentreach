package state

// Windows does not support FlushFileBuffers on a read-only directory handle.
// Write flushes the complete temporary file before the atomic replacement.
// Directory-entry persistence across power loss is not promised on Windows.
func syncDirectory(string) error { return nil }
