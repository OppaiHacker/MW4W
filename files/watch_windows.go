//go:build windows

package main

// Live index updates: ReadDirectoryChangesW on each root, whole tree. Changed folders are
// collected for a moment and read again (see Index.changed). A lost batch (buffer overflow)
// means a full rescan.

import (
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var watching = false

func watchRoots(roots []string, changed func([]string), overflow func()) {
	var mu sync.Mutex
	dirty := map[string]bool{}
	for _, root := range roots {
		root := filepath.Clean(root)
		p, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			continue
		}
		watching = true
		go func() {
			defer windows.CloseHandle(h)
			buf := make([]byte, 64<<10)
			const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
				windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE
			for {
				var n uint32
				if err := windows.ReadDirectoryChanges(h, &buf[0], uint32(len(buf)), true, mask, &n, nil, 0); err != nil {
					return
				}
				if n == 0 {
					overflow()
					continue
				}
				mu.Lock()
				for off := uint32(0); ; {
					fi := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
					name := windows.UTF16ToString(unsafe.Slice(&fi.FileName, fi.FileNameLength/2))
					dirty[filepath.Dir(filepath.Join(root, name))] = true
					if fi.NextEntryOffset == 0 {
						break
					}
					off += fi.NextEntryOffset
				}
				mu.Unlock()
			}
		}()
	}
	if !watching {
		return
	}
	for range time.Tick(700 * time.Millisecond) {
		mu.Lock()
		if len(dirty) == 0 {
			mu.Unlock()
			continue
		}
		dirs := make([]string, 0, len(dirty))
		for d := range dirty {
			dirs = append(dirs, d)
		}
		dirty = map[string]bool{}
		mu.Unlock()
		changed(dirs)
	}
}
