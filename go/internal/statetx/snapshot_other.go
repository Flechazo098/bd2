//go:build !windows && !linux && !darwin

package statetx

func snapshotBefore(source, destination string) error {
	return copySynced(source, destination)
}
