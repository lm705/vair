package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The release key at rest.
//
// It used to be the bare private key in base64: whoever could read the file —
// a backup copied somewhere, a synced folder, a program running as the
// maintainer — could sign an update every installed copy would accept. Now it
// is sealed with a passphrase: PBKDF2-SHA256 derives an AES-256-GCM key, and
// the file holds the salt, the nonce and the sealed key. A plain key file is
// still read, so an existing one keeps working until it is converted with
// -encrypt.
//
//	vair-release-key/v2 pbkdf2-sha256 600000
//	<salt, base64>
//	<nonce, base64>
//	<sealed private key, base64>

const (
	sealedMagic = "vair-release-key/v2"
	sealedKDF   = "pbkdf2-sha256"
	// sealedIter is the PBKDF2 work factor for a new file; a file records its
	// own, so raising this later does not lock out an existing key.
	sealedIter = 600000
	// minPassphrase is short enough to remember and long enough that the work
	// factor above, not the passphrase, is what an offline guesser runs into.
	minPassphrase = 12
)

// sealKey encrypts a private key with a passphrase.
func sealKey(priv ed25519.PrivateKey, pass string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := keyCipher(pass, salt, sealedIter)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, priv, []byte(sealedMagic))
	enc := base64.StdEncoding.EncodeToString
	return []byte(fmt.Sprintf("%s %s %d\n%s\n%s\n%s\n", sealedMagic, sealedKDF, sealedIter,
		enc(salt), enc(nonce), enc(sealed))), nil
}

// isSealed says whether a key file is the passphrase-protected kind.
func isSealed(data []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(data)), sealedMagic)
}

// openKey reads a key file of either kind. ask is called for the passphrase
// only when the file is sealed.
func openKey(data []byte, ask func() (string, error)) (ed25519.PrivateKey, error) {
	if !isSealed(data) {
		priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(priv) != ed25519.PrivateKeySize {
			return nil, errors.New("not an Ed25519 private key")
		}
		return ed25519.PrivateKey(priv), nil
	}
	lines := strings.Fields(strings.ReplaceAll(string(data), "\r", ""))
	// magic kdf iter salt nonce sealed
	if len(lines) != 6 || lines[0] != sealedMagic || lines[1] != sealedKDF {
		return nil, errors.New("the sealed key file is damaged")
	}
	iter, err := strconv.Atoi(lines[2])
	if err != nil || iter < 1 {
		return nil, errors.New("the sealed key file is damaged")
	}
	// Strict: the last character of a base64 field also carries a few padding
	// bits, and the lenient decoder ignores them — a file damaged there decoded
	// to the same bytes and opened as if nothing had happened.
	dec := base64.StdEncoding.Strict().DecodeString
	salt, err1 := dec(lines[3])
	nonce, err2 := dec(lines[4])
	sealed, err3 := dec(lines[5])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, errors.New("the sealed key file is damaged")
	}
	pass, err := ask()
	if err != nil {
		return nil, err
	}
	gcm, err := keyCipher(pass, salt, iter)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("the sealed key file is damaged")
	}
	priv, err := gcm.Open(nil, nonce, sealed, []byte(sealedMagic))
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("wrong passphrase, or the key file is damaged")
	}
	return ed25519.PrivateKey(priv), nil
}

func keyCipher(pass string, salt []byte, iter int) (cipher.AEAD, error) {
	k, err := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// stdin is shared by every prompt: a reader per prompt would buffer the next
// answer away when the passphrases arrive through a pipe.
var stdin = bufio.NewReader(os.Stdin)

// askPassphrase reads a passphrase from the terminal without echoing it, or a
// line from standard input when that is not a terminal.
func askPassphrase(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	restore := echoOff()
	line, err := stdin.ReadString('\n')
	restore()
	fmt.Fprintln(os.Stderr)
	if err != nil && line == "" {
		return "", errors.New("no passphrase given")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// askNewPassphrase asks twice and refuses a short one.
func askNewPassphrase() (string, error) {
	p1, err := askPassphrase("new passphrase for the release key: ")
	if err != nil {
		return "", err
	}
	if len([]rune(p1)) < minPassphrase {
		return "", fmt.Errorf("the passphrase must be at least %d characters", minPassphrase)
	}
	p2, err := askPassphrase("the same passphrase again: ")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", errors.New("the two passphrases differ")
	}
	return p1, nil
}
