package agents

import "golang.org/x/sys/unix"

func exchangeDirs(a, b string) error {
	return unix.RenamexNp(a, b, unix.RENAME_SWAP)
}
