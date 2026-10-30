package transfer_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/account"
	"parakopru/internal/bank"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/audit"
	"parakopru/internal/transfer"
)

// fakeBank testlerde bankanın cevaplarını elle yönetir.
type fakeBank struct {
	mu      sync.Mutex
	send    func(bank.SendRequest) (bank.Status, error)
	inquire func(string) (bank.Status, error)
	sends   atomic.Int32
}

func (b *fakeBank) Send(_ context.Context, r bank.SendRequest) (bank.Status, error) {
	b.sends.Add(1)
	b.mu.Lock()
	f := b.send
	b.mu.Unlock()
	return f(r)
}

func (b *fakeBank) Inquire(_ context.Context, ref string) (bank.Status, error) {
	b.mu.Lock()
	f := b.inquire
	b.mu.Unlock()
	return f(ref)
}

func (b *fakeBank) set(send func(bank.SendRequest) (bank.Status, error), inquire func(string) (bank.Status, error)) {
	b.mu.Lock()
	b.send, b.inquire = send, inquire
	b.mu.Unlock()
}

func always(st bank.Status) func(bank.SendRequest) (bank.Status, error) {
	return func(bank.SendRequest) (bank.Status, error) { return st, nil }
}

func inquiring(st bank.Status, err error) func(string) (bank.Status, error) {
	return func(string) (bank.Status, error) { return st, err }
}

var errTimeout = errors.New("bank: outcome uncertain: timeout")

// extFixture dış transfer denemesi için bakiyeli bir müşteri ve dış IBAN hazırlar; takas ve nostro bakiyelerini ölçer.
type extFixture struct {
	fixture
	ctx       context.Context
	from      account.Account
	clearing  uuid.UUID
	nostro    uuid.UUID
	clearing0 int64
	nostro0   int64
}

func newExt(t *testing.T) (extFixture, uuid.UUID) {
	t.Helper()
	f := newFixture(t)
	_, from := f.owned(t, money.TRY, 1_000_00)
	e := extFixture{fixture: f, ctx: audit.WithSystemActor(t.Context()), from: from}
	var err error
	e.clearing, err = f.accounts.InternalAccountID(t.Context(), nil, account.ExternalClearing, money.TRY)
	require.NoError(t, err)
	e.nostro, err = f.accounts.InternalAccountID(t.Context(), nil, account.Nostro, money.TRY)
	require.NoError(t, err)
	e.clearing0, e.nostro0 = f.balance(t, e.clearing), f.balance(t, e.nostro)
	return e, from.ID
}

// create dış IBAN'a transfer başlatır (sahibi olarak).
func (e extFixture) create(t *testing.T, key string, minor int64) transfer.Transfer {
	t.Helper()
	tr, _, err := e.transfer.Create(t.Context(), e.ops, key, transfer.CreateInput{
		FromAccountID: e.from.ID, ToIBAN: account.NewIBAN(900_000_000), AmountMinor: minor, Currency: money.TRY, Type: transfer.EFT})
	require.NoError(t, err)
	return tr
}

func (e extFixture) state(t *testing.T, id uuid.UUID) transfer.Detail {
	t.Helper()
	d, err := e.transfer.Get(t.Context(), e.ops, id)
	require.NoError(t, err)
	return d
}

func states(d transfer.Detail) []transfer.State {
	out := make([]transfer.State, len(d.Events))
	for i, ev := range d.Events {
		out[i] = ev.To
	}
	return out
}

func (e extFixture) requireInvariants(t *testing.T) {
	t.Helper()
	inv, err := e.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
}

// Mutlu yol: ayırma → gönderim → banka onayı → takas kapanır, nostro azalır.
func TestExternalTransferCompletes(t *testing.T) {
	e, _ := newExt(t)
	e.bank.set(always(bank.Completed), inquiring(bank.Completed, nil))
	tr := e.create(t, "ext-ok", 250_00)

	require.Equal(t, transfer.Reserved, tr.State)
	require.Equal(t, transfer.EFT, tr.Type)
	require.Nil(t, tr.ToAccountID)
	require.Equal(t, int64(750_00), e.balance(t, e.from.ID), "para müşteriden hemen ayrılır")
	require.Equal(t, e.clearing0+250_00, e.balance(t, e.clearing), "takas hesabında bekler")
	var jobsQueued int
	require.NoError(t, e.env.App.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'transfer.send' AND payload->>'transfer_id' = $1`, tr.ID.String()).Scan(&jobsQueued))
	require.Equal(t, 1, jobsQueued, "outbox: gönderim işi ayırmayla birlikte kuyruğa girdi")

	require.NoError(t, e.transfer.ProcessSend(e.ctx, tr.ID))
	d := e.state(t, tr.ID)
	require.Equal(t, transfer.Completed, d.State)
	require.Equal(t, []transfer.State{transfer.Created, transfer.Reserved, transfer.Sent, transfer.Completed}, states(d))
	require.Equal(t, e.clearing0, e.balance(t, e.clearing), "takas kapandı")
	require.Equal(t, e.nostro0-250_00, e.balance(t, e.nostro), "para muhabir bankadan çıktı")
	require.Equal(t, int64(750_00), e.balance(t, e.from.ID))
	require.NotNil(t, d.SettlementEntryID)
	e.requireInvariants(t)

	// İş tekrar çalışsa (kuyruk en az bir kez çalıştırır) hiçbir şey değişmez.
	require.NoError(t, e.transfer.ProcessSend(e.ctx, tr.ID))
	require.Equal(t, int32(1), e.bank.sends.Load())
	require.Equal(t, e.nostro0-250_00, e.balance(t, e.nostro))
}

// Banka reddederse telafi: ters kayıt, para müşteriye döner, takas sıfırlanır.
func TestExternalTransferRejectedIsReversed(t *testing.T) {
	e, _ := newExt(t)
	e.bank.set(always(bank.Rejected), nil)
	tr := e.create(t, "ext-rej", 100_00)
	require.NoError(t, e.transfer.ProcessSend(e.ctx, tr.ID))

	d := e.state(t, tr.ID)
	require.Equal(t, transfer.Reversed, d.State)
	require.Equal(t, []transfer.State{transfer.Created, transfer.Reserved, transfer.Sent, transfer.Failed, transfer.Reversed}, states(d))
	require.Equal(t, int64(1_000_00), e.balance(t, e.from.ID), "para geri döndü")
	require.Equal(t, e.clearing0, e.balance(t, e.clearing))
	require.Equal(t, e.nostro0, e.balance(t, e.nostro), "nostro hiç hareket etmedi")
	require.NotNil(t, d.ReversalEntryID)
	require.Equal(t, "BANK_REJECTED", *d.FailureCode)
	// Özgün ayırma kaydı silinmedi; ters kayıtla düzeltildi.
	orig, err := e.ledger.Entry(t.Context(), *d.JournalEntryID)
	require.NoError(t, err)
	require.Equal(t, d.ReversalEntryID, orig.ReversedBy)
	e.requireInvariants(t)
}

// Belirsiz sonuç: para tahmine göre hareket etmez; UNKNOWN'da ayrılmış kalır, sorgu kesin cevap verince çözülür.
func TestUncertainOutcomeIsQueriedThenCompleted(t *testing.T) {
	e, _ := newExt(t)
	e.bank.set(func(bank.SendRequest) (bank.Status, error) { return "", errTimeout }, inquiring("", errTimeout))
	tr := e.create(t, "ext-unk", 60_00)

	err := e.transfer.ProcessSend(e.ctx, tr.ID)
	require.Error(t, err, "iş yeniden denenmek üzere hata döner")
	require.Equal(t, transfer.Unknown, e.state(t, tr.ID).State)
	require.Equal(t, int64(940_00), e.balance(t, e.from.ID), "belirsizken para ayrılmış kalır")
	require.Equal(t, e.clearing0+60_00, e.balance(t, e.clearing))

	// Sorgulama da başarısız: UNKNOWN kalır, yeniden denenir.
	require.Error(t, e.transfer.ProcessSend(e.ctx, tr.ID))
	require.Equal(t, transfer.Unknown, e.state(t, tr.ID).State)

	// Sorgu "işlenmişti" der → tamamlanır; yeniden gönderim yapılmaz.
	sends := e.bank.sends.Load()
	e.bank.set(nil, inquiring(bank.Completed, nil))
	require.NoError(t, e.transfer.ProcessSend(e.ctx, tr.ID))
	d := e.state(t, tr.ID)
	require.Equal(t, transfer.Completed, d.State)
	require.Equal(t, []transfer.State{transfer.Created, transfer.Reserved, transfer.Sent, transfer.Unknown, transfer.Completed}, states(d))
	require.Equal(t, sends, e.bank.sends.Load())
	require.Equal(t, e.nostro0-60_00, e.balance(t, e.nostro))
	e.requireInvariants(t)
}

// Banka hiç işlememişse (NOT_FOUND) telafi edilir: ters kayıt.
func TestUncertainOutcomeNotFoundIsCompensated(t *testing.T) {
	e, _ := newExt(t)
	e.bank.set(func(bank.SendRequest) (bank.Status, error) { return "", errTimeout }, inquiring(bank.NotFound, nil))
	tr := e.create(t, "ext-nf", 70_00)
	require.Error(t, e.transfer.ProcessSend(e.ctx, tr.ID))
	require.NoError(t, e.transfer.ProcessSend(e.ctx, tr.ID))

	d := e.state(t, tr.ID)
	require.Equal(t, transfer.Reversed, d.State)
	require.Equal(t, int64(1_000_00), e.balance(t, e.from.ID))
	require.Equal(t, e.clearing0, e.balance(t, e.clearing))
	e.requireInvariants(t)
}

// Çift cevap: aynı bildirim iki kez, geç gelen çelişkili bildirim, tutar uyuşmazlığı, bilinmeyen referans.
func TestWebhookDuplicatesAndMismatch(t *testing.T) {
	e, _ := newExt(t)
	tr := e.create(t, "ext-wh", 40_00)
	res := transfer.BankResult{Reference: tr.ID.String(), Status: "COMPLETED", AmountMinor: 40_00, Currency: "TRY"}

	require.NoError(t, e.transfer.ApplyBankResult(t.Context(), res))
	require.Equal(t, transfer.Completed, e.state(t, tr.ID).State, "bildirim gönderim kaydından önce geldi: RESERVED → SENT → COMPLETED")
	require.NoError(t, e.transfer.ApplyBankResult(t.Context(), res), "çift cevap sessizce yok sayılır")
	late := res
	late.Status = "REJECTED"
	require.NoError(t, e.transfer.ApplyBankResult(t.Context(), late), "sonuçlanmış transfere gelen çelişkili bildirim para hareketi yapmaz")
	d := e.state(t, tr.ID)
	require.Equal(t, transfer.Completed, d.State)
	require.Equal(t, []transfer.State{transfer.Created, transfer.Reserved, transfer.Sent, transfer.Completed}, states(d))
	require.Equal(t, e.nostro0-40_00, e.balance(t, e.nostro), "tutar yalnız bir kez nostrodan çıktı")

	wrong := res
	wrong.AmountMinor = 41_00
	require.ErrorIs(t, e.transfer.ApplyBankResult(t.Context(), wrong), transfer.ErrBankMismatch)
	wrong = res
	wrong.Reference = uuid.NewString()
	require.ErrorIs(t, e.transfer.ApplyBankResult(t.Context(), wrong), transfer.ErrNotFound)
	e.requireInvariants(t)
}

// Worker ve bildirimler aynı anda sonuçlandırmaya çalışır: transfer satırı kilidi sayesinde yalnız biri işler.
func TestConcurrentSettlementHappensOnce(t *testing.T) {
	e, _ := newExt(t)
	e.bank.set(always(bank.Completed), inquiring(bank.Completed, nil))
	tr := e.create(t, "ext-race", 30_00)
	res := transfer.BankResult{Reference: tr.ID.String(), Status: "COMPLETED", AmountMinor: 30_00, Currency: "TRY"}

	var g errgroup.Group
	for i := range 12 {
		g.Go(func() error {
			if i%2 == 0 {
				return e.transfer.ProcessSend(e.ctx, tr.ID)
			}
			return e.transfer.ApplyBankResult(t.Context(), res)
		})
	}
	require.NoError(t, g.Wait())
	require.Equal(t, transfer.Completed, e.state(t, tr.ID).State)
	require.Equal(t, e.nostro0-30_00, e.balance(t, e.nostro), "tek tahsilat")
	require.Equal(t, e.clearing0, e.balance(t, e.clearing))
	e.requireInvariants(t)
}

func TestExternalInsufficientFundsIsRejectedWithoutJob(t *testing.T) {
	e, _ := newExt(t)
	tr := e.create(t, "ext-poor", 5_000_00)
	require.Equal(t, transfer.Rejected, tr.State)
	require.Equal(t, int64(1_000_00), e.balance(t, e.from.ID))
	var n int
	require.NoError(t, e.env.App.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'transfer.send' AND payload->>'transfer_id' = $1`, tr.ID.String()).Scan(&n))
	require.Zero(t, n)
}

func TestSettleRefusesInternalTransfers(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 10_00)
	_, to := f.owned(t, money.TRY, 0)
	tr, _, err := f.transfer.Create(t.Context(), p, "int", in(from, to, 1_00))
	require.NoError(t, err)
	err = f.transfer.ApplyBankResult(t.Context(), transfer.BankResult{Reference: tr.ID.String(), Status: "COMPLETED", AmountMinor: 1_00, Currency: "TRY"})
	require.ErrorIs(t, err, transfer.ErrNotExternal)
	require.Equal(t, transfer.Completed, tr.State)
}

// Gelen dış transfer: alıcı alacaklanır, nostro borçlanır; banka aynı bildirimi iki kez gönderse de bir kez yazılır.
func TestIncomingTransferIsIdempotentByBankReference(t *testing.T) {
	f := newFixture(t)
	_, to := f.owned(t, money.TRY, 0)
	nostro, err := f.accounts.InternalAccountID(t.Context(), nil, account.Nostro, money.TRY)
	require.NoError(t, err)
	n0 := f.balance(t, nostro)
	in := transfer.IncomingTransfer{Reference: "bank-" + uuid.NewString(), BeneficiaryIBAN: *to.IBAN, AmountMinor: 55_00, Currency: "TRY"}

	dup, err := f.transfer.ReceiveIncoming(audit.WithSystemActor(t.Context()), in)
	require.NoError(t, err)
	require.False(t, dup)
	dup, err = f.transfer.ReceiveIncoming(audit.WithSystemActor(t.Context()), in)
	require.NoError(t, err)
	require.True(t, dup)
	require.Equal(t, int64(55_00), f.balance(t, to.ID))
	require.Equal(t, n0+55_00, f.balance(t, nostro), "nostro (varlık) borçlanınca artar")

	bad := in
	bad.Reference, bad.BeneficiaryIBAN = "x-"+uuid.NewString(), account.NewIBAN(888_888_888)
	_, err = f.transfer.ReceiveIncoming(audit.WithSystemActor(t.Context()), bad)
	require.ErrorIs(t, err, transfer.ErrBeneficiary)
	bad = in
	bad.Reference, bad.Currency = "y-"+uuid.NewString(), "USD"
	_, err = f.transfer.ReceiveIncoming(audit.WithSystemActor(t.Context()), bad)
	require.ErrorIs(t, err, apperr.New(0, "CURRENCY_MISMATCH", ""))
	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK)
}
