//go:build !windows

package install

import "io/fs"

func statPerm(perm fs.FileMode) fs.FileMode { return perm }
