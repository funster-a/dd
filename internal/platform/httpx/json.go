package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxBodyBytes ограничивает тело JSON-запроса: API не принимает больших тел.
const maxBodyBytes = 1 << 20

// WriteJSON отвечает кодом code и телом v в JSON.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Error — единый формат ошибки API: {"error": {"code": "...", "message": "..."}}.
// Code — машинно-читаемый код, по нему фронтенд выбирает текст для пользователя.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError отвечает ошибкой в едином формате.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]Error{"error": {Code: code, Message: message}})
}

// DecodeJSON читает тело запроса в dst: не больше 1 МБ, без неизвестных полей
// и без лишних данных после объекта.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: unexpected data after object")
	}
	return nil
}
