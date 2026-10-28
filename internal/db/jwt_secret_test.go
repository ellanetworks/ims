package db

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestJWTSecretSurvivesReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ims.db")

	var secrets [][]byte

	for range 2 {
		d, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}

		for range 2 {
			secret, err := d.JWTSecret(ctx)
			if err != nil {
				t.Fatalf("JWTSecret: %v", err)
			}

			secrets = append(secrets, secret)
		}

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if len(secrets[0]) != jwtSecretLength {
		t.Fatalf("secret is %d bytes, want %d", len(secrets[0]), jwtSecretLength)
	}

	for _, s := range secrets[1:] {
		if !bytes.Equal(s, secrets[0]) {
			t.Fatal("JWTSecret changed")
		}
	}

	other, err := openTestDB(t).JWTSecret(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(other, secrets[0]) {
		t.Fatal("two databases have the same JWT secret")
	}
}
