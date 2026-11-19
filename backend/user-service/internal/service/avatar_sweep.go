package service

import (
	"context"
	"time"
)

// AvatarSweepGrace is how old an unreferenced file must be before the sweep
// removes it. An upload writes its file before the profile change commits, so
// a younger file may still be about to become referenced.
const AvatarSweepGrace = time.Hour

// StoredFile is one file in avatar storage.
type StoredFile struct {
	Name     string
	Modified time.Time
}

// AvatarFileLister lists and removes files in avatar storage.
type AvatarFileLister interface {
	// List returns every file in storage, including abandoned temporary files.
	List() ([]StoredFile, error)
	// RemoveFile deletes one listed file by name. A missing file is not an error.
	RemoveFile(name string) error
}

// AvatarReferences reports which upload keys a profile still names.
type AvatarReferences interface {
	// ReferencedUploads returns the subset of keys some profile uses as its
	// uploaded avatar.
	ReferencedUploads(ctx context.Context, keys []string) (map[string]bool, error)
}

// AvatarSweep removes files no profile references: an upload whose profile
// change failed or was replaced while the old file could not be removed, the
// avatar of a deleted account whose removal failed, and abandoned temporary
// files.
type AvatarSweep struct {
	files AvatarFileLister
	refs  AvatarReferences
	// OnError reports a file that could not be removed; the next sweep retries it.
	OnError func(name string, err error)
}

// NewAvatarSweep returns the sweep.
func NewAvatarSweep(files AvatarFileLister, refs AvatarReferences) *AvatarSweep {
	return &AvatarSweep{files: files, refs: refs}
}

// Sweep removes every file older than AvatarSweepGrace at now that no profile
// references. It returns how many files it removed.
//
// Errors: listing storage or reading references failed; nothing was removed.
func (s *AvatarSweep) Sweep(ctx context.Context, now time.Time) (int, error) {
	files, err := s.files.List()
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-AvatarSweepGrace)
	var candidates, keys []string
	for _, file := range files {
		if !file.Modified.Before(cutoff) {
			continue
		}
		candidates = append(candidates, file.Name)
		if ValidAvatarKey(file.Name) {
			keys = append(keys, file.Name)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	referenced := map[string]bool{}
	if len(keys) > 0 {
		if referenced, err = s.refs.ReferencedUploads(ctx, keys); err != nil {
			return 0, err
		}
	}
	removed := 0
	for _, name := range candidates {
		if referenced[name] {
			continue
		}
		if err := s.files.RemoveFile(name); err != nil {
			if s.OnError != nil {
				s.OnError(name, err)
			}
			continue
		}
		removed++
	}

	return removed, nil
}
