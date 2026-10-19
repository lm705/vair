// Command sign-manifest signs version.json with the release key.
//
// The client verifies the manifest against a public key compiled into it (see
// core/selfupdate_verify.go), so a mirror or a hijacked connection can no
// longer decide what the program downloads and runs.
//
//	go run ./scripts/sign-manifest -genkey      # once, ever
//	go run ./scripts/sign-manifest              # at every release
//
// The private key never enters the repository. It lives at
// %USERPROFILE%\.vair\release.key (or $HOME/.vair/release.key), or wherever
// VAIR_RELEASE_KEY points.
//
// SIGNING IS NOT OPTIONAL ONCE SHIPPED. From the first release that verifies,
// every later manifest must carry a fresh .sig beside it — publishing
// version.json without re-signing makes every client of that version and newer
// stop seeing updates. Keep this step in the same script that publishes.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	genkey := flag.Bool("genkey", false, "create a new release key pair and print the public half")
	manifest := flag.String("manifest", defaultManifest(), "path to version.json")
	keyPath := flag.String("key", defaultKeyPath(), "path to the private key")
	flag.Parse()

	if *genkey {
		if err := generate(*keyPath); err != nil {
			fail(err)
		}
		return
	}
	if err := sign(*manifest, *keyPath); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sign-manifest:", err)
	os.Exit(1)
}

// defaultManifest is the repository root's version.json, one level above dev/.
func defaultManifest() string { return filepath.Join("..", "version.json") }

func defaultKeyPath() string {
	if p := os.Getenv("VAIR_RELEASE_KEY"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "release.key"
	}
	return filepath.Join(home, ".vair", "release.key")
}

// generate writes a new private key and prints the public one.
//
// It refuses to overwrite: a lost release key cannot be recovered, and every
// installed copy of the program trusts only the key it was built with. If this
// file goes, so does the ability to publish an update that anyone will accept.
func generate(keyPath string) error {
	if _, err := os.Stat(keyPath); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite a release key", keyPath)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("private key written to %s — back it up somewhere offline, it cannot be regenerated\n\n", keyPath)
	fmt.Printf("put this in core/selfupdate_verify.go as releasePublicKey:\n\n\t%s\n\n",
		base64.StdEncoding.EncodeToString(pub))
	return nil
}

// sign writes <manifest>.sig next to the manifest.
//
// The raw bytes of the file are signed, not a re-serialised copy of its
// contents: signing a parsed-and-printed document would mean the signer and the
// verifier must agree on key order, spacing and escaping forever after, and any
// disagreement breaks updates for everyone at once.
func sign(manifestPath, keyPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	keyB64, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key (run -genkey once, or set VAIR_RELEASE_KEY): %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyB64)))
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("%s does not contain an Ed25519 private key", keyPath)
	}

	sig := ed25519.Sign(ed25519.PrivateKey(priv), raw)
	sigPath := manifestPath + ".sig"
	if err := os.WriteFile(sigPath, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}

	// Verify what was just written, with the public half derived from the
	// private key. A signature that does not check out is worse than none: it
	// looks published and silently blocks every client from updating.
	pub := ed25519.PrivateKey(priv).Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, raw, sig) {
		return fmt.Errorf("the signature just written does not verify — do not publish")
	}
	fmt.Printf("signed %s → %s\n", manifestPath, sigPath)
	fmt.Printf("public key: %s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Println("commit BOTH files in one commit — a manifest without its matching signature stops updates.")
	return nil
}
