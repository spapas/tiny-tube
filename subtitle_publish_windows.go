package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func publishSubtitle(video, dir *os.Root, staging, name string) error {
	err := dir.Rename(staging, name)
	if !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return err
	}
	return replaceSubtitleOnWindows(video, dir, staging, name)
}

func replaceSubtitleOnWindows(video, dir *os.Root, staging, name string) error {
	// Some Windows environments reject the handle-relative rename. Before
	// falling back to a path-based rename, hold both directories open without
	// delete sharing and verify that they are the same directories os.Root
	// opened. Refuse reparse points on both paths.
	videoHandle, err := pinSubtitleDirectory(video)
	if err != nil {
		return err
	}
	defer videoHandle.Close()
	dirHandle, err := pinSubtitleDirectory(dir)
	if err != nil {
		return err
	}
	defer dirHandle.Close()
	return os.Rename(filepath.Join(dirHandle.Name(), staging), filepath.Join(dirHandle.Name(), name))
}

func pinSubtitleDirectory(root *os.Root) (*os.File, error) {
	path := root.Name()
	if !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\\.\`) {
		if unc, ok := strings.CutPrefix(path, `\\`); ok {
			path = `\\?\UNC\` + unc
		} else {
			path = `\\?\` + path
		}
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open subtitle directory", Path: root.Name(), Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	var attrs windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &attrs); err != nil {
		file.Close()
		return nil, err
	}
	if attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		attrs.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		file.Close()
		return nil, &os.PathError{Op: "verify subtitle directory", Path: root.Name(), Err: os.ErrInvalid}
	}
	current, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	expected, err := root.Stat(".")
	if err != nil {
		file.Close()
		return nil, err
	}
	if !os.SameFile(current, expected) {
		file.Close()
		return nil, &os.PathError{Op: "verify subtitle directory", Path: root.Name(), Err: os.ErrInvalid}
	}
	return file, nil
}
