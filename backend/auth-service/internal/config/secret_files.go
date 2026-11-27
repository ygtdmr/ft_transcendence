package config

import (
	"errors"
	"io"
	"os"
	"strings"
)

// maxSecretFileSize caps how much of a secret file is read, so a wrong path
// cannot make startup read a large file.
const maxSecretFileSize = 4 << 10

// secretSetting reads a secret either from the file named by <name>_FILE, the
// way mounted deployment secrets arrive, or from the <name> variable itself for
// local runs. Surrounding whitespace, such as the trailing newline written by
// `openssl rand -hex 32 > file`, is trimmed.
//
// It returns "" when neither variable is set.
//
// Errors name the variable only, never the path or the contents:
//
//   - Both <name> and <name>_FILE are set.
//   - The file cannot be read or is larger than 4096 bytes.
func secretSetting(name string) (string, error) {
	fileVariable := name + "_FILE"
	direct := strings.TrimSpace(os.Getenv(name))
	path := strings.TrimSpace(os.Getenv(fileVariable))
	if path == "" {
		return direct, nil
	}
	if direct != "" {
		return "", errors.New(name + " and " + fileVariable + " must not both be set")
	}

	file, err := os.Open(path)
	if err != nil {
		return "", errors.New(fileVariable + " must name a readable file")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSecretFileSize+1))
	if err != nil || len(raw) > maxSecretFileSize {
		return "", errors.New(fileVariable + " must name a readable file of at most 4096 bytes")
	}

	return strings.TrimSpace(string(raw)), nil
}
