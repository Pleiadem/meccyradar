//go:build !windows

package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

type localProtector struct{ gcm cipher.AEAD }

func NewPlatformProtector(serviceName string) (Protector, error) {
	if serviceName == "" {
		return nil, errors.New("缺少服务名称")
	}
	seed := sha256.Sum256([]byte("meccyradar-development-protector:" + serviceName))
	block, err := aes.NewCipher(seed[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &localProtector{gcm: gcm}, nil
}

func (p *localProtector) Protect(plain []byte) ([]byte, error) {
	nonce := make([]byte, p.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return p.gcm.Seal(nonce, nonce, plain, nil), nil
}

func (p *localProtector) Unprotect(protected []byte) ([]byte, error) {
	if len(protected) < p.gcm.NonceSize() {
		return nil, errors.New("本地会话数据无效")
	}
	nonce, ciphertext := protected[:p.gcm.NonceSize()], protected[p.gcm.NonceSize():]
	return p.gcm.Open(nil, nonce, ciphertext, nil)
}
