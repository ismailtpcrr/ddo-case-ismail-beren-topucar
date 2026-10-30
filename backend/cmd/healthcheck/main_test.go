package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	require.NoError(t, check(t.Context(), srv.URL))

	status = http.StatusServiceUnavailable
	require.ErrorContains(t, check(t.Context(), srv.URL), "503")
}

func TestCheckFailsWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // artık kimse dinlemiyor

	require.Error(t, check(t.Context(), url))
}

func TestCheckRespectsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Error(t, check(ctx, srv.URL))
	require.Less(t, time.Since(start), 2*time.Second, "yanıt vermeyen servis healthcheck'i kilitlememeli")
}
