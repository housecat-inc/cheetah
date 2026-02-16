package api

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEncryptDecryptEnv(t *testing.T) {
	tests := []struct {
		_name      string
		app        string
		passphrase string
		vars       map[string]string
	}{
		{
			_name:      "single var",
			app:        "auth",
			passphrase: "team-secret",
			vars:       map[string]string{"DATABASE_URL": "postgres://localhost/auth"},
		},
		{
			_name:      "multiple vars",
			app:        "greet",
			passphrase: "p@ss!",
			vars:       map[string]string{"API_KEY": "abc", "PORT": "8080", "SECRET": "xyz"},
		},
		{
			_name:      "empty vars",
			app:        "app",
			passphrase: "pass",
			vars:       map[string]string{},
		},
		{
			_name:      "unicode passphrase",
			app:        "svc",
			passphrase: "p\u00e4ssw\u00f6rd",
			vars:       map[string]string{"KEY": "value"},
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			blob, err := encryptEnv(tt.app, tt.vars, tt.passphrase)
			a.NoError(err)
			a.True(strings.HasPrefix(blob, blobPrefix))

			app, vars, err := decryptEnv(blob, tt.passphrase)
			a.NoError(err)
			a.Equal(tt.app, app)
			a.Equal(tt.vars, vars)
		})
	}
}

func TestDecryptWrongPassphrase(t *testing.T) {
	a := assert.New(t)

	blob, err := encryptEnv("app", map[string]string{"K": "V"}, "correct")
	a.NoError(err)

	_, _, err = decryptEnv(blob, "wrong")
	a.Error(err)
	a.Contains(err.Error(), "decryption failed")
}

func TestDecryptInvalidBlob(t *testing.T) {
	tests := []struct {
		_name string
		blob  string
		err   string
	}{
		{
			_name: "no prefix",
			blob:  "garbage",
			err:   "invalid blob format",
		},
		{
			_name: "bad base64",
			blob:  "cheetah:v1:!!!!",
			err:   "base64 decode",
		},
		{
			_name: "too short",
			blob:  "cheetah:v1:" + base64.StdEncoding.EncodeToString([]byte("short")),
			err:   "blob too short",
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			_, _, err := decryptEnv(tt.blob, "pass")
			a.ErrorContains(err, tt.err)
		})
	}
}

func TestEncryptProducesDifferentBlobs(t *testing.T) {
	a := assert.New(t)

	blob1, err := encryptEnv("app", map[string]string{"K": "V"}, "pass")
	a.NoError(err)

	blob2, err := encryptEnv("app", map[string]string{"K": "V"}, "pass")
	a.NoError(err)

	a.NotEqual(blob1, blob2)
}
