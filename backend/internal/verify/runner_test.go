package verify

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunAllStatuses(t *testing.T) {
	checks := []Check{
		{ID: "OK", Title: "geçer", Run: func(context.Context, *Env) (string, error) { return "her şey yolunda", nil }},
		{ID: "ERR", Title: "hata verir", Run: func(context.Context, *Env) (string, error) { return "", errors.New("beklenen 200, gelen 500") }},
		{ID: "PANIC", Title: "panic", Run: func(context.Context, *Env) (string, error) { panic("patladı") }},
		{ID: "SLOW", Title: "zaman aşımı", Timeout: 20 * time.Millisecond, Run: func(ctx context.Context, _ *Env) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}},
		{ID: "TODO", Title: "henüz yok", PlannedIn: "B6"},
	}
	results := RunAll(t.Context(), &Env{}, checks, &bytes.Buffer{})

	require.Len(t, results, 5)
	require.Equal(t, Pass, results[0].Status)
	require.Equal(t, Fail, results[1].Status)
	require.Contains(t, results[2].Detail, "patladı", "panic diğer kontrolleri durdurmamalı")
	require.Equal(t, Fail, results[3].Status)
	require.Equal(t, Fail, results[4].Status, "uygulanmamış kimlik FAIL sayılmalı")
	require.Contains(t, results[4].Detail, "B6")
}

func TestPrintTable(t *testing.T) {
	var out bytes.Buffer
	ok := PrintTable(&out, []Result{
		{Check: Check{ID: "HEALTH", Title: "sağlık"}, Status: Pass, Detail: "200", Duration: 12 * time.Millisecond},
		{Check: Check{ID: "F-1", Title: "hesaplar"}, Status: Fail, Detail: "henüz\nuygulanmadı"},
	})
	require.False(t, ok, "bir FAIL varken başarı dönmemeli")
	s := out.String()
	require.Contains(t, s, "HEALTH")
	require.Contains(t, s, "PASS")
	require.Contains(t, s, "henüz uygulanmadı", "çok satırlı açıklama tek satıra inmeli")
	require.Contains(t, s, "Toplam 2 kontrol: 1 PASS, 1 FAIL")

	out.Reset()
	require.True(t, PrintTable(&out, []Result{{Check: Check{ID: "A"}, Status: Pass}}))
}

func TestChecksCoverEverySpecID(t *testing.T) {
	ids := map[string]bool{}
	for _, c := range Checks() {
		require.False(t, ids[c.ID], "tekrarlanan kimlik %s", c.ID)
		ids[c.ID] = true
		require.True(t, c.Run != nil || c.PlannedIn != "", "%s: ne kontrolü ne de planlandığı adım var", c.ID)
	}
	// proje.md §10'daki zorunlu kimlikler ve ek senaryolar.
	for _, id := range []string{
		"F-1", "F-2", "F-3", "F-4", "F-5", "F-6", "F-7", "F-8",
		"T-1", "T-2", "T-3", "T-4", "T-5", "T-6", "T-7",
		"HEALTH", "AUTH-1", "AUTH-2", "AUTH-3", "AUTH-4", "UI",
	} {
		require.True(t, ids[id], "eksik kimlik: %s", id)
	}
}

func TestTamperRoleKeepsSignature(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	token := enc([]byte(`{"alg":"HS256"}`)) + "." + enc([]byte(`{"role":"CUSTOMER","sub":"u1"}`)) + ".imza"

	got, err := tamperRole(token, "AUDITOR")
	require.NoError(t, err)
	parts := strings.Split(got, ".")
	require.Equal(t, "imza", parts[2], "imza değişmemeli (saldırgan anahtarı bilmiyor)")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.Contains(t, string(payload), `"role":"AUDITOR"`)

	_, err = tamperRole("jwt-degil", "AUDITOR")
	require.Error(t, err)
}
