package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/cockroachdb/errors"
	"golang.org/x/crypto/pbkdf2"
)

const (
	blobPrefix = "cheetah:v1:"
	nonceLen   = 12
	pbkdf2Iter = 100_000
	saltLen    = 16
)

type envEnvelope struct {
	App  string            `json:"app"`
	Vars map[string]string `json:"vars"`
}

func encryptEnv(app string, vars map[string]string, passphrase string) (string, error) {
	plaintext, err := json.Marshal(envEnvelope{App: app, Vars: vars})
	if err != nil {
		return "", errors.Wrap(err, "marshal envelope")
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.Wrap(err, "generate salt")
	}

	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, 32, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.Wrap(err, "aes cipher")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.Wrap(err, "gcm")
	}

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.Wrap(err, "generate nonce")
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	blob := make([]byte, 0, saltLen+nonceLen+len(ciphertext))
	blob = append(blob, salt...)
	blob = append(blob, nonce...)
	blob = append(blob, ciphertext...)

	return blobPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

func decryptEnv(blob string, passphrase string) (string, map[string]string, error) {
	if !strings.HasPrefix(blob, blobPrefix) {
		return "", nil, errors.New("invalid blob format")
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(blob, blobPrefix))
	if err != nil {
		return "", nil, errors.Wrap(err, "base64 decode")
	}

	if len(raw) < saltLen+nonceLen+1 {
		return "", nil, errors.New("blob too short")
	}

	salt := raw[:saltLen]
	nonce := raw[saltLen : saltLen+nonceLen]
	ciphertext := raw[saltLen+nonceLen:]

	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, 32, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, errors.Wrap(err, "aes cipher")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, errors.Wrap(err, "gcm")
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", nil, errors.New("decryption failed: wrong passphrase or corrupted data")
	}

	var env envEnvelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		return "", nil, errors.Wrap(err, "unmarshal envelope")
	}

	return env.App, env.Vars, nil
}
