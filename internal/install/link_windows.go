//go:build windows

package install

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func reparseTag(path string) uint32 {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var data syscall.Win32finddata
	h, err := syscall.FindFirstFile(name, &data)
	if err != nil {
		return 0
	}
	_ = syscall.FindClose(h)
	if data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return 0
	}
	return data.Reserved0
}

// makeJunction creates link as a directory junction to target, which
// needs no privilege. target must be an absolute path on a local volume.
func makeJunction(target, link string) error {
	target = strings.TrimPrefix(target, `\\?\`)
	if strings.HasPrefix(target, `\\`) {
		return errors.New("a junction can't point at a network path")
	}
	if err := os.Mkdir(link, 0o777); err != nil {
		return err
	}
	if err := setMountPoint(link, target); err != nil {
		_ = os.Remove(link)
		return err
	}
	return nil
}

// mountPointBuffer is a REPARSE_DATA_BUFFER for IO_REPARSE_TAG_MOUNT_POINT:
// the 8 byte header, four name offsets and lengths, then the substitute and
// the print name, each ending in a NUL.
func mountPointBuffer(target string) []byte {
	sub := utf16.Encode([]rune(`\??\` + target))
	printName := utf16.Encode([]rune(target))
	pathBytes := (len(sub) + 1 + len(printName) + 1) * 2
	buf := make([]byte, 16+pathBytes)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], reparseTagMountPoint)
	le.PutUint16(buf[4:], uint16(8+pathBytes))
	le.PutUint16(buf[10:], uint16(len(sub)*2))
	le.PutUint16(buf[12:], uint16((len(sub)+1)*2))
	le.PutUint16(buf[14:], uint16(len(printName)*2))
	off := 16
	for _, u := range sub {
		le.PutUint16(buf[off:], u)
		off += 2
	}
	off += 2
	for _, u := range printName {
		le.PutUint16(buf[off:], u)
		off += 2
	}
	return buf
}

func setMountPoint(link, target string) error {
	buf := mountPointBuffer(target)
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var returned uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &returned, nil)
}
