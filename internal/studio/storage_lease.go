package studio

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

const storageLeaseName = ".studio-writer.lock"

// StorageLease excludes competing applications and offline maintenance from
// the same private directories, including applications using another database.
// Lock files remain on disk; OS locks, not their existence, determine ownership.
type StorageLease struct {
	once  sync.Once
	locks []*flock.Flock
	err   error
}

func AcquireStorageLease(directories ...string) (*StorageLease, error) {
	if len(directories) == 0 {
		return nil, errors.New("private storage directories are required")
	}
	paths := make([]string, 0, len(directories))
	seen := make(map[string]bool)
	for _, directory := range directories {
		if strings.TrimSpace(directory) == "" {
			return nil, errors.New("private storage directory is empty")
		}
		resolved, err := resolveConfigPath(directory)
		if err != nil {
			return nil, errors.New("private storage directory cannot be resolved")
		}
		identity := strings.ToLower(filepath.Clean(resolved))
		if !seen[identity] {
			seen[identity] = true
			paths = append(paths, resolved)
		}
	}
	sort.Strings(paths)
	lease := &StorageLease{}
	for _, directory := range paths {
		if err := os.MkdirAll(directory, 0700); err != nil {
			_ = lease.Close()
			return nil, errors.New("private storage directory cannot be opened")
		}
		lockPath := filepath.Join(directory, storageLeaseName)
		if stat, err := os.Lstat(lockPath); err == nil && (!stat.Mode().IsRegular() || stat.Mode()&os.ModeSymlink != 0) {
			_ = lease.Close()
			return nil, errors.New("private storage lock path is not a regular file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = lease.Close()
			return nil, errors.New("private storage lock path cannot be inspected")
		}
		lock := flock.New(lockPath, flock.SetPermissions(0600))
		locked, err := lock.TryLock()
		if err != nil || !locked {
			_ = lock.Close()
			_ = lease.Close()
			return nil, errors.New("private storage directory is in use; stop its writer before maintenance or another instance")
		}
		lease.locks = append(lease.locks, lock)
	}
	return lease, nil
}

func (s *StorageLease) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		for index := len(s.locks) - 1; index >= 0; index-- {
			if err := s.locks[index].Close(); err != nil {
				s.err = errors.New("private storage directory lock could not be released")
			}
		}
	})
	return s.err
}
