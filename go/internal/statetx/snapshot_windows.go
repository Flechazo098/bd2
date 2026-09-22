//go:build windows

package statetx

import (
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getDiskFreeSpaceW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")

const fileSupportsBlockRefcounting = 0x08000000

type windowsVolumeSnapshotInfo struct {
	clusterSize int64
	blockClone  bool
}

var windowsVolumeCache sync.Map

type duplicateExtentsData struct {
	SourceHandle windows.Handle
	SourceOffset int64
	TargetOffset int64
	ByteCount    int64
}

// ReFS block cloning creates an independent copy-on-write file. NTFS does not
// expose file-level CoW cloning, so it uses a hard link to the immutable old
// inode; every account Store is required to publish through temp+replace.
// Unsupported Windows filesystems fall back to a synchronized byte copy.
func snapshotBefore(source, destination string) error {
	if volume, err := volumeSnapshotInfo(source); err == nil && volume.blockClone {
		if err := blockCloneWindows(source, destination, volume.clusterSize); err == nil {
			return nil
		}
		_ = os.Remove(destination)
	}
	if err := os.Link(source, destination); err == nil {
		return nil
	}
	return copySynced(source, destination)
}

func blockCloneWindows(source, destination string, clusterSize int64) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	size := info.Size()
	cloneBytes := size / clusterSize * clusterSize
	if cloneBytes == 0 {
		return windows.ERROR_NOT_SUPPORTED
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err = dst.Truncate(size); err == nil && size != 0 {
		input := duplicateExtentsData{
			SourceHandle: windows.Handle(src.Fd()),
			ByteCount:    cloneBytes,
		}
		var returned uint32
		err = windows.DeviceIoControl(
			windows.Handle(dst.Fd()),
			windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
			(*byte)(unsafe.Pointer(&input)), uint32(unsafe.Sizeof(input)),
			nil, 0, &returned, nil,
		)
	}
	if err == nil && cloneBytes < size {
		tail := make([]byte, size-cloneBytes)
		if _, readErr := src.ReadAt(tail, cloneBytes); readErr != nil {
			err = readErr
		} else {
			_, err = dst.WriteAt(tail, cloneBytes)
		}
	}
	if err == nil {
		err = dst.Sync()
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	return err
}

func volumeSnapshotInfo(path string) (windowsVolumeSnapshotInfo, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windowsVolumeSnapshotInfo{}, err
	}
	volume := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(pathUTF16, &volume[0], uint32(len(volume))); err != nil {
		return windowsVolumeSnapshotInfo{}, err
	}
	volumeName := windows.UTF16ToString(volume)
	if cached, ok := windowsVolumeCache.Load(volumeName); ok {
		return cached.(windowsVolumeSnapshotInfo), nil
	}
	var flags uint32
	if err := windows.GetVolumeInformation(&volume[0], nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return windowsVolumeSnapshotInfo{}, err
	}
	var sectorsPerCluster, bytesPerSector uint32
	result, _, callErr := getDiskFreeSpaceW.Call(
		uintptr(unsafe.Pointer(&volume[0])),
		uintptr(unsafe.Pointer(&sectorsPerCluster)),
		uintptr(unsafe.Pointer(&bytesPerSector)),
		0, 0,
	)
	if result == 0 {
		return windowsVolumeSnapshotInfo{}, callErr
	}
	cluster := uint64(sectorsPerCluster) * uint64(bytesPerSector)
	if cluster == 0 || cluster > uint64(^uint32(0)) {
		return windowsVolumeSnapshotInfo{}, windows.ERROR_INVALID_DATA
	}
	resultInfo := windowsVolumeSnapshotInfo{clusterSize: int64(cluster), blockClone: flags&fileSupportsBlockRefcounting != 0}
	windowsVolumeCache.Store(volumeName, resultInfo)
	return resultInfo, nil
}
