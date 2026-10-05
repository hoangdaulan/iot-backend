package service

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"iot-backend/internal/model"
)

// Claims identify the authenticated user of a request.
type Claims struct {
	UserID int64
	Role   model.Role
}

// TokenService issues and verifies HS256 access tokens. There are no refresh tokens: an expired
// token means logging in again.
type TokenService struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokenService(secret string, ttl time.Duration) *TokenService {
	return &TokenService{secret: []byte(secret), ttl: ttl, now: time.Now}
}

type tokenClaims struct {
	Role model.Role `json:"role"`
	jwt.RegisteredClaims
}

func (s *TokenService) Issue(u *model.User) (string, error) {
	now := s.now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, tokenClaims{
		Role: u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	})
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Parse verifies the signature and expiry of raw.
func (s *TokenService) Parse(raw string) (Claims, error) {
	var claims tokenClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) {
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithTimeFunc(s.now),
		jwt.WithExpirationRequired())
	if err != nil {
		return Claims{}, err
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id <= 0 {
		return Claims{}, errors.New("invalid subject")
	}
	return Claims{UserID: id, Role: claims.Role}, nil
}
