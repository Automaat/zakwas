//go:build !windows

package main

import "os"

func enableANSI(*os.File) bool { return true }
