package security

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"clipsyncpro/internal/domain"
)

func TestResolveLocalSecretUsesEnvWhenSet(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_SECRET", "custom-env-secret-key")

	got, err := ResolveLocalSecret()
	if err != nil {
		t.Fatalf("ResolveLocalSecret() error = %v", err)
	}
	if got != "custom-env-secret-key" {
		t.Fatalf("ResolveLocalSecret() = %q, want %q", got, "custom-env-secret-key")
	}

	keyPath := filepath.Join(tempDir, "master.key")
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("expected master.key not to be created when CLIPSYNC_SECRET is set, stat err = %v", err)
	}
}

func TestResolveLocalSecretGeneratesAndPersistsMasterKey(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_SECRET", "")

	first, err := ResolveLocalSecret()
	if err != nil {
		t.Fatalf("first ResolveLocalSecret() error = %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("generated secret length = %d, want 64 hex chars", len(first))
	}
	rawBytes, err := hex.DecodeString(first)
	if err != nil || len(rawBytes) != 32 {
		t.Fatalf("generated secret is not valid 32-byte hex: len=%d, err=%v", len(rawBytes), err)
	}

	keyPath := filepath.Join(tempDir, "master.key")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat master.key: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("master.key file is empty")
	}

	// Simulate restart: calling ResolveLocalSecret again must return the exact same key
	second, err := ResolveLocalSecret()
	if err != nil {
		t.Fatalf("second ResolveLocalSecret() error = %v", err)
	}
	if second != first {
		t.Fatalf("persisted secret mismatch across restart: %q != %q", second, first)
	}

	// Verify AES-256-GCM encryption/decryption and HMAC blind search index work across restarts
	box, err := EncryptString(first, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("EncryptString with local secret: %v", err)
	}
	decrypted, err := DecryptString(second, box)
	if err != nil {
		t.Fatalf("DecryptString with reloaded secret: %v", err)
	}
	if decrypted != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("decrypted 2FA secret = %q, want %q", decrypted, "JBSWY3DPEHPK3PXP")
	}

	idx := BuildSearchIndex(first, "github.com", "developer@example.com", "生产环境账号")
	q := BuildQueryIndex(second, "developer")
	if !domain.ContainsAll(idx, q) {
		t.Fatal("expected HMAC search index built with first load to match query from second load")
	}
}

func TestResolveOrCreateSecretInDirRefusesToOverwriteEmptyKeyFile(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, MasterKeyFileName)
	whitespaceContent := []byte("   \n\t  ")
	if err := os.WriteFile(keyPath, whitespaceContent, 0o600); err != nil {
		t.Fatalf("write whitespace master.key: %v", err)
	}

	got, err := ResolveOrCreateSecretInDir(tempDir)
	if err == nil {
		t.Fatalf("expected error when master.key is whitespace, got secret %q", got)
	}

	after, readErr := os.ReadFile(keyPath)
	if readErr != nil {
		t.Fatalf("ReadFile after failed ResolveOrCreateSecretInDir: %v", readErr)
	}
	if string(after) != string(whitespaceContent) {
		t.Fatalf("master.key was overwritten: got %q, want %q", string(after), string(whitespaceContent))
	}
}

func TestResolveOrCreateSecretInDirFailsWhenKeyPathIsUnreadableDirectory(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, MasterKeyFileName)
	if err := os.MkdirAll(keyPath, 0o700); err != nil {
		t.Fatalf("create directory at master.key path: %v", err)
	}

	got, err := ResolveOrCreateSecretInDir(tempDir)
	if err == nil {
		t.Fatalf("expected error when master.key path is a directory, got secret %q", got)
	}

	info, statErr := os.Stat(keyPath)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("expected master.key directory to remain untouched, err=%v", statErr)
	}
}

func TestResolveOrCreateSecretInDirConcurrentFirstCreation(t *testing.T) {
	tempDir := t.TempDir()
	const workers = 16

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)

	results := make([]string, workers)
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = ResolveOrCreateSecretInDir(tempDir)
		}(i)
	}

	close(start)
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d returned error: %v", i, errs[i])
		}
		if len(results[i]) != 64 {
			t.Fatalf("worker %d returned invalid secret length %d: %q", i, len(results[i]), results[i])
		}
		if results[i] != results[0] {
			t.Fatalf("concurrent secret mismatch: worker %d got %q, worker 0 got %q", i, results[i], results[0])
		}
	}
}

