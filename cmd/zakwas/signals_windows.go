//go:build windows

package main

import "syscall"

const hasSIGTERMAndHUP = false

func killSelf(syscall.Signal) {}
