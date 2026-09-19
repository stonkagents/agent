package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MinFreeBytes is the free space the data directory must have (1 GiB).
const MinFreeBytes uint64 = 1 << 30

// StorageCandidates returns the directories offered as data_dir choices:
// the current directory first, then platform-specific suggestions.
func StorageCandidates(current string) []string {
	return platformStorageCandidates(current)
}

// StorageCheck evaluates the data directory: it must exist, be writable and
// have at least MinFreeBytes free. Detail carries path, freeBytes and the
// candidate directories.
func StorageCheck(path string) Check {
	detail := map[string]any{
		"path":         path,
		"freeBytes":    nil,
		"minFreeBytes": MinFreeBytes,
		"candidates":   StorageCandidates(path),
	}
	if strings.TrimSpace(path) == "" {
		return Missing(IDStorage, "no data directory configured", detail)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Missing(IDStorage, "data directory does not exist", detail)
		}
		return Failed(IDStorage, fmt.Sprintf("cannot access data directory: %v", err), detail)
	}
	if !info.IsDir() {
		return Failed(IDStorage, "data directory path is not a directory", detail)
	}
	if err := probeWritable(path); err != nil {
		return Failed(IDStorage, fmt.Sprintf("data directory is not writable: %v", err), detail)
	}
	free, err := FreeBytes(path)
	if err != nil {
		return Failed(IDStorage, fmt.Sprintf("cannot read free space: %v", err), detail)
	}
	detail["freeBytes"] = free
	if free < MinFreeBytes {
		return Failed(IDStorage, fmt.Sprintf("only %d MB free; at least 1 GB required", free>>20), detail)
	}
	return OK(IDStorage, detail)
}

// StorageRoots returns the directories a caller-supplied data_dir may live
// under: the current data directory's parent, the user's home directory,
// %ProgramData% on Windows and the platform candidates. Anything else (an
// arbitrary absolute path, a UNC share) is refused by ValidateStoragePath so a
// request cannot point the chunk store at a system or network location.
func StorageRoots(current string) []string {
	var roots []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		roots = append(roots, filepath.Clean(p))
	}
	if current != "" {
		add(filepath.Dir(filepath.Clean(current)))
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	if pd := os.Getenv("ProgramData"); pd != "" {
		add(pd)
	}
	for _, cand := range StorageCandidates(current) {
		add(cand)
	}
	return roots
}

// ValidateStoragePath cleans path and checks it is absolute, not a UNC path
// and located under one of roots. Returns the cleaned path.
func ValidateStoragePath(path string, roots []string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("network (UNC) paths are not allowed")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	path = filepath.Clean(path)
	for _, root := range roots {
		if pathWithin(root, path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("path must be under the current data directory, your profile or the offered candidates")
}

// pathWithin reports whether p equals root or is inside it (both cleaned and
// absolute). Case-insensitive so Windows drive letters and casing differences
// do not matter; the platform separator is used for the boundary check.
func pathWithin(root, p string) bool {
	root, p = strings.ToLower(root), strings.ToLower(p)
	if root == p {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(p, root)
}

// PrepareStorage validates a candidate data directory for the storage fix:
// the path must be absolute; it is created when missing and must then pass
// StorageCheck.
func PrepareStorage(path string) (Check, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Check{}, fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		return Check{}, fmt.Errorf("path must be absolute")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return Check{}, fmt.Errorf("cannot create %s: %w", path, err)
	}
	check := StorageCheck(path)
	if check.Status != StatusOK {
		return check, fmt.Errorf("%s", check.Message)
	}
	return check, nil
}

func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".stonkagents-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
