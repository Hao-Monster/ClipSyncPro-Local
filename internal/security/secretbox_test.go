package security

import (
	"encoding/base64"
	"testing"

	"clipsyncpro/internal/domain"
)

func TestEncryptDecryptString(t *testing.T) {
	box, err := EncryptString("secret", "账号密码")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if box.Ciphertext == "账号密码" {
		t.Fatal("ciphertext leaked plaintext")
	}
	got, err := DecryptString("secret", box)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "账号密码" {
		t.Fatalf("got %q", got)
	}
	if _, err := DecryptString("wrong", box); err == nil {
		t.Fatal("expected decrypt failure with wrong secret")
	}
}

func TestDecryptStringRejectsInvalidNonceAndIterationsWithoutPanic(t *testing.T) {
	valid, err := EncryptString("secret", "sensitive-payload")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	for _, badNonce := range [][]byte{
		{},
		[]byte("short"),
		[]byte("0123456789abcdef"), // 16 bytes instead of 12
	} {
		tampered := *valid
		tampered.Nonce = base64.StdEncoding.EncodeToString(badNonce)
		if _, err := DecryptString("secret", &tampered); err == nil {
			t.Fatalf("expected error for nonce of length %d, got nil", len(badNonce))
		}
	}

	for _, badIter := range []int{0, -1, -100, 10_000_001} {
		tampered := *valid
		tampered.Iterations = badIter
		if _, err := DecryptString("secret", &tampered); err == nil {
			t.Fatalf("expected error for iterations=%d, got nil", badIter)
		}
	}
}

func TestSearchIndex(t *testing.T) {
	index := BuildSearchIndex("secret", "alice@example.com", "备注")
	query := BuildQueryIndex("secret", "ali")
	if !domain.ContainsAll(index, query) {
		t.Fatal("expected ali query to match index")
	}
	other := BuildQueryIndex("secret", "bob")
	if domain.ContainsAll(index, other) {
		t.Fatal("did not expect bob query to match")
	}
}

