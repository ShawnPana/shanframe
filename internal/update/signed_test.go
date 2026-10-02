package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testKey(t *testing.T) (pub string, priv ed25519.PrivateKey) {
	t.Helper()
	p, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(p), k
}

const sha = "8fc93593ab53826e2236b0713ec0ecd34a17000cfd14131df691b5d3e7ad6ad0"

// What the server is NOT able to do on its own, however it is compromised.
func TestOnlyASignedReleaseIsAccepted(t *testing.T) {
	pub, priv := testKey(t)
	_, stolenBoxKey := testKey(t) // an attacker's key: they own the server, not the release key
	keys := []string{pub}
	good := Release{Name: "shanframe-linux-arm64", SHA256: sha, Build: "abc1234", Time: 2000}

	if r, err := verifyRelease(SigFile(priv, good), "shanframe-linux-arm64", keys, 1000); err != nil || r != good {
		t.Fatalf("a properly signed release must be accepted: %v", err)
	}
	for name, tc := range map[string]struct {
		file    []byte
		forName string
		running int64
	}{
		"signed by someone else":                 {SigFile(stolenBoxKey, good), good.Name, 1000},
		"another artifact's signature":           {SigFile(priv, good), "shanframe-darwin-arm64", 1000},
		"a genuine but older release (rollback)": {SigFile(priv, good), good.Name, 3000},
		"no signature at all":                    {[]byte(sha + "\n"), good.Name, 1000},
		"empty":                                  {nil, good.Name, 1000},
	} {
		if _, err := verifyRelease(tc.file, tc.forName, keys, tc.running); err == nil {
			t.Fatalf("%s: must be refused", name)
		}
	}
	// the manifest can't be edited after signing: swap the sha, keep the signature
	lines := strings.Fields(string(SigFile(priv, good)))
	evil := good
	evil.SHA256 = strings.Repeat("0", 64)
	forged := strings.Fields(string(SigFile(stolenBoxKey, evil)))[0] + "\n" + lines[1] + "\n"
	if _, err := verifyRelease([]byte(forged), good.Name, keys, 1000); err == nil {
		t.Fatal("a manifest edited after signing must be refused")
	}
	// rotation: a release signed with either pinned key is fine
	pub2, priv2 := testKey(t)
	if _, err := verifyRelease(SigFile(priv2, good), good.Name, []string{pub, pub2}, 1000); err != nil {
		t.Fatalf("second pinned key: %v", err)
	}
}

// Over the wire, as the agent does it: a server with no signature means no
// update; a server with a forged one is refused loudly.
func TestLatestAgainstAServer(t *testing.T) {
	pub, priv := testKey(t)
	_, attacker := testKey(t)
	saved, savedT := releaseKeys, BuildTime
	releaseKeys, BuildTime = []string{pub}, 1000
	defer func() { releaseKeys, BuildTime = saved, savedT }()

	name := Name("shanframe")
	var serve []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bin/"+name+".sig" || serve == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(serve)
	}))
	defer srv.Close()

	if r, err := Latest(srv.URL, "k", "shanframe"); err != nil || r.SHA256 != "" {
		t.Fatalf("no signature on the server means nothing to install: %+v %v", r, err)
	}
	serve = SigFile(priv, Release{Name: name, SHA256: sha, Build: "abc", Time: 2000})
	if r, err := Latest(srv.URL, "k", "shanframe"); err != nil || r.SHA256 != sha {
		t.Fatalf("signed release: %+v %v", r, err)
	}
	serve = SigFile(attacker, Release{Name: name, SHA256: strings.Repeat("e", 64), Build: "evil", Time: 9000})
	if r, err := Latest(srv.URL, "k", "shanframe"); err == nil || r.SHA256 != "" {
		t.Fatalf("a release signed by the wrong key must be refused, got %+v", r)
	}
}

// The key this build pins is a real ed25519 public key (a placeholder or a
// typo here would strand every device on its current build).
func TestPinnedKeyIsWellFormed(t *testing.T) {
	if len(releaseKeys) == 0 {
		t.Fatal("no release key pinned")
	}
	for _, k := range releaseKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			t.Fatalf("pinned key %q is not an ed25519 public key", k)
		}
	}
}
