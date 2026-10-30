package transfer_test

// Dört göz onayı (proje.md Görev 7): 10.000 TL üzeri transferi isteyen onaylayamaz.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/account"
	"parakopru/internal/bank"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/page"
	"parakopru/internal/transfer"
)

type approvalFixture struct {
	fixture
	customer auth.Principal // transferi isteyen müşteri
	from, to account.Account
	approver auth.Principal // uyum uzmanı
	ops2     auth.Principal // ikinci operasyon uzmanı
}

func newApproval(t *testing.T, balance int64) approvalFixture {
	t.Helper()
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, balance)
	_, to := f.owned(t, money.TRY, 0)
	return approvalFixture{fixture: f, customer: p, from: from, to: to,
		approver: newUser(t, f.env, auth.RoleCompliance), ops2: newUser(t, f.env, auth.RoleOperations)}
}

func (a approvalFixture) request(t *testing.T, p auth.Principal, key string, minor int64) transfer.Transfer {
	t.Helper()
	tr, _, err := a.transfer.Create(t.Context(), p, key, in(a.from, a.to, minor))
	require.NoError(t, err)
	return tr
}

// Eşik tam 10.000,00 TL'dir ve "üzeri" onay ister: 10.000,00 hemen tamamlanır, 10.000,01 onay bekler.
func TestApprovalThresholdIsStrictlyAbove(t *testing.T) {
	a := newApproval(t, 30_000_00)
	exact := a.request(t, a.customer, "k-exact", 10_000_00)
	require.Equal(t, transfer.Completed, exact.State, "10.000,00 TL onay istemez")
	over := a.request(t, a.customer, "k-over", 10_000_01)
	require.Equal(t, transfer.PendingApproval, over.State)
	require.Equal(t, int64(20_000_00), a.balance(t, a.from.ID), "onay beklerken para bloke edilmez / düşmez")
	require.Zero(t, a.balance(t, a.to.ID)-10_000_00)
	require.Nil(t, over.JournalEntryID, "defter kaydı yok")
	var jobs int
	require.NoError(t, a.env.App.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE payload->>'transfer_id' = $1`, over.ID.String()).Scan(&jobs))
	require.Zero(t, jobs)
}

func TestFourEyesApprovalExecutesTransfer(t *testing.T) {
	a := newApproval(t, 30_000_00)
	tr := a.request(t, a.customer, "k1", 15_000_00)
	require.Equal(t, transfer.PendingApproval, tr.State)

	// İsteyen (müşteri) onaylayamaz: izin ayrıca router'da yok; servis düzeyinde de kural çalışır.
	_, _, err := a.transfer.Approve(t.Context(), a.customer, "ap-self", tr.ID)
	require.ErrorIs(t, err, transfer.ErrFourEyes)
	require.Equal(t, transfer.PendingApproval, a.mustGet(t, tr.ID).State, "reddedilen onay durumu değiştirmez")

	got, _, err := a.transfer.Approve(t.Context(), a.approver, "ap-1", tr.ID)
	require.NoError(t, err)
	require.Equal(t, transfer.Completed, got.State)
	require.Equal(t, int64(15_000_00), a.balance(t, a.to.ID))
	require.Equal(t, int64(15_000_00), a.balance(t, a.from.ID))

	d := a.mustGet(t, tr.ID)
	require.Equal(t, []transfer.State{transfer.Created, transfer.PendingApproval, transfer.Completed}, states(d))
	// Aynı anahtar: tekrar yürütme yok; yeni anahtarla ikinci onay 409.
	again, replayed, err := a.transfer.Approve(t.Context(), a.approver, "ap-1", tr.ID)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, tr.ID, again.ID)
	_, _, err = a.transfer.Approve(t.Context(), a.ops2, "ap-2", tr.ID)
	require.ErrorIs(t, err, transfer.ErrNotPending)
	require.Equal(t, int64(15_000_00), a.balance(t, a.to.ID), "çift onay çift para hareketi yapmaz")
	_, _, err = a.transfer.Approve(t.Context(), a.approver, "ap-3", uuid.New())
	require.ErrorIs(t, err, transfer.ErrNotFound)

	logs, err := audit.NewService(a.env.App).List(t.Context(), audit.Filter{Entity: "transfer", EntityID: tr.ID.String()}, pageReq())
	require.NoError(t, err)
	actions := map[string]bool{}
	for _, l := range logs.Items {
		actions[l.Action] = true
	}
	require.True(t, actions["transfer.create"] && actions["transfer.approve"], "denetim kaydı: %v", actions)
}

// İsteyen operasyon uzmanıysa (onay yetkisi olsa bile) kendi isteğini onaylayamaz; veritabanı kısıtı da bunu tutar.
func TestRequesterWithApproveRoleCannotApproveOwnTransfer(t *testing.T) {
	a := newApproval(t, 30_000_00)
	tr := a.request(t, a.ops2, "ops-req", 12_000_00) // ops2 herhangi bir müşteri hesabından başlatabilir
	require.Equal(t, transfer.PendingApproval, tr.State)
	_, _, err := a.transfer.Approve(t.Context(), a.ops2, "self", tr.ID)
	require.ErrorIs(t, err, transfer.ErrFourEyes)

	// Uygulama katmanı atlansa bile veritabanı kısıtı reddeder.
	_, err = a.env.App.Exec(t.Context(), `UPDATE approvals SET status = 'APPROVED', decided_by = requested_by, decided_at = now() WHERE transfer_id = $1`, tr.ID)
	require.Error(t, err)
	require.ErrorContains(t, err, "approvals_four_eyes")

	got, _, err := a.transfer.Approve(t.Context(), a.approver, "other", tr.ID)
	require.NoError(t, err)
	require.Equal(t, transfer.Completed, got.State)
}

func TestApprovalRejectAndInsufficientFundsAtExecution(t *testing.T) {
	a := newApproval(t, 11_000_00)
	// Reddedilen transferde para hareket etmez; gerekçe kaydedilir.
	t1 := a.request(t, a.customer, "r1", 10_500_00)
	_, _, err := a.transfer.Reject(t.Context(), a.approver, "rj1", t1.ID, transfer.RejectInput{Reason: ""})
	require.NoError(t, err, "servis gerekçeyi HTTP katmanında doğrular; boş gerekçe handler'da 400")
	t2 := a.request(t, a.customer, "r2", 10_600_00)
	rej, _, err := a.transfer.Reject(t.Context(), a.approver, "rj2", t2.ID, transfer.RejectInput{Reason: "şüpheli"})
	require.NoError(t, err)
	require.Equal(t, transfer.Rejected, rej.State)
	require.Equal(t, "APPROVAL_REJECTED", *rej.FailureCode)
	require.Equal(t, int64(11_000_00), a.balance(t, a.from.ID))
	_, _, err = a.transfer.Approve(t.Context(), a.approver, "late", t2.ID)
	require.ErrorIs(t, err, transfer.ErrNotPending, "reddedilmiş transfer onaylanamaz")

	// İki bekleyen transfer toplamda bakiyeyi aşıyor: yetersiz bakiye yürütme anında kontrol edilir (PLAN.md §12 notu).
	t3 := a.request(t, a.customer, "r3", 10_800_00)
	t4 := a.request(t, a.customer, "r4", 10_900_00)
	ok, _, err := a.transfer.Approve(t.Context(), a.approver, "a3", t3.ID)
	require.NoError(t, err)
	require.Equal(t, transfer.Completed, ok.State)
	poor, _, err := a.transfer.Approve(t.Context(), a.approver, "a4", t4.ID)
	require.NoError(t, err)
	require.Equal(t, transfer.Rejected, poor.State)
	require.Equal(t, "INSUFFICIENT_FUNDS", *poor.FailureCode)
	require.Equal(t, int64(200_00), a.balance(t, a.from.ID))
}

// Eşzamanlı iki farklı onaylayan: tek biri yürütür, diğeri 409 alır.
func TestConcurrentApprovalsExecuteOnce(t *testing.T) {
	a := newApproval(t, 30_000_00)
	tr := a.request(t, a.customer, "c1", 12_000_00)
	var g errgroup.Group
	results := make([]error, 6)
	for i := range results {
		p := a.approver
		if i%2 == 1 {
			p = a.ops2
		}
		g.Go(func() error {
			_, _, err := a.transfer.Approve(t.Context(), p, "conc-"+uuid.NewString(), tr.ID)
			results[i] = err
			return nil
		})
	}
	require.NoError(t, g.Wait())
	var ok int
	for _, err := range results {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, transfer.ErrNotPending)
		}
	}
	require.Equal(t, 1, ok)
	require.Equal(t, int64(12_000_00), a.balance(t, a.to.ID))
}

// Dış transfer onaydan sonra RESERVED olur, gönderim işi kuyruğa girer ve saga normal şekilde sonuçlanır.
func TestApprovedExternalTransferFollowsSaga(t *testing.T) {
	a := newApproval(t, 30_000_00)
	a.bank.set(always(bank.Completed), inquiring(bank.Completed, nil))
	tr, _, err := a.transfer.Create(t.Context(), a.customer, "ext-big", transfer.CreateInput{
		FromAccountID: a.from.ID, ToIBAN: account.NewIBAN(910_000_000), AmountMinor: 20_000_00, Currency: money.TRY, Type: transfer.FAST})
	require.NoError(t, err)
	require.Equal(t, transfer.PendingApproval, tr.State)
	require.Equal(t, int64(30_000_00), a.balance(t, a.from.ID))

	got, _, err := a.transfer.Approve(t.Context(), a.approver, "ext-ap", tr.ID)
	require.NoError(t, err)
	require.Equal(t, transfer.Reserved, got.State)
	require.Equal(t, int64(10_000_00), a.balance(t, a.from.ID))
	require.NoError(t, a.transfer.ProcessSend(audit.WithSystemActor(context.Background()), tr.ID))
	require.Equal(t, transfer.Completed, a.mustGet(t, tr.ID).State)
	a.requireInvariants(t)
}

func TestFourEyesErrorIsForbidden(t *testing.T) {
	var ae *apperr.Error
	require.ErrorAs(t, transfer.ErrFourEyes, &ae)
	require.Equal(t, 403, ae.Status)
	require.Equal(t, "FOUR_EYES_VIOLATION", ae.Code)
}

func (a approvalFixture) mustGet(t *testing.T, id uuid.UUID) transfer.Detail {
	t.Helper()
	d, err := a.transfer.Get(t.Context(), a.ops2, id)
	require.NoError(t, err)
	return d
}

func (a approvalFixture) requireInvariants(t *testing.T) {
	t.Helper()
	inv, err := a.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
}

func pageReq() page.Request { return page.Request{Limit: 20} }
