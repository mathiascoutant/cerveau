// Package cryptoutil chiffre les secrets utilisateurs (mot de passe IMAP, tokens
// Slack/WhatsApp) avant de les écrire en base. Rien de sensible ne doit atterrir
// en clair dans MongoDB.
package cryptoutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
}

// New construit un chiffreur AES-256-GCM à partir d'une clé hexadécimale de 32 octets.
func New(masterKeyHex string) (*Cipher, error) {
	key, err := hex.DecodeString(masterKeyHex)
	if err != nil {
		return nil, errors.New("MASTER_KEY doit être une chaîne hexadécimale (openssl rand -hex 32)")
	}
	if len(key) != 32 {
		return nil, errors.New("MASTER_KEY doit faire 32 octets (64 caractères hexadécimaux)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plain, nil), nil
}

func (c *Cipher) Open(blob []byte) ([]byte, error) {
	if len(blob) < c.aead.NonceSize() {
		return nil, errors.New("secret chiffré invalide")
	}
	nonce, body := blob[:c.aead.NonceSize()], blob[c.aead.NonceSize():]
	return c.aead.Open(nil, nonce, body, nil)
}

// SealJSON sérialise puis chiffre une structure de credentials.
func (c *Cipher) SealJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return c.Seal(raw)
}

// OpenJSON déchiffre puis désérialise vers v.
func (c *Cipher) OpenJSON(blob []byte, v any) error {
	raw, err := c.Open(blob)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
