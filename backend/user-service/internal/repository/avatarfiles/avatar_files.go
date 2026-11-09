// Package avatarfiles stores uploaded avatar images in one directory.
package avatarfiles

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.AvatarFiles = (*Store)(nil)

// Store keeps avatar files in one directory. os.Root confines every operation
// to that directory, even if a link inside it points elsewhere.
type Store struct {
	root *os.Root
}

// Open creates dir when needed and confirms it is writable, so a bad volume
// fails at startup rather than on the first upload.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("avatar storage directory cannot be created: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("avatar storage directory cannot be opened: %w", err)
	}
	store := &Store{root: root}
	name, err := temporaryName()
	if err == nil {
		err = root.WriteFile(name, nil, 0o640)
	}
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("avatar storage directory is not writable: %w", err)
	}
	if err := root.Remove(name); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("avatar storage directory is not writable: %w", err)
	}

	return store, nil
}

// Close releases the directory.
func (s *Store) Close() error {
	return s.root.Close()
}

// Store implements service.AvatarFiles by writing a temporary file and renaming it.
func (s *Store) Store(key string, image []byte) error {
	if !service.ValidAvatarKey(key) {
		return errors.New("invalid avatar key")
	}
	temporary, err := temporaryName()
	if err != nil {
		return err
	}
	if err := s.writeSynced(temporary, image); err != nil {
		_ = s.root.Remove(temporary)
		return err
	}
	if err := s.root.Rename(temporary, key); err != nil {
		_ = s.root.Remove(temporary)
		return err
	}

	return nil
}

func (s *Store) writeSynced(name string, data []byte) error {
	file, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}

	return errors.Join(err, file.Close())
}

// Remove implements service.AvatarFiles.
func (s *Store) Remove(key string) error {
	if !service.ValidAvatarKey(key) {
		return errors.New("invalid avatar key")
	}
	if err := s.root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// Read returns a stored image, or service.ErrAvatarFileNotFound for a missing
// file or a key the service could not have generated.
func (s *Store) Read(key string) ([]byte, error) {
	if !service.ValidAvatarKey(key) {
		return nil, service.ErrAvatarFileNotFound
	}
	data, err := s.root.ReadFile(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, service.ErrAvatarFileNotFound
	}

	return data, err
}

// temporaryName cannot match a key, so an abandoned file is never served.
func temporaryName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}

	return ".upload-" + hex.EncodeToString(random[:]), nil
}
