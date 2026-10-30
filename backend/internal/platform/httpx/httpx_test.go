package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/page"
)

type errResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(t *testing.T, err error) (int, errResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	httpx.WriteError(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil), err)

	var body errResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	return rec.Code, body
}

func TestWriteErrorUsesAppErrorCode(t *testing.T) {
	status, body := writeErr(t, fmt.Errorf("servis: %w", apperr.ErrNotFound.WithMessage("Hesap bulunamadı.")))

	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "NOT_FOUND", body.Error.Code)
	require.Equal(t, "Hesap bulunamadı.", body.Error.Message)
}

func TestWriteErrorHidesUnknownErrors(t *testing.T) {
	status, body := writeErr(t, errors.New(`pq: relation "secret_table" does not exist`))

	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, "INTERNAL", body.Error.Code)
	require.NotContains(t, body.Error.Message, "secret_table", "iç ayrıntı istemciye sızmamalı")
}

func TestWriteErrorMapsDeadline(t *testing.T) {
	status, body := writeErr(t, fmt.Errorf("sorgu: %w", context.DeadlineExceeded))

	require.Equal(t, http.StatusGatewayTimeout, status)
	require.Equal(t, "TIMEOUT", body.Error.Code)
}

type transferInput struct {
	AmountMinor int64  `json:"amount_minor" validate:"gt=0"`
	Currency    string `json:"currency"     validate:"required,oneof=TRY USD EUR"`
	Note        string `json:"note"         validate:"max=5"`
}

func decode(t *testing.T, body string) (transferInput, error) {
	t.Helper()
	var in transferInput
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
	err := httpx.DecodeJSON(httptest.NewRecorder(), req, &in)
	return in, err
}

func TestDecodeJSONValid(t *testing.T) {
	in, err := decode(t, `{"amount_minor": 1500, "currency": "TRY"}`)
	require.NoError(t, err)
	require.Equal(t, transferInput{AmountMinor: 1500, Currency: "TRY"}, in)
}

func TestDecodeJSONRejectsBadInput(t *testing.T) {
	cases := []struct {
		name, body, wantMsg string
	}{
		{"boş gövde", ``, "boş"},
		{"bozuk JSON", `{"amount_minor": `, "geçerli JSON değil"},
		{"yanlış tip", `{"amount_minor": "on", "currency": "TRY"}`, "amount_minor alanının tipi hatalı"},
		{"ondalıklı tutar", `{"amount_minor": 10.5, "currency": "TRY"}`, "amount_minor alanının tipi hatalı"},
		{"bilinmeyen alan", `{"amount_minor": 1, "currency": "TRY", "admin": true}`, `Bilinmeyen alan: "admin"`},
		{"iki nesne", `{"amount_minor": 1, "currency": "TRY"} {}`, "tek bir JSON nesnesi"},
		{"sıfır tutar", `{"amount_minor": 0, "currency": "TRY"}`, "amount_minor 0 değerinden büyük olmalı."},
		{"eksik alan", `{"amount_minor": 1}`, "currency zorunludur."},
		{"izin verilmeyen değer", `{"amount_minor": 1, "currency": "GBP"}`, "currency şunlardan biri olmalı: TRY USD EUR."},
		{"uzun metin", `{"amount_minor": 1, "currency": "TRY", "note": "çok uzun not"}`, "note en fazla 5 karakter olmalı."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decode(t, tc.body)
			require.ErrorIs(t, err, apperr.ErrValidation)
			ae, _ := apperr.As(err)
			require.Contains(t, ae.Message, tc.wantMsg)
		})
	}
}

func TestDecodeJSONRejectsTooLargeBody(t *testing.T) {
	big := `{"currency": "` + strings.Repeat("A", httpx.MaxBodyBytes) + `"}`
	_, err := decode(t, big)
	require.ErrorIs(t, err, apperr.ErrTooLarge)
}

func TestParsePage(t *testing.T) {
	get := func(query string) (page.Request, error) {
		return httpx.ParsePage(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items"+query, nil))
	}

	req, err := get("")
	require.NoError(t, err)
	require.Equal(t, page.Request{Limit: page.DefaultLimit}, req)

	req, err = get("?limit=10&cursor=abc")
	require.NoError(t, err)
	require.Equal(t, page.Request{Limit: 10, Cursor: "abc"}, req)

	for _, bad := range []string{"?limit=0", "?limit=-5", "?limit=201", "?limit=on"} {
		_, err := get(bad)
		require.ErrorIs(t, err, apperr.ErrValidation, bad)
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteJSON(rec, http.StatusCreated, map[string]int64{"amount_minor": 1050})

	require.Equal(t, http.StatusCreated, rec.Code)
	require.JSONEq(t, `{"amount_minor":1050}`, rec.Body.String())
}
