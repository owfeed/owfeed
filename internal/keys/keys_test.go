package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ground truth, not a self-consistency check: this public key's private half signed an
// index with apk-tools 3.0.5, and apk recorded the identity below in the signature block
// (`# sig v00 h04 364b7138efaa7f7f862f62fd04099d96...`). If IdentityOf ever disagrees
// with this constant, owfeed and apk have diverged on what a key is called.
//
// Hashing the SPKI DER instead of the point would give a406a2ccc40af6dbf8e44f7081f716d7,
// which is the plausible-looking wrong answer this vector exists to rule out.
const (
	testPublicPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEFgwLep+OXSckz1mvibhYO6r4rozZ
U7LEsMLRh/eBKWu8jvKPGIt894ehx0Vto4jLJ7dYW8umERfEbXP2sOPEdg==
-----END PUBLIC KEY-----
`
	testIdentity    = "364b7138efaa7f7f862f62fd04099d96"
	spkiWrongAnswer = "a406a2ccc40af6dbf8e44f7081f716d7"
)

func TestIdentityMatchesApk(t *testing.T) {
	pub, err := LoadPublic([]byte(testPublicPEM))
	if err != nil {
		t.Fatalf("LoadPublic: %v", err)
	}
	id, err := IdentityOf(pub)
	if err != nil {
		t.Fatalf("IdentityOf: %v", err)
	}
	if got := id.String(); got != testIdentity {
		if got == spkiWrongAnswer {
			t.Fatalf("IdentityOf hashed the SPKI DER; apk hashes the uncompressed EC point")
		}
		t.Fatalf("IdentityOf = %s, want %s (as recorded by apk-tools 3.0.5)", got, testIdentity)
	}
}

func TestGenerateRoundTripsAsSEC1(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	privPEM, err := MarshalPrivate(key)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	// The header is the whole point: apk refuses "BEGIN PRIVATE KEY".
	if !strings.HasPrefix(string(privPEM), "-----BEGIN EC PRIVATE KEY-----") {
		t.Fatalf("MarshalPrivate wrote %.32q, want a SEC1 header", privPEM)
	}

	back, err := LoadPrivate(privPEM)
	if err != nil {
		t.Fatalf("LoadPrivate: %v", err)
	}
	if !back.Equal(key) {
		t.Error("LoadPrivate returned a different key")
	}

	pubPEM, err := MarshalPublic(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPublic: %v", err)
	}
	pub, err := LoadPublic(pubPEM)
	if err != nil {
		t.Fatalf("LoadPublic: %v", err)
	}
	if !pub.Equal(&key.PublicKey) {
		t.Error("LoadPublic returned a different key")
	}
}

// A PKCS#8 key is the single most likely way for a user to arrive with an unusable key,
// because `openssl genpkey -algorithm EC` is the form most modern documentation teaches.
// It must be named, not lumped into a generic parse failure — apk's own diagnostic
// ("cryptographic key format not recognized", exit 99) does not hint at the fix.
func TestLoadPrivateRejectsPKCS8(t *testing.T) {
	const pkcs8 = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg0000000000000000
-----END PRIVATE KEY-----
`
	if _, err := LoadPrivate([]byte(pkcs8)); !errors.Is(err, ErrPKCS8) {
		t.Fatalf("LoadPrivate(PKCS#8) = %v, want ErrPKCS8", err)
	}
}

func TestLoadPrivateRejectsJunk(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"not pem", "garbage\n", ErrPEM},
		{"empty", "", ErrPEM},
		{"wrong block type", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", ErrPEMType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadPrivate([]byte(tc.in)); !errors.Is(err, tc.want) {
				t.Fatalf("LoadPrivate = %v, want %v", err, tc.want)
			}
		})
	}
}

// P-384 parses fine as ECDSA and would sail through anything that only checks "is this an
// EC key", then fail inside apk. Reject it here, where the error can be specific.
func TestRejectsWrongCurve(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384: %v", err)
	}

	if _, err := IdentityOf(&p384.PublicKey); !errors.Is(err, ErrCurve) {
		t.Errorf("IdentityOf(P-384) = %v, want ErrCurve", err)
	}

	// And it must not sneak in through the file path either.
	privPEM, err := MarshalPrivate(p384)
	if err != nil {
		t.Fatalf("MarshalPrivate(P-384): %v", err)
	}
	if _, err := LoadPrivate(privPEM); !errors.Is(err, ErrCurve) {
		t.Errorf("LoadPrivate(P-384) = %v, want ErrCurve", err)
	}
}

func TestWritePrivateIs0600AndRefusesClobber(t *testing.T) {
	key, _ := Generate()
	path := filepath.Join(t.TempDir(), "nested", "feed.pem")

	if err := WritePrivate(path, key); err != nil {
		t.Fatalf("WritePrivate: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	// Overwriting a signing key would make every already-published index unverifiable
	// for subscribers who hold the old public key. It must never be a silent success.
	other, _ := Generate()
	if err := WritePrivate(path, other); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("WritePrivate(existing) = %v, want ErrKeyExists", err)
	}
}
