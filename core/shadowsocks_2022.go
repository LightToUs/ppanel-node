package core

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

func shadowsocks2022KeyLength(cipher string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(cipher)) {
	case "2022-blake3-aes-128-gcm":
		return 16, nil
	case "2022-blake3-aes-256-gcm":
		return 32, nil
	case "2022-blake3-chacha20-poly1305":
		return 0, errors.New(
			"Shadowsocks 2022 ChaCha20 multi-user authentication is not supported by Xray",
		)
	default:
		return 0, fmt.Errorf("unsupported Shadowsocks 2022 cipher %q", cipher)
	}
}

func normalizeShadowsocks2022Key(key string, keyLength int) (string, error) {
	key = strings.TrimSpace(key)
	if decoded, err := base64.StdEncoding.DecodeString(key); err == nil {
		if len(decoded) == keyLength {
			return base64.StdEncoding.EncodeToString(decoded), nil
		}
	}
	if len(key) != keyLength {
		return "", fmt.Errorf(
			"invalid Shadowsocks 2022 key length %d, want %d bytes",
			len(key),
			keyLength,
		)
	}
	return base64.StdEncoding.EncodeToString([]byte(key)), nil
}

func composeShadowsocks2022ClientKey(
	serverKey string,
	credential string,
	cipher string,
) (string, error) {
	keyLength, err := shadowsocks2022KeyLength(cipher)
	if err != nil {
		return "", err
	}
	masterKey, err := normalizeShadowsocks2022Key(serverKey, keyLength)
	if err != nil {
		return "", fmt.Errorf("normalize Shadowsocks 2022 server key: %w", err)
	}
	if len(credential) < keyLength {
		return "", fmt.Errorf(
			"Shadowsocks 2022 credential is %d bytes, want at least %d",
			len(credential),
			keyLength,
		)
	}
	userKey := base64.StdEncoding.EncodeToString([]byte(credential[:keyLength]))
	return masterKey + ":" + userKey, nil
}
