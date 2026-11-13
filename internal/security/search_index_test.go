package security

import (
	"testing"

	"clipsyncpro/internal/domain"
)

func TestBuildQueryIndexMultiWordSearch(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	index := BuildSearchIndex(secret, "GitHub", "prod-admin@example.com", "核心库")

	query := BuildQueryIndex(secret, "GitHub prod")
	if len(query) == 0 {
		t.Fatal("expected non-empty query hashes for 'GitHub prod'")
	}
	if !domain.ContainsAll(index, query) {
		t.Fatal("expected BuildQueryIndex(secret, 'GitHub prod') to match index built from separate fields")
	}

	query2 := BuildQueryIndex(secret, "prod 核心")
	if !domain.ContainsAll(index, query2) {
		t.Fatal("expected BuildQueryIndex(secret, 'prod 核心') to match index")
	}

	nonMatching := BuildQueryIndex(secret, "GitHub staging")
	if domain.ContainsAll(index, nonMatching) {
		t.Fatal("did not expect 'GitHub staging' to match index")
	}
}
