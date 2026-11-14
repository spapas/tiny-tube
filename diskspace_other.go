//go:build !windows && !linux && !darwin

package main

import "errors"

func availableDiskBytes(string) (int64, error) {
	return 0, errors.ErrUnsupported
}
