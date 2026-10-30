package sim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/sim"
)

type countingNotifier struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *countingNotifier) Notify(_ context.Context, n sim.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[n.Reference]++
	return nil
}

func newBank(t *testing.T, seed int64, n sim.Notifier) *httptest.Server {
	t.Helper()
	b := sim.NewBank(sim.BankConfig{Seed: seed, FailurePercent: 20, DuplicatePercent: 10, Hang: time.Millisecond}, n, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	return srv
}

var goodIBAN = account.NewIBAN(12345)

// send bankaya gönderir; (HTTP durumu, hata) döndürür. Bağlantı kesilmesi durum 0 + hatadır (zaman aşımı).
func send(srv *httptest.Server, ref, iban string) (int, error) {
	body, _ := json.Marshal(sim.SendRequest{Reference: ref, AmountMinor: 100, Currency: "TRY", BeneficiaryIBAN: iban})
	resp, err := http.Post(srv.URL+"/v1/transfers", "application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func status(t *testing.T, srv *httptest.Server, ref string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v1/transfers/" + ref) //nolint:noctx // test
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var s struct{ Status string }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&s))
	return s.Status
}

// Şartname: ~%20 hata. Hatalar dört türe bölünür; ikisi "işlendi mi bilmiyoruz" (belirsiz sonuç) üretir.
func TestBankFailureRateAndUncertainOutcomes(t *testing.T) {
	srv := newBank(t, 1, nil)
	const n = 3000
	var failed, uncertain, lostBeforeProcessing int
	for i := range n {
		ref := fmt.Sprintf("ref-%d", i)
		code, err := send(srv, ref, goodIBAN)
		if err == nil && code == http.StatusOK {
			continue
		}
		failed++
		switch status(t, srv, ref) {
		case "COMPLETED":
			uncertain++ // hata döndü ama banka işlemişti
		case "NOT_FOUND":
			lostBeforeProcessing++
		}
	}
	require.InDelta(t, 0.20, float64(failed)/n, 0.03)
	require.Positive(t, uncertain, "işlendiği halde hata/zaman aşımı dönen çağrılar olmalı")
	require.Positive(t, lostBeforeProcessing, "işlenmeden hata dönen çağrılar olmalı")
	require.Equal(t, failed, uncertain+lostBeforeProcessing)
}

// Aynı referansla tekrar gönderim yeni işlem yapmaz; sonunda her referans işlenir ve durumu COMPLETED kalır.
func TestBankIsIdempotentPerReference(t *testing.T) {
	srv := newBank(t, 2, nil)
	for i := range 200 {
		ref := fmt.Sprintf("idem-%d", i)
		var ok bool
		for range 30 { // istemci başarılı cevap alana kadar yeniden dener
			if code, err := send(srv, ref, goodIBAN); err == nil && code == http.StatusOK {
				ok = true
				break
			}
		}
		require.True(t, ok, ref)
		require.Equal(t, "COMPLETED", status(t, srv, ref))
	}
	require.Equal(t, "NOT_FOUND", status(t, srv, "hiç-gönderilmedi"))
}

func TestBankRejectsInvalidBeneficiary(t *testing.T) {
	srv := newBank(t, 3, nil)
	for range 30 {
		if code, err := send(srv, "bad-iban", "TR000000000000000000000000"); err == nil && code == http.StatusOK {
			break
		}
	}
	require.Equal(t, "REJECTED", status(t, srv, "bad-iban"))
}

// Şartname: ~%10 çift cevap. Sonuç bildirimi bazı transferlerde iki kez gelir.
func TestBankSendsDuplicateNotifications(t *testing.T) {
	cn := &countingNotifier{n: map[string]int{}}
	srv := newBank(t, 4, cn)
	const n = 2000
	for i := range n {
		for range 30 {
			if code, err := send(srv, fmt.Sprintf("dup-%d", i), goodIBAN); err == nil && code == http.StatusOK {
				break
			}
		}
	}
	require.Eventually(t, func() bool {
		cn.mu.Lock()
		defer cn.mu.Unlock()
		return len(cn.n) == n
	}, 10*time.Second, 50*time.Millisecond, "her işlenen transfer için bildirim gelmeli")
	time.Sleep(200 * time.Millisecond)
	cn.mu.Lock()
	defer cn.mu.Unlock()
	var dups int
	for _, c := range cn.n {
		require.LessOrEqual(t, c, 2)
		if c == 2 {
			dups++
		}
	}
	require.InDelta(t, 0.10, float64(dups)/n, 0.03)
}

// Aynı tohum aynı davranış: iki banka örneği aynı isteklere aynı kaderi yaşatır.
func TestBankIsDeterministicPerSeed(t *testing.T) {
	run := func(seed int64) []int {
		srv := newBank(t, seed, nil)
		out := make([]int, 300)
		for i := range out {
			code, _ := send(srv, fmt.Sprintf("d-%d", i), goodIBAN)
			out[i] = code
		}
		return out
	}
	require.Equal(t, run(9), run(9))
	require.NotEqual(t, run(9), run(10))
}
