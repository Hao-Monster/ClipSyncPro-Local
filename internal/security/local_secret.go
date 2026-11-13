package security

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	MasterKeyFileName = "master.key"
	masterKeyBytes    = 32
)

// DefaultDataDir returns the local application data directory for ClipSync Pro.
// It honors CLIPSYNC_DATA_DIR first, then %LOCALAPPDATA%\ClipSync Pro, and falls
// back to os.UserCacheDir().
func DefaultDataDir() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("CLIPSYNC_DATA_DIR")); custom != "" {
		return custom, nil
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return filepath.Join(localAppData, "ClipSync Pro"), nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "ClipSync Pro"), nil
}

// ResolveLocalSecret returns CLIPSYNC_SECRET when explicitly configured, or
// loads/generates a persistent 32-byte hex-encoded master key in DefaultDataDir()/master.key.
func ResolveLocalSecret() (string, error) {
	if secret := strings.TrimSpace(os.Getenv("CLIPSYNC_SECRET")); secret != "" {
		return secret, nil
	}
	dir, err := DefaultDataDir()
	if err != nil {
		return "", err
	}
	return ResolveOrCreateSecretInDir(dir)
}

// ResolveOrCreateSecretInDir loads or creates master.key inside dir with 0600 permissions.
func ResolveOrCreateSecretInDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("data directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	keyPath := filepath.Join(dir, MasterKeyFileName)
	data, err := os.ReadFile(keyPath)
	if err == nil {
		if existing := strings.TrimSpace(string(data)); existing != "" {
			return existing, nil
		}
		// Wait briefly in case a concurrent creator just opened the file with O_EXCL and is writing
		for i := 0; i < 10; i++ {
			time.Sleep(2 * time.Millisecond)
			if retryData, retryErr := os.ReadFile(keyPath); retryErr == nil {
				if existing := strings.TrimSpace(string(retryData)); existing != "" {
					return existing, nil
				}
			}
		}
		return "", errors.New("master.key exists but is empty")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	raw := make([]byte, masterKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(raw)
	for i := range raw {
		raw[i] = 0
	}

	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			var lastErr error
			for i := 0; i < 20; i++ {
				retryData, readErr := os.ReadFile(keyPath)
				if readErr == nil {
					if existing := strings.TrimSpace(string(retryData)); existing != "" {
						return existing, nil
					}
				} else {
					lastErr = readErr
				}
				time.Sleep(2 * time.Millisecond)
			}
			if lastErr != nil {
				return "", lastErr
			}
			return "", errors.New("master.key exists but is empty")
		}
		return "", err
	}

	_, writeErr := f.WriteString(secret)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(keyPath)
		return "", writeErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return secret, nil
}

