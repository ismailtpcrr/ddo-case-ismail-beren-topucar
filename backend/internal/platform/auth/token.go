package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"parakopru/internal/platform/clock"
)

const (
	issuer = "parakopru"

	tokenTypeAccess  = "access"
	tokenTypeRefresh = "refresh"
)

var errInvalidToken = errors.New("auth: invalid token")

// claims JWT gövdesidir. "typ" alanı access ve refresh token'ı birbirinden ayırır: refresh token
// Authorization başlığında access yerine kullanılamaz, tersi de mümkün değildir.
type claims struct {
	Role Role   `json:"role,omitempty"`
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

// Principal, doğrulanmış isteğin sahibidir.
type Principal struct {
	UserID uuid.UUID
	Role   Role
}

// Tokens JWT üretir ve doğrular (HS256).
//
//   - Access token kısa ömürlüdür (varsayılan 15 dk); her istekte Authorization: Bearer ile gelir,
//     veritabanına bakmadan doğrulanır.
//   - Refresh token uzun ömürlüdür (varsayılan 7 gün); HttpOnly çerezde durur. Kimliği (jti)
//     refresh_tokens tablosunda tutulur; böylece çıkışta ya da çalınma şüphesinde iptal edilebilir.
type Tokens struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	clock      clock.Clock
}

// NewTokens yeni bir Tokens oluşturur.
func NewTokens(secret string, accessTTL, refreshTTL time.Duration, c clock.Clock) *Tokens {
	return &Tokens{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL, clock: c}
}

// IssueAccess kullanıcı için access token üretir.
func (t *Tokens) IssueAccess(userID uuid.UUID, role Role) (string, time.Time, error) {
	return t.sign(claims{Role: role, Type: tokenTypeAccess}, userID, uuid.Nil, t.accessTTL)
}

// IssueRefresh verilen kimlikle (jti) refresh token üretir.
func (t *Tokens) IssueRefresh(userID, tokenID uuid.UUID) (string, time.Time, error) {
	return t.sign(claims{Type: tokenTypeRefresh}, userID, tokenID, t.refreshTTL)
}

func (t *Tokens) sign(c claims, userID, tokenID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	now := t.clock.Now()
	exp := now.Add(ttl)
	c.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}
	if tokenID != uuid.Nil {
		c.ID = tokenID.String()
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
	}
	return s, exp, nil
}

// ParseAccess access token'ı doğrular: imza, algoritma (yalnız HS256), yayımlayan, süre ve tür.
func (t *Tokens) ParseAccess(s string) (Principal, error) {
	c, err := t.parse(s, tokenTypeAccess)
	if err != nil {
		return Principal{}, err
	}
	if !c.Role.Valid() {
		return Principal{}, errInvalidToken
	}
	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return Principal{}, errInvalidToken
	}
	return Principal{UserID: userID, Role: c.Role}, nil
}

// ParseRefresh refresh token'ı doğrular ve kullanıcı ile token kimliğini döndürür.
// İptal edilip edilmediği ayrıca veritabanından kontrol edilir (Service.Refresh).
func (t *Tokens) ParseRefresh(s string) (userID, tokenID uuid.UUID, err error) {
	c, err := t.parse(s, tokenTypeRefresh)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if userID, err = uuid.Parse(c.Subject); err != nil {
		return uuid.Nil, uuid.Nil, errInvalidToken
	}
	if tokenID, err = uuid.Parse(c.ID); err != nil {
		return uuid.Nil, uuid.Nil, errInvalidToken
	}
	return userID, tokenID, nil
}

func (t *Tokens) parse(s, wantType string) (*claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(s, &c,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		// Algoritma sabitlenir: "alg: none" ya da başka bir algoritmayla imzalanmış token kabul edilmez.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.clock.Now),
	)
	if err != nil || c.Type != wantType {
		return nil, errInvalidToken
	}
	return &c, nil
}
