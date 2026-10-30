package fx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"parakopru/internal/fx"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fixture struct {
	env *dbtest.Env
	svc *fx.Service
	ops auth.Principal
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	svc := fx.NewService(txm, ids.V7{}, clock.System{}, audit.NewRecorder(ids.V7{}))
	require.NoError(t, svc.EnsureDefaults(t.Context(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		map[money.Currency]string{money.USD: "40.0000", money.EUR: "50.0000"}))
	id := uuid.New()
	_, err := env.App.Exec(t.Context(), `INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'x', 'OPERATIONS', 'Kur')`, id, id.String()+"@parakopru.test")
	require.NoError(t, err)
	return fixture{env: env, svc: svc, ops: auth.Principal{UserID: id, Role: auth.RoleOperations}}
}

func (f fixture) convert(t *testing.T, minor int64, from, to money.Currency) fx.Conversion {
	t.Helper()
	c, err := f.svc.Convert(t.Context(), nil, money.New(minor, from), to)
	require.NoError(t, err)
	return c
}

// Dönüşüm decimal ile yapılır ve banker's rounding ile kuruşa yuvarlanır; çapraz kur TL üzerinden hesaplanır.
func TestConvertUsesBankersRoundingAndCrossRates(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, int64(2_50), f.convert(t, 100_00, money.TRY, money.USD).Amount.Minor, "100 TL / 40")
	require.Equal(t, int64(4000_00), f.convert(t, 100_00, money.USD, money.TRY).Amount.Minor)
	require.Equal(t, int64(8_00), f.convert(t, 10_00, money.USD, money.EUR).Amount.Minor, "10 USD × 40/50")
	// 1,00 TL / 40 = 0,025 USD = 2,5 sent → çifte yuvarlanır (2); 1,50 TL → 3,75 sent → 4; 2,50 sent → 2, 3,50 → 4.
	require.Equal(t, int64(2), f.convert(t, 1_00, money.TRY, money.USD).Amount.Minor)
	require.Equal(t, int64(4), f.convert(t, 1_50, money.TRY, money.USD).Amount.Minor)
	require.Equal(t, int64(2), f.convert(t, 1_00, money.TRY, money.USD).Amount.Minor)
	same := f.convert(t, 123, money.TRY, money.TRY)
	require.Equal(t, int64(123), same.Amount.Minor)
	require.True(t, same.Rate.Equal(decimal.NewFromInt(1)))
	require.True(t, f.convert(t, 100, money.USD, money.TRY).Rate.Equal(decimal.NewFromInt(40)), "kayda yazılacak kur: 1 USD = 40 TRY")
	tl, err := f.svc.TRYEquivalentMinor(t.Context(), nil, money.New(300_00, money.USD))
	require.NoError(t, err)
	require.Equal(t, int64(12_000_00), tl)
}

// Zaman geçerliliği: yeni kur eskisini değiştirmez; gelecekteki kur henüz geçerli değildir; geçmiş sorgulanabilir.
func TestRatesAreTimeValidAndAppendOnly(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	_, err := f.svc.Create(t.Context(), f.ops, fx.CreateInput{Base: money.USD, Rate: "45.0000", ValidFrom: &past})
	require.NoError(t, err)
	future := now.Add(time.Hour)
	_, err = f.svc.Create(t.Context(), f.ops, fx.CreateInput{Base: money.USD, Rate: "60.0000", ValidFrom: &future})
	require.NoError(t, err)

	require.True(t, f.convert(t, 100, money.USD, money.TRY).Rate.Equal(decimal.NewFromInt(45)), "şimdi geçerli olan: geçmişteki en yeni; gelecekteki değil")
	usd := money.USD
	twoHoursAgo := now.Add(-2 * time.Hour)
	old, err := f.svc.List(t.Context(), &usd, &twoHoursAgo, 10)
	require.NoError(t, err)
	require.NotEmpty(t, old)
	require.True(t, old[0].Rate.Equal(decimal.NewFromInt(40)), "2 saat önce geçerli olan kur değişmedi")
	all, err := f.svc.List(t.Context(), &usd, nil, 10)
	require.NoError(t, err)
	require.True(t, all[0].Rate.Equal(decimal.NewFromInt(60)), "en yeni geçerlilik önce")
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	none, err := f.svc.List(t.Context(), &usd, &ancient, 10)
	require.NoError(t, err)
	require.Empty(t, none)

	_, err = f.svc.Create(t.Context(), f.ops, fx.CreateInput{Base: money.USD, Rate: "46.0000", ValidFrom: &past})
	require.ErrorIs(t, err, fx.ErrRateExists, "aynı geçerlilik zamanında ikinci kur yok")

	// Kur satırları değiştirilemez ve silinemez (uygulama rolü yetkisiz, sahip rol trigger'a takılır).
	for _, stmt := range []string{`UPDATE fx_rates SET rate = 1`, `DELETE FROM fx_rates`, `TRUNCATE fx_rates`} {
		_, err := f.env.App.Exec(t.Context(), stmt)
		require.ErrorContains(t, err, "permission denied", stmt)
		_, err = f.env.Owner.Exec(t.Context(), stmt)
		require.ErrorContains(t, err, "eklemeli-yalnızdır", stmt)
	}
}

func TestRateValidation(t *testing.T) {
	f := newFixture(t)
	for _, bad := range []string{"", "0", "-1", "abc", "1e3", "41,5", " ", "1.123456789", "2000000", "+5"} {
		_, err := f.svc.Create(t.Context(), f.ops, fx.CreateInput{Base: money.USD, Rate: bad})
		require.ErrorIs(t, err, fx.ErrInvalidRate, "%q", bad)
	}
}

// Property: dönüşüm sonucu, tam değerden en fazla yarım kuruş sapar ve negatif olmaz.
func TestPropertyConversionWithinHalfMinorUnit(t *testing.T) {
	f := newFixture(t)
	rapid.Check(t, func(rt *rapid.T) {
		minor := rapid.Int64Range(0, 1_000_000_000_00).Draw(rt, "tutar")
		pair := rapid.SampledFrom([][2]money.Currency{{money.TRY, money.USD}, {money.USD, money.TRY}, {money.USD, money.EUR}, {money.EUR, money.TRY}}).Draw(rt, "çift")
		c, err := f.svc.Convert(context.Background(), nil, money.New(minor, pair[0]), pair[1])
		if err != nil {
			rt.Fatalf("dönüşüm: %v", err)
		}
		exact := money.New(minor, pair[0]).Decimal().Mul(c.Rate)
		diff := c.Amount.Decimal().Sub(exact).Abs()
		if c.Amount.Minor < 0 || diff.GreaterThan(decimal.RequireFromString("0.005")) {
			rt.Fatalf("sapma %s (tutar %d %s → %s, kur %s)", diff, minor, pair[0], pair[1], c.Rate)
		}
	})
}

func TestHandlers(t *testing.T) {
	f := newFixture(t)
	h := fx.NewHandler(f.svc)
	r := chi.NewRouter()
	r.Post("/fx/rates", h.Create)
	r.Get("/fx/rates", h.List)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), f.ops), method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	rec := call(http.MethodPost, "/fx/rates", `{"base":"EUR","rate":"52.2500"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var out fx.Rate
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "52.25", out.Rate.String())
	require.Contains(t, rec.Body.String(), `"rate":"52.25"`, "kur JSON'da metin (kayan nokta yok)")
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/fx/rates", `{"base":"USD","rate":41.5}`).Code, "sayı değil metin beklenir")
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/fx/rates", `{"base":"TRY","rate":"1"}`).Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/fx/rates", `{"base":"USD","quote":"EUR","rate":"1"}`).Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/fx/rates", `{"base":"USD","rate":"-3"}`).Code)
	rec = call(http.MethodGet, "/fx/rates?base=EUR&limit=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "52.25")
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/fx/rates?base=GBP", "").Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/fx/rates?as_of=dün", "").Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/fx/rates?limit=0", "").Code)
	var ae *apperr.Error
	require.ErrorAs(t, fx.ErrRateUnavailable, &ae)
	require.Equal(t, 422, ae.Status)
}
