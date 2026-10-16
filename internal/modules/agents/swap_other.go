//go:build !darwin && !linux

package agents

import "errors"

func exchangeDirs(a, b string) error {
	return errors.ErrUnsupported
}
