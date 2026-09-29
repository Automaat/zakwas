package install

import "golang.org/x/sys/unix"

const immutableSupported = true

func isImmutable(path string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return false, err
	}
	return st.Flags&unix.UF_IMMUTABLE != 0, nil
}

func setImmutable(path string, on bool) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	flags := st.Flags &^ unix.UF_IMMUTABLE
	if on {
		flags |= unix.UF_IMMUTABLE
	}
	if flags == st.Flags {
		return nil
	}
	return unix.Chflags(path, int(flags))
}
