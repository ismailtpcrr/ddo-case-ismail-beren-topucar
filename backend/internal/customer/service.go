package customer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
)

// Service müşteri iş kurallarıdır.
type Service struct {
	tx    *db.TxManager
	ids   ids.Generator
	audit *audit.Recorder
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, idGen ids.Generator, rec *audit.Recorder) *Service {
	return &Service{tx: tx, ids: idGen, audit: rec}
}

// Create yeni müşteri kaydı açar ve denetim kaydına yazar.
func (s *Service) Create(ctx context.Context, in CreateInput) (Customer, error) {
	c := Customer{ID: s.ids.NewID(), FullName: in.FullName, NationalID: in.NationalID}
	if in.Email != "" {
		c.Email = &in.Email
	}
	err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		if err := insert(ctx, tx, c); err != nil {
			return err
		}
		created, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM customers WHERE id = $1`, c.ID))
		if err != nil {
			return err
		}
		c = created
		return s.audit.Record(ctx, tx, audit.Entry{Action: "customer.create", Entity: "customer", EntityID: c.ID.String(), New: c})
	})
	return c, err
}

// EnsureForUser kullanıcıya bağlı müşteri kaydını döndürür; yoksa oluşturur (demo seed). created yeni oluşturulduysa true.
func (s *Service) EnsureForUser(ctx context.Context, tx db.DBTX, userID uuid.UUID, in CreateInput) (Customer, bool, error) {
	c, err := s.ForUser(ctx, tx, userID)
	if err == nil {
		return c, false, nil
	}
	if !errors.Is(err, ErrNoCustomerProfile) {
		return Customer{}, false, err
	}
	c = Customer{ID: s.ids.NewID(), UserID: &userID, FullName: in.FullName, NationalID: in.NationalID}
	if in.Email != "" {
		c.Email = &in.Email
	}
	if err := insert(ctx, tx, c); err != nil {
		return Customer{}, false, err
	}
	if err := s.audit.Record(ctx, tx, audit.Entry{Action: "customer.create", Entity: "customer", EntityID: c.ID.String(), New: c}); err != nil {
		return Customer{}, false, err
	}
	return c, true, nil
}

// Get müşteriyi getirir.
func (s *Service) Get(ctx context.Context, q db.DBTX, id uuid.UUID) (Customer, error) {
	if q == nil {
		q = s.tx.DB()
	}
	return scan(q.QueryRow(ctx, `SELECT `+columns+` FROM customers WHERE id = $1`, id))
}

// ForUser kullanıcıya bağlı müşteri kaydını döndürür (müşteri rolünün "kendi" kapsamı).
func (s *Service) ForUser(ctx context.Context, q db.DBTX, userID uuid.UUID) (Customer, error) {
	if q == nil {
		q = s.tx.DB()
	}
	c, err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM customers WHERE user_id = $1`, userID))
	if errors.Is(err, ErrNotFound) {
		return Customer{}, ErrNoCustomerProfile
	}
	return c, err
}

type cursor struct {
	ID uuid.UUID `json:"id"`
}

// List müşterileri en yeniden eskiye, keyset sayfalı döndürür.
func (s *Service) List(ctx context.Context, req page.Request) (page.Page[Customer], error) {
	var after *uuid.UUID
	if req.Cursor != "" {
		var c cursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[Customer]{}, err
		}
		after = &c.ID
	}
	rows, err := s.tx.DB().Query(ctx,
		`SELECT `+columns+` FROM customers WHERE ($1::uuid IS NULL OR id < $1) ORDER BY id DESC LIMIT $2`,
		after, req.Limit+1)
	if err != nil {
		return page.Page[Customer]{}, fmt.Errorf("customer: list: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Customer, error) { return scan(r) })
	if err != nil {
		return page.Page[Customer]{}, err
	}
	return page.New(list, req.Limit, func(c Customer) any { return cursor{ID: c.ID} })
}

// Exists müşteri yoksa ErrNotFound döner (hesap modülü için).
func (s *Service) Exists(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	_, err := s.Get(ctx, q, id)
	return err
}

// IDForUser kullanıcıya bağlı müşterinin kimliğidir (hesap modülü için).
func (s *Service) IDForUser(ctx context.Context, q db.DBTX, userID uuid.UUID) (uuid.UUID, error) {
	c, err := s.ForUser(ctx, q, userID)
	return c.ID, err
}
