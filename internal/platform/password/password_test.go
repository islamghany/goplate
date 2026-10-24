package password_test

import (
	"strings"
	"testing"

	"github.com/islamghany/goplate/internal/platform/password"
)

func TestHashAndCompare(t *testing.T) {
	h := password.NewHasher(password.Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})

	hash, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected format: %s", hash)
	}
	if other, _ := h.Hash("correct horse battery staple"); other == hash {
		t.Fatal("hashes must be salted")
	}

	if ok, err := h.Compare(hash, "correct horse battery staple"); !ok || err != nil {
		t.Fatalf("Compare(correct) = %v, %v", ok, err)
	}
	if ok, _ := h.Compare(hash, "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := h.Compare("$2a$10$bcrypt-hash", "x"); err == nil {
		t.Fatal("malformed hashes must error")
	}
}

func TestParamsAreReadFromTheHash(t *testing.T) {
	weak := password.NewHasher(password.Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	strong := password.NewHasher(password.DefaultParams)
	hash, _ := weak.Hash("pw")
	// A hasher with different defaults still verifies old hashes, so
	// parameters can be raised without breaking existing users.
	if ok, err := strong.Compare(hash, "pw"); !ok || err != nil {
		t.Fatalf("Compare = %v, %v", ok, err)
	}
}
