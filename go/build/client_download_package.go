package main

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func (t task) packageBepInEx(game, bepinex, client string) error {
	file, err := os.Create(filepath.Join(client, "bepinex.zip"))
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	add := func(source, name string) error {
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		info, err := in.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("BepInEx package entries must be regular files")
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		header.Method = zip.Deflate
		out, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		return err
	}
	var result error
	for _, name := range []string{"winhttp.dll", "doorstop_config.ini"} {
		if result = add(filepath.Join(game, name), name); result != nil {
			break
		}
	}
	if result == nil {
		core := filepath.Join(bepinex, "core")
		result = filepath.WalkDir(core, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("BepInEx core must not contain symlinks")
			}
			relative, err := filepath.Rel(core, path)
			if err != nil {
				return err
			}
			return add(path, filepath.Join("BepInEx", "core", relative))
		})
	}
	return errors.Join(result, archive.Close(), file.Close())
}
