//go:build !windows

package main

import (
	"os"
	"syscall"
)

const hasSIGTERMAndHUP = true

func killSelf(s syscall.Signal) { _ = syscall.Kill(os.Getpid(), s) }
