package ticket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"

	"github.com/google/uuid"
)

// Signer выдаёт и проверяет токены билетов. Токен — id билета и подпись
// HMAC-SHA256 от него: по ссылке нельзя подобрать чужой билет, а проверка
// не требует хранить токен в базе.
type Signer struct {
	key []byte
}

// NewSigner создаёт подписчика с секретом key.
func NewSigner(key string) Signer { return Signer{key: []byte(key)} }

const macLen = 16

// Token возвращает токен билета: base64url(16 байт id + 16 байт подписи).
func (s Signer) Token(ticketID string) string {
	id := uuid.MustParse(ticketID)
	return base64.RawURLEncoding.EncodeToString(append(id[:], s.mac(id[:])...))
}

// Parse проверяет токен и возвращает id билета.
func (s Signer) Parse(token string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != 16+macLen {
		return "", false
	}
	if !hmac.Equal(b[16:], s.mac(b[:16])) {
		return "", false
	}
	id, err := uuid.FromBytes(b[:16])
	if err != nil {
		return "", false
	}
	return id.String(), true
}

func (s Signer) mac(id []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(id)
	return m.Sum(nil)[:macLen]
}
