//go:build !windows

package install

import "errors"

func makeJunction(string, string) error { return errors.ErrUnsupported }

func reparseTag(string) uint32 { return 0 }
