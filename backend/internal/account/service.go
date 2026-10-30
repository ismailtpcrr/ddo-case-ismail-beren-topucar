package account

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
	"parakopru/internal/platform/page"
)

// Customers, hesap modülünün müşteri modülünden ihtiyaç duyduğu tek şeydir (arayüzü kullanan paket tanımlar).
type Customers interface {
	// Exists müşteri yoksa customer.ErrNotFound döner.
	Exists(ctx context.Context, q db.DBTX, id uuid.UUID) error
	// IDForUser müşteri rolündeki kullanıcının müşteri kaydını döndürür.
	IDForUser(ctx context.Context, q db.DBTX, userID uuid.UUID) (uuid.UUID, error)
}

// Service hesap iş kurallarıdır.
type Service struct {
	tx        *db.TxManager
	customers Customers
	ids       ids.Generator
	clock     clock.Clock
	audit     *audit.Recorder
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, customers Customers, idGen ids.Generator, c clock.Clock, rec *audit.Recorder) *Service {
	return &Service{tx: tx, customers: customers, ids: idGen, clock: c, audit: rec}
}

// Open müşteri için yeni hesap açar.
func (s *Service) Open(ctx context.Context, in OpenInput) (Account, error) {
	var a Account
	err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		var err error
		a, err = s.OpenTx(ctx, tx, in)
		return err
	})
	return a, err
}

// OpenTx hesabı verilen transaction içinde açar (seed ve diğer modüller için).
func (s *Service) OpenTx(ctx context.Context, tx db.DBTX, in OpenInput) (Account, error) {
	maturity, err := s.validateMaturity(in)
	if err != nil {
		return Account{}, err
	}
	if err := s.customers.Exists(ctx, tx, in.CustomerID); err != nil {
		return Account{}, err
	}
	n, err := nextAccountNumber(ctx, tx)
	if err != nil {
		return Account{}, err
	}
	iban := NewIBAN(n)
	product := ProductChecking
	name := "Vadesiz " + string(in.Currency)
	if in.Kind == TimeDeposit {
		product, name = ProductTimeDeposit, "Vadeli "+string(in.Currency)
	}
	a := Account{
		ID: s.ids.NewID(), CustomerID: &in.CustomerID, Kind: in.Kind, Currency: in.Currency,
		NormalSide: Credit, AllowNegative: false, Name: name, IBAN: &iban,
		InterestProduct: &product, MaturityDate: maturity,
	}
	if err := insertAccount(ctx, tx, a); err != nil {
		return Account{}, err
	}
	created, err := getAccount(ctx, tx, a.ID)
	if err != nil {
		return Account{}, err
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Action: "account.open", Entity: "account", EntityID: a.ID.String(), New: created.ToResponse(),
	})
	return created, err
}

func (s *Service) validateMaturity(in OpenInput) (*time.Time, error) {
	if in.Kind != TimeDeposit {
		if in.MaturityDate != "" {
			return nil, ErrMaturityNotAllowed
		}
		return nil, nil
	}
	if in.MaturityDate == "" {
		return nil, ErrMaturityRequired
	}
	d, err := time.Parse(time.DateOnly, in.MaturityDate)
	if err != nil {
		return nil, ErrMaturityRequired
	}
	today := s.clock.Now().Truncate(24 * time.Hour)
	if !d.After(today) {
		return nil, ErrMaturityRequired
	}
	return &d, nil
}

// Get hesabı getirir. Müşteri yalnız kendi hesabını görebilir; tüm hesapları okuma izni (ledger:read) olan roller
// her hesabı görür.
func (s *Service) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Account, error) {
	a, err := getAccount(ctx, s.tx.DB(), id)
	if err != nil {
		return Account{}, err
	}
	if p.Role.Has(auth.PermLedgerRead) {
		return a, nil
	}
	own, err := s.customers.IDForUser(ctx, s.tx.DB(), p.UserID)
	if err != nil {
		return Account{}, err
	}
	if a.CustomerID == nil || *a.CustomerID != own {
		observability.Logger(ctx).Warn("access to another customer's account denied", slog.String("account_id", id.String()))
		return Account{}, ErrNotOwner
	}
	return a, nil
}

// List hesapları listeler. Müşteri yalnız kendi hesaplarını görür (customerID süzgeci yok sayılır);
// ledger:read sahibi roller tüm hesapları (iç hesaplar dahil) ya da verilen müşterinin hesaplarını görür.
func (s *Service) List(ctx context.Context, p auth.Principal, customerID *uuid.UUID, req page.Request) (page.Page[Account], error) {
	if !p.Role.Has(auth.PermLedgerRead) {
		own, err := s.customers.IDForUser(ctx, s.tx.DB(), p.UserID)
		if err != nil {
			return page.Page[Account]{}, err
		}
		customerID = &own
	}
	return listAccounts(ctx, s.tx.DB(), customerID, req)
}

// EnsureInternalAccounts her para birimi için bankanın iç hesaplarını açar; var olanlara dokunmaz.
// api her açılışta çağırır (temel seed).
func (s *Service) EnsureInternalAccounts(ctx context.Context) (int, error) {
	created := 0
	err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		created = 0
		for _, c := range money.Currencies {
			for _, ia := range internalChart {
				ok, err := insertInternalIfAbsent(ctx, tx, s.ids.NewID(), ia.Kind, ia.Side, c, ia.Name+" "+string(c))
				if err != nil {
					return err
				}
				if ok {
					created++
				}
			}
		}
		return nil
	})
	if err == nil {
		observability.Logger(ctx).Info("internal accounts ensured", slog.Int("created", created))
	}
	return created, err
}

// InternalAccountID verilen tür ve para birimindeki iç hesabın kimliğidir.
func (s *Service) InternalAccountID(ctx context.Context, q db.DBTX, kind Kind, c money.Currency) (uuid.UUID, error) {
	if q == nil {
		q = s.tx.DB()
	}
	return internalAccountID(ctx, q, kind, c)
}

// ByIBAN IBAN'a göre hesabı getirir; yoksa ErrNotFound (yetki kontrolü yapmaz, transfer alıcısını çözmek içindir).
func (s *Service) ByIBAN(ctx context.Context, q db.DBTX, iban string) (Account, error) {
	if q == nil {
		q = s.tx.DB()
	}
	return getAccountByIBAN(ctx, q, iban)
}

// OwnCustomerID müşteri rolündeki kullanıcının müşteri kimliğidir (nesne düzeyinde yetki için).
func (s *Service) OwnCustomerID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	return s.customers.IDForUser(ctx, s.tx.DB(), userID)
}
