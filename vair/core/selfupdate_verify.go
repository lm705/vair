package core

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// Signature checking for the update manifest.
//
// The manifest names both the download URL and the SHA-256 the download must
// match, so whoever controls the manifest controls what this program executes.
// HTTPS proves who the SERVER is; it says nothing about who wrote the file —
// and the manifest is deliberately mirrored, so "the server" is more than one
// party. A signature moves the trust from the channel to a key, and the key is
// compiled into this binary.
//
// The SHA-256 in a verified manifest then covers the download itself, which is
// why the binary needs no separate signature.

// releasePublicKey is the Ed25519 public key every published manifest is signed
// with, base64. Public by nature — it verifies, it cannot sign.
//
// Empty in a build that has none: verification then refuses everything, which
// is the safe direction. Set it with the key printed by
// `go run ./scripts/sign-manifest -genkey`.
const releasePublicKey = "pvabCv3jC1trB99tvabOk7UP1tGyOsf/lDO0oJr9Tzg="

// errUnverifiedManifest is returned for anything that leaves the manifest
// unproven — a missing signature file, a malformed one, or one that does not
// match. The caller deliberately does NOT tell the user they are under attack:
// the same symptom appears for a few minutes after every release, because
// raw.githubusercontent caches each file separately and can serve a new
// manifest beside the previous signature.
var errUnverifiedManifest = errors.New("update manifest could not be verified")

// verifyManifest checks raw against sigB64 using the built-in release key.
func verifyManifest(raw []byte, sigB64 string) error {
	return verifyManifestWith(releasePublicKey, raw, sigB64)
}

// verifyManifestWith is the testable half: the key is a parameter so a test can
// sign with its own throwaway pair.
func verifyManifestWith(pubB64 string, raw []byte, sigB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		// A build with no usable key cannot verify anything, and must not
		// therefore accept everything.
		return errUnverifiedManifest
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errUnverifiedManifest
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), raw, sig) {
		return errUnverifiedManifest
	}
	return nil
}
