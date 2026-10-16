// Package fsutil holds the file operations whose behavior differs between
// Unix and Windows, so callers get the Unix semantics on both.
package fsutil

// Rename moves oldpath over newpath, replacing a file already there.
//
// On Windows it also replaces a read-only file, as rename(2) does, and
// retries briefly while another process (an antivirus scan, an agent
// watching its config) holds the destination open.
func Rename(oldpath, newpath string) error {
	return rename(oldpath, newpath)
}

// SyncDir flushes the directory entry changes of dir to disk, where the
// platform allows it. Windows can't flush a directory handle, so it does
// nothing there.
func SyncDir(dir string) error {
	return syncDir(dir)
}
