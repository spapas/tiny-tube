package main

import "golang.org/x/sys/windows"

func availableDiskBytes(path string) (int64, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(name, &available, nil, nil)
	return int64(available), err
}
