// Package credutil has helpers for SSH public-key handling.
package credutil

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Fingerprint returns the SSH SHA256 fingerprint of a public key (OpenSSH
// authorized_keys format). Falls back to a hash of the raw string.
func Fingerprint(pub string) string {
	pub = strings.TrimSpace(pub)
	if pub == "" {
		return ""
	}
	if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub)); err == nil {
		return ssh.FingerprintSHA256(key)
	}
	h := sha256.Sum256([]byte(pub))
	return "sha256:" + hex.EncodeToString(h[:])[:16]
}

// Kind attempts to extract the key type (e.g. ssh-ed25519) from the line.
func Kind(pub string) string {
	pub = strings.TrimSpace(pub)
	if i := strings.IndexAny(pub, " \t"); i > 0 {
		return pub[:i]
	}
	return "unknown"
}

// DerivePubFromPriv parses an OpenSSH/PEM private key and returns its canonical
// "type base64" public key line. Returns an error for passphrase-encrypted keys
// (which can't be parsed without the passphrase) or anything that isn't a valid
// private key — callers then ask the user to also paste the public key.
func DerivePubFromPriv(privPEM string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privPEM))
	if err != nil {
		return "", err
	}
	pk := signer.PublicKey()
	return pk.Type() + " " + base64.StdEncoding.EncodeToString(pk.Marshal()), nil
}
