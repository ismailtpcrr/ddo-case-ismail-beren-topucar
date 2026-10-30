package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
)

var (
	// ErrInvalidCredentials yanlış e-posta ya da parola. Hangisinin yanlış olduğu söylenmez;
	// aksi halde sistemde hangi e-postaların kayıtlı olduğu öğrenilebilirdi.
	ErrInvalidCredentials = apperr.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "E-posta ya da parola hatalı.")
	// ErrSessionExpired refresh token yok, geçersiz, süresi dolmuş ya da iptal edilmiş.
	ErrSessionExpired = apperr.New(http.StatusUnauthorized, "SESSION_EXPIRED", "Oturumun süresi doldu; lütfen yeniden giriş yapın.")
)

// Session giriş ya da yenileme sonucudur.
type Session struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	User             User
}

// Service kimlik doğrulama iş kurallarıdır.
type Service struct {
	tx        *db.TxManager
	repo      Repo
	tokens    *Tokens
	ids       ids.Generator
	clock     clock.Clock
	dummyHash string
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, repo Repo, tokens *Tokens, idGen ids.Generator, c clock.Clock) *Service {
	return &Service{
		tx: tx, repo: repo, tokens: tokens, ids: idGen, clock: c,
		// Kayıtlı olmayan e-postada da bir argon2id hesabı yapılır; böylece yanıt süresinden
		// e-postanın kayıtlı olup olmadığı anlaşılamaz.
		dummyHash: HashPassword("dummy-password-for-timing"),
	}
}

// NormalizeEmail e-postayı karşılaştırma için sadeleştirir.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Login e-posta ve parolayı doğrular, yeni bir oturum açar.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	u, err := s.repo.UserByEmail(ctx, s.tx.DB(), NormalizeEmail(email))
	if errors.Is(err, errNotFound) {
		_, _ = VerifyPassword(password, s.dummyHash)
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		observability.Logger(ctx).Info("login failed", slog.String("user_id", u.ID.String()))
		return Session{}, ErrInvalidCredentials
	}

	tokenID := s.ids.NewID()
	sess, err := s.issue(u, tokenID)
	if err != nil {
		return Session{}, err
	}
	if err := s.repo.InsertRefreshToken(ctx, s.tx.DB(), tokenID, u.ID, sess.RefreshExpiresAt); err != nil {
		return Session{}, err
	}
	observability.Logger(ctx).Info("login succeeded", slog.String("user_id", u.ID.String()), slog.String("role", string(u.Role)))
	return sess, nil
}

// Refresh geçerli bir refresh token karşılığında yeni bir access ve refresh token verir (rotasyon).
//
// Eski token iptal edilir. İptal edilmiş bir token tekrar gelirse (ör. çalınıp kullanılmış) kullanıcının
// açık bütün oturumları kapatılır; meşru kullanıcı da yeniden giriş yapmak zorunda kalır.
func (s *Service) Refresh(ctx context.Context, token string) (Session, error) {
	userID, tokenID, err := s.tokens.ParseRefresh(token)
	if err != nil {
		return Session{}, ErrSessionExpired
	}

	var (
		sess   Session
		reused bool
	)
	err = s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		reused = false
		rt, err := s.repo.LockRefreshToken(ctx, tx, tokenID)
		if errors.Is(err, errNotFound) {
			return ErrSessionExpired
		}
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if rt.UserID != userID || !now.Before(rt.ExpiresAt) {
			return ErrSessionExpired
		}
		if rt.RevokedAt != nil {
			// Hata döndürmüyoruz: iptal işlemi commit edilmeli. Hata transaction'dan sonra dönülür.
			reused = true
			_, err := s.repo.RevokeAllForUser(ctx, tx, userID, now)
			return err
		}

		u, err := s.repo.UserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		newID := s.ids.NewID()
		if sess, err = s.issue(u, newID); err != nil {
			return err
		}
		if err := s.repo.InsertRefreshToken(ctx, tx, newID, u.ID, sess.RefreshExpiresAt); err != nil {
			return err
		}
		return s.repo.RevokeRefreshToken(ctx, tx, tokenID, now, &newID)
	})
	if err != nil {
		return Session{}, err
	}
	if reused {
		observability.Logger(ctx).Warn("refresh token reuse detected; all sessions revoked",
			slog.String("user_id", userID.String()))
		return Session{}, ErrSessionExpired
	}
	return sess, nil
}

// Logout refresh token'ı iptal eder. Token yoksa ya da zaten geçersizse sessizce başarılı sayılır.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, tokenID, err := s.tokens.ParseRefresh(token)
	if err != nil {
		return nil
	}
	return s.repo.RevokeRefreshToken(ctx, s.tx.DB(), tokenID, s.clock.Now(), nil)
}

// Me oturumdaki kullanıcıyı döndürür.
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (User, error) {
	u, err := s.repo.UserByID(ctx, s.tx.DB(), userID)
	if errors.Is(err, errNotFound) {
		return User{}, ErrSessionExpired // token geçerli ama kullanıcı silinmiş
	}
	return u, err
}

// UserByEmail e-postaya göre kullanıcıyı getirir (seed için).
func (s *Service) UserByEmail(ctx context.Context, email string) (User, error) {
	return s.repo.UserByEmail(ctx, s.tx.DB(), NormalizeEmail(email))
}

func (s *Service) issue(u User, refreshID uuid.UUID) (Session, error) {
	access, accessExp, err := s.tokens.IssueAccess(u.ID, u.Role)
	if err != nil {
		return Session{}, err
	}
	refresh, refreshExp, err := s.tokens.IssueRefresh(u.ID, refreshID)
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refresh, RefreshExpiresAt: refreshExp,
		User: u,
	}, nil
}
