package safepoint

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func copyPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.Symlink(link, target)
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
			return err
		}
		return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == source {
				return nil
			}
			if entry.Name() == ".svn" {
				return fmt.Errorf("不能完整保护包含 Subversion 元数据的路径 %s", path)
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			destination := filepath.Join(target, relative)
			entryInfo, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if entryInfo.IsDir() {
				return os.MkdirAll(destination, entryInfo.Mode().Perm())
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
					return err
				}
				return os.Symlink(link, destination)
			}
			if !entryInfo.Mode().IsRegular() {
				return fmt.Errorf("%s 是不支持的文件类型", path)
			}
			return copyRegularFile(path, destination, entryInfo.Mode())
		})
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 是不支持的文件类型", source)
	}
	return copyRegularFile(source, target, info.Mode())
}

func copyRegularFile(source, target string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func replacePath(source, target string, kind EntryKind, linkTarget string, mode fs.FileMode) error {
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	switch kind {
	case EntrySymlink:
		return os.Symlink(linkTarget, target)
	case EntryDir:
		if source == "" {
			return os.MkdirAll(target, mode.Perm())
		}
		return copyPath(source, target)
	case EntryFile:
		return copyPath(source, target)
	default:
		return fmt.Errorf("不支持的 SafePoint 条目类型 %q", kind)
	}
}

func pathMetadata(path string) (EntryKind, fs.FileMode, string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return EntryFile, 0, "", false, nil
		}
		return "", 0, "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return EntrySymlink, info.Mode(), target, true, err
	}
	if info.IsDir() {
		return EntryDir, info.Mode(), "", true, nil
	}
	if info.Mode().IsRegular() {
		return EntryFile, info.Mode(), "", true, nil
	}
	return "", 0, "", false, fmt.Errorf("%s 是不支持的文件类型", path)
}

func looksBinary(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 8*1024)
	data, _ := reader.Peek(8 * 1024)
	return bytes.IndexByte(data, 0) >= 0
}

func directorySize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func samePathContent(left, right string) (bool, error) {
	leftDigest, err := pathDigest(left)
	if err != nil {
		return false, err
	}
	rightDigest, err := pathDigest(right)
	if err != nil {
		return false, err
	}
	return leftDigest == rightDigest, nil
}

func pathDigest(root string) ([sha256.Size]byte, error) {
	digest := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root && entry.Name() == ".svn" && entry.IsDir() {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(digest, "%s\x00%d\x00", filepath.ToSlash(relative), info.Mode()); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = io.WriteString(digest, target)
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	var result [sha256.Size]byte
	if err == nil {
		copy(result[:], digest.Sum(nil))
	}
	return result, err
}
