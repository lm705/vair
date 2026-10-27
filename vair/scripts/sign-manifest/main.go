// Command sign-manifest signs version.json with the release key.
//
// The client verifies the manifest against a public key compiled into it (see
// core/selfupdate_verify.go), so a mirror or a hijacked connection can no
// longer decide what the program downloads and runs.
//
//	go run ./scripts/sign-manifest -genkey      # once, ever
//	go run ./scripts/sign-manifest -encrypt     # once, for a key made before 2.7.0
//	go run ./scripts/sign-manifest              # at every release
//
// The private key never enters the repository. It lives at
// %USERPROFILE%\.vair\release.key.enc (or $HOME/.vair/release.key.enc), sealed
// with a passphrase (keyfile.go), or wherever VAIR_RELEASE_KEY points. A plain
// release.key from before 2.7.0 is still read when no sealed one exists.
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
)

func main() {
	genkey := flag.Bool("genkey", false, "create a new release key pair, sealed with a passphrase, and print the public half")
	encrypt := flag.Bool("encrypt", false, "seal an existing plain key file with a passphrase, writing <key>.enc beside it")
	manifest := flag.String("manifest", defaultManifest(), "path to version.json")
	keyPath := flag.String("key", "", "path to the private key (default: VAIR_RELEASE_KEY, then ~/.vair/release.key.enc, then ~/.vair/release.key)")
	flag.Parse()

	switch {
	case *genkey:
		path := *keyPath
		if path == "" {
			path = sealedKeyPath()
		}
		if err := generate(path); err != nil {
			fail(err)
		}
	case *encrypt:
		path := *keyPath
		if path == "" {
			path = plainKeyPath()
		}
		if err := encryptKey(path); err != nil {
			fail(err)
		}
	default:
		path := *keyPath
		if path == "" {
			path = defaultKeyPath()
		}
		if err := sign(*manifest, path); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sign-manifest:", err)
	os.Exit(1)
}

// defaultManifest is the repository root's version.json, one level above dev/.
func defaultManifest() string { return filepath.Join("..", "version.json") }

func keyDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".vair")
}

func plainKeyPath() string  { return filepath.Join(keyDir(), "release.key") }
func sealedKeyPath() string { return filepath.Join(keyDir(), "release.key.enc") }

// defaultKeyPath is the key a release is signed with: VAIR_RELEASE_KEY, else the
// sealed file, else the plain one from before keys were sealed.
func defaultKeyPath() string {
	if p := os.Getenv("VAIR_RELEASE_KEY"); p != "" {
		return p
	}
	if _, err := os.Stat(sealedKeyPath()); err == nil {
		return sealedKeyPath()
	}
	return plainKeyPath()
}

// generate writes a new private key, sealed, and prints the public one.
//
// It refuses to overwrite: a lost release key cannot be recovered, and every
// installed copy of the program trusts only the key it was built with. If this
// file goes, so does the ability to publish an update that anyone will accept.
func generate(keyPath string) error {
	if _, err := os.Stat(keyPath); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite a release key", keyPath)
	}
	pass, err := askNewPassphrase()
	if err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	sealed, err := sealKey(priv, pass)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, sealed, 0o600); err != nil {
		return err
	}
	fmt.Printf("private key written to %s, sealed with the passphrase — back up the file AND the passphrase somewhere offline; neither can be regenerated\n\n", keyPath)
	fmt.Printf("put this in core/selfupdate_verify.go as releasePublicKey:\n\n\t%s\n\n",
		base64.StdEncoding.EncodeToString(pub))
	return nil
}

// encryptKey seals an existing plain key file into <path>.enc. The plain file
// is left where it is: removing it is the maintainer's step, once the sealed
// copy has signed a test manifest and the offline backup exists — a script
// that deleted the only working copy of the key on a typo would end updates
// for good.
func encryptKey(plainPath string) error {
	data, err := os.ReadFile(plainPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	if isSealed(data) {
		return fmt.Errorf("%s is already sealed", plainPath)
	}
	priv, err := openKey(data, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", plainPath, err)
	}
	dst := plainPath + ".enc"
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite a release key", dst)
	}
	pass, err := askNewPassphrase()
	if err != nil {
		return err
	}
	sealed, err := sealKey(priv, pass)
	if err != nil {
		return err
	}
	// Read back before anything is said to be done.
	back, err := openKey(sealed, func() (string, error) { return pass, nil })
	if err != nil || !back.Equal(priv) {
		return fmt.Errorf("the sealed key does not open to the same key — nothing written")
	}
	if err := os.WriteFile(dst, sealed, 0o600); err != nil {
		return err
	}
	fmt.Printf("sealed key written to %s\n", dst)
	fmt.Printf("signing now uses it. Once it has signed a release and the offline backup is in place, delete %s yourself.\n", plainPath)
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
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key (run -genkey once, or set VAIR_RELEASE_KEY): %w", err)
	}
	priv, err := openKey(keyData, func() (string, error) {
		return askPassphrase("passphrase for " + keyPath + ": ")
	})
	if err != nil {
		return fmt.Errorf("%s: %w", keyPath, err)
	}
	if !isSealed(keyData) {
		fmt.Fprintf(os.Stderr, "note: %s is not sealed with a passphrase — run with -encrypt once\n", keyPath)
	}

	sig := ed25519.Sign(priv, raw)
	sigPath := manifestPath + ".sig"
	if err := os.WriteFile(sigPath, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}

	// Verify what was just written, with the public half derived from the
	// private key. A signature that does not check out is worse than none: it
	// looks published and silently blocks every client from updating.
	pub := priv.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, raw, sig) {
		return fmt.Errorf("the signature just written does not verify — do not publish")
	}
	fmt.Printf("signed %s → %s\n", manifestPath, sigPath)
	fmt.Printf("public key: %s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Println("commit BOTH files in one commit — a manifest without its matching signature stops updates.")
	return nil
}
