package studio

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const maxMailPayload = 8192

type EmailEnvelope struct {
	ID, Purpose, To, Token string
	ExpiresAt              time.Time
}

type encryptedMail struct {
	ID, Purpose, KeyID string
	Ciphertext         []byte
	ExpiresAt          time.Time
}

type mailPayload struct {
	Version int    `json:"version"`
	To      string `json:"to"`
	Token   string `json:"token"`
	From    string `json:"from"`
	Origin  string `json:"origin"`
}

var mailIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)
var mailTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type mailCipher struct {
	aead  cipher.AEAD
	keyID string
}

func newMailCipher(encoded string) (*mailCipher, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid independent mail encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("mail encryption unavailable")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("mail encryption unavailable")
	}
	digest := sha256.Sum256(key)
	return &mailCipher{aead: aead, keyID: hex.EncodeToString(digest[:16])}, nil
}

func mailAAD(item encryptedMail) []byte {
	return []byte("studio-mail-v1\x00" + item.ID + "\x00" + item.Purpose + "\x00" + item.ExpiresAt.UTC().Format(time.RFC3339Nano))
}

func validMailIdentity(item encryptedMail) bool {
	return mailIDPattern.MatchString(item.ID) && (item.Purpose == "verify_email" || item.Purpose == "reset_password") && !item.ExpiresAt.IsZero()
}

func (m *mailDelivery) seal(e EmailEnvelope) (encryptedMail, error) {
	item := encryptedMail{ID: e.ID, Purpose: e.Purpose, KeyID: m.cipher.keyID, ExpiresAt: e.ExpiresAt.UTC().Truncate(time.Microsecond)}
	if !validMailIdentity(item) || !mailTokenPattern.MatchString(e.Token) {
		return encryptedMail{}, errors.New("invalid security mail envelope")
	}
	if _, err := strictMailbox(e.To); err != nil {
		return encryptedMail{}, errors.New("invalid security mail envelope")
	}
	plain, err := json.Marshal(mailPayload{Version: 1, To: e.To, Token: e.Token, From: m.config.From, Origin: m.config.PublicOrigin})
	if err != nil || len(plain) > maxMailPayload {
		return encryptedMail{}, errors.New("invalid security mail envelope")
	}
	nonce := make([]byte, m.cipher.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return encryptedMail{}, errors.New("mail encryption unavailable")
	}
	item.Ciphertext = m.cipher.aead.Seal(nonce, nonce, plain, mailAAD(item))
	return item, nil
}

func (c *mailCipher) open(item encryptedMail) (mailPayload, error) {
	fail := func() (mailPayload, error) {
		return mailPayload{}, errors.New("encrypted mail cannot be read; retain queue metadata and verify encryption key")
	}
	if !validMailIdentity(item) || item.KeyID != c.keyID || len(item.Ciphertext) > maxMailPayload+64 || len(item.Ciphertext) < c.aead.NonceSize()+c.aead.Overhead() {
		return fail()
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, item.Ciphertext[:n], item.Ciphertext[n:], mailAAD(item))
	if err != nil {
		return fail()
	}
	var p mailPayload
	if json.Unmarshal(plain, &p) != nil || p.Version != 1 || !mailTokenPattern.MatchString(p.Token) {
		return fail()
	}
	if _, err = strictMailbox(p.To); err != nil {
		return fail()
	}
	if _, err = strictMailbox(p.From); err != nil {
		return fail()
	}
	if _, err = mailOrigin(p.Origin); err != nil {
		return fail()
	}
	return p, nil
}
