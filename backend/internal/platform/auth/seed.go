package auth

import (
	"context"
	"errors"
	"log/slog"

	"parakopru/internal/platform/observability"
)

// DemoUser her rol için bir demo hesaptır (proje.md §3: "her rol için bir tane"; README ve TESTING.md'de listelenir).
type DemoUser struct {
	Email    string
	FullName string
	Role     Role
}

// DemoUsers PLAN.md §6'daki demo hesaplardır.
var DemoUsers = []DemoUser{
	{Email: "musteri@parakopru.test", FullName: "Demo Müşteri", Role: RoleCustomer},
	{Email: "operasyon@parakopru.test", FullName: "Demo Operasyon Uzmanı", Role: RoleOperations},
	{Email: "risk@parakopru.test", FullName: "Demo Risk Analisti", Role: RoleRiskAnalyst},
	{Email: "uyum@parakopru.test", FullName: "Demo Uyum Uzmanı", Role: RoleCompliance},
	{Email: "denetci@parakopru.test", FullName: "Demo Denetçi", Role: RoleAuditor},
	{Email: "sistem@parakopru.test", FullName: "Sistem (zamanlanmış işler)", Role: RoleSystem},
}

// SeedDemoUsers eksik demo hesapları ekler; var olanlara dokunmaz. api her açılışta çağırır, tekrar
// çalışması güvenlidir. Parola özeti yalnız eksik hesaplar için hesaplanır.
func (s *Service) SeedDemoUsers(ctx context.Context, password string) (int, error) {
	if password == "" {
		return 0, errors.New("auth: demo password is empty")
	}
	emails := make([]string, len(DemoUsers))
	for i, d := range DemoUsers {
		emails[i] = d.Email
	}
	existing, err := s.repo.ExistingEmails(ctx, s.tx.DB(), emails)
	if err != nil {
		return 0, err
	}

	created := 0
	for _, d := range DemoUsers {
		if existing[d.Email] {
			continue
		}
		ok, err := s.repo.InsertUserIfAbsent(ctx, s.tx.DB(), User{
			ID: s.ids.NewID(), Email: d.Email, PasswordHash: HashPassword(password), Role: d.Role, FullName: d.FullName,
		})
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	observability.Logger(ctx).Info("demo users seeded", slog.Int("created", created), slog.Int("total", len(DemoUsers)))
	return created, nil
}
