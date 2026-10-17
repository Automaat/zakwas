//go:build windows

package install

import "io/fs"

// statPerm is the permission os.Stat reports for a file zakwas gave perm:
// Windows has only the read-only attribute, which shows as 0444 against 0666.
func statPerm(perm fs.FileMode) fs.FileMode {
	if perm&0o200 == 0 {
		return 0o444
	}
	return 0o666
}
