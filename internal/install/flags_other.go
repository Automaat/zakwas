//go:build !darwin

package install

const immutableSupported = false

func isImmutable(string) (bool, error) { return false, nil }

func setImmutable(string, bool) error { return nil }
