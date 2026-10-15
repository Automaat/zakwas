//go:build !darwin

package agents

import "errors"

func exchangeDirs(a, b string) error {
	return errors.ErrUnsupported
}
