//go:build !windows

package main

import "os"

func publishSubtitle(_, dir *os.Root, staging, name string) error {
	return dir.Rename(staging, name)
}
