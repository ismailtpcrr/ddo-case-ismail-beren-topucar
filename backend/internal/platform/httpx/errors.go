package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/observability"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError hatayı şartnamedeki {"error":{"code","message"}} biçiminde yazar.
//
// apperr.Error ise kendi kodu ve durumu kullanılır. Değilse (beklenmeyen hata) 500 INTERNAL döner;
// ayrıntı yalnız loga yazılır, istemciye iç bilgi (SQL, yığın, dosya yolu) sızmaz.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := apperr.As(err)
	switch {
	case ok:
	case errors.Is(err, context.DeadlineExceeded):
		ae = apperr.ErrTimeout
	default:
		ae = apperr.ErrInternal
	}
	if ae.Status >= http.StatusInternalServerError {
		observability.Logger(r.Context()).Error("request failed",
			slog.String("code", ae.Code), slog.Any("error", err))
	}
	writeAppError(w, ae)
}

func writeAppError(w http.ResponseWriter, ae *apperr.Error) {
	WriteJSON(w, ae.Status, errorBody{Error: errorDetail{Code: ae.Code, Message: ae.Message}})
}
