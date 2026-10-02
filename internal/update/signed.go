package update

// Releases are signed. The server hosts the binaries, but it cannot vouch for
// them: a sha256 served next to a file only proves the download arrived whole.
// Every artifact ships with <artifact>.sig — a manifest (which artifact, its
// sha256, the build and when it was cut) and an ed25519 signature over it,
// made at release time with a key that never touches the server. The agent
// pins the public half, so neither the box, nor its operator's cloud account,
// nor the deploy key can put code on a device. There is no unsigned path.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// releaseKeys are the public keys a release may be signed with (base64).
// More than one only while rotating: add the new key, ship, then drop the old.
var releaseKeys = []string{
	"7e1IJ6+xzeYLA/yVbIegUi/VSsEWirsW/7LURznPLBM=",
}

// BuildTime is when this binary was cut (unix seconds; stamped by the release
// script, 0 for a local build). A release older than the running one is
// refused: replaying a genuine old release is how a fixed hole gets reopened.
var BuildTime int64

// Release is the signed statement about one artifact.
type Release struct {
	Name   string `json:"name"`   // e.g. shanframe-linux-arm64 — a signature for one artifact is no good for another
	SHA256 string `json:"sha256"` // of the binary
	Build  string `json:"build"`  // git revision, for the log
	Time   int64  `json:"time"`   // when it was cut, unix seconds
}

// SigFile renders the .sig file: the manifest and its signature, one base64
// line each. Used by the release signer (scripts/sign.go) and the tests.
func SigFile(priv ed25519.PrivateKey, r Release) []byte {
	m, _ := json.Marshal(r)
	sig := ed25519.Sign(priv, m)
	return []byte(base64.StdEncoding.EncodeToString(m) + "\n" + base64.StdEncoding.EncodeToString(sig) + "\n")
}

// verifyRelease checks a .sig file against the pinned keys and returns the
// release it vouches for — only if it is for this artifact and not older
// than what is running.
func verifyRelease(sigFile []byte, name string, keys []string, runningSince int64) (Release, error) {
	lines := strings.Fields(string(sigFile))
	if len(lines) != 2 {
		return Release{}, errors.New("malformed signature file")
	}
	manifest, err1 := base64.StdEncoding.DecodeString(lines[0])
	sig, err2 := base64.StdEncoding.DecodeString(lines[1])
	if err1 != nil || err2 != nil {
		return Release{}, errors.New("malformed signature file")
	}
	signed := false
	for _, k := range keys {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(pub), manifest, sig) {
			signed = true
			break
		}
	}
	if !signed {
		return Release{}, errors.New("not signed by a shanframe release key")
	}
	var r Release
	if err := json.Unmarshal(manifest, &r); err != nil {
		return Release{}, errors.New("malformed manifest")
	}
	if r.Name != name {
		return Release{}, fmt.Errorf("signed for %q, not %q", r.Name, name)
	}
	if len(r.SHA256) != 64 {
		return Release{}, errors.New("manifest has no sha256")
	}
	if r.Time < runningSince {
		return Release{}, fmt.Errorf("older than the running build (%s < %s)",
			time.Unix(r.Time, 0).UTC().Format("2006-01-02 15:04"), time.Unix(runningSince, 0).UTC().Format("2006-01-02 15:04"))
	}
	return r, nil
}

// Latest asks the server which release it has for our artifact and returns
// it only if the signature holds. (Release{}, nil) when the server has none.
func Latest(server, token, bin string) (Release, error) {
	req, _ := http.NewRequest("GET", server+"/v1/bin/"+Name(bin)+".sig", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, nil
	}
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("server said %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return Release{}, err
	}
	r, err := verifyRelease(b, Name(bin), releaseKeys, BuildTime)
	if err != nil {
		return Release{}, fmt.Errorf("refusing the server's release: %w", err)
	}
	return r, nil
}
