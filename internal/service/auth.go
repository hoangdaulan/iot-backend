// Package service holds the application logic between the HTTP/MQTT adapters and the
// repositories.
package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"iot-backend/internal/apperr"
	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

const (
	minUsernameLength = 3
	minPasswordLength = 6
	// bcrypt only uses the first 72 bytes of a password.
	maxPasswordBytes = 72
)

type AuthService struct {
	users      repository.UserRepository
	tokens     *TokenService
	bcryptCost int

	dummyHashOnce sync.Once
	dummyHash     []byte
}

func NewAuthService(users repository.UserRepository, tokens *TokenService) *AuthService {
	return &AuthService{users: users, tokens: tokens, bcryptCost: bcrypt.DefaultCost}
}

// WithBcryptCost lowers the hashing cost; tests use bcrypt.MinCost to stay fast.
func (s *AuthService) WithBcryptCost(cost int) *AuthService {
	s.bcryptCost = cost
	return s
}

type RegisterInput struct {
	Username, Email, Password string
}

func (s *AuthService) Register(ctx context.Context, in RegisterInput) (*model.User, error) {
	username := strings.TrimSpace(in.Username)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	switch {
	case utf8.RuneCountInString(username) < minUsernameLength:
		return nil, apperr.BadRequest("Invalid request data: username must be at least 3 characters")
	case !validEmail(email):
		return nil, apperr.BadRequest("Invalid request data: invalid email")
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		return nil, err
	}
	user := &model.User{Username: username, Email: email, PasswordHash: string(hash), Role: model.RoleUser}
	if err := s.users.Create(ctx, user); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.Conflict("Username or email already exists")
		}
		return nil, err
	}
	return user, nil
}

// Login accepts a username or an email and returns an access token.
func (s *AuthService) Login(ctx context.Context, login, password string) (string, *model.User, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return "", nil, apperr.BadRequest("Invalid request data")
	}
	user, err := s.users.FindByLogin(ctx, login)
	if errors.Is(err, repository.ErrNotFound) {
		// Compare anyway so unknown usernames take as long as wrong passwords.
		_ = bcrypt.CompareHashAndPassword(s.timingHash(), []byte(password))
		return "", nil, apperr.Unauthorized("Invalid email or password")
	}
	if err != nil {
		return "", nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return "", nil, apperr.Unauthorized("Invalid email or password")
	}
	token, err := s.tokens.Issue(user)
	if err != nil {
		return "", nil, err
	}
	return token, user, nil
}

func (s *AuthService) Profile(ctx context.Context, userID int64) (*model.User, error) {
	user, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("User not found")
	}
	return user, err
}

func (s *AuthService) UpdateProfile(
	ctx context.Context, userID int64, p repository.ProfileUpdate,
) (*model.User, error) {
	user, err := s.users.UpdateProfile(ctx, userID, p)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("User not found")
	}
	return user, err
}

func (s *AuthService) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	user, err := s.Profile(ctx, userID)
	if err != nil {
		return err
	}
	// 400, not 401: a 401 would make the client discard a valid session.
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)) != nil {
		return apperr.BadRequest("Old password is incorrect")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.bcryptCost)
	if err != nil {
		return err
	}
	return s.users.UpdatePassword(ctx, userID, string(hash))
}

func validatePassword(password string) error {
	switch {
	case len(password) < minPasswordLength:
		return apperr.BadRequest("Invalid request data: password must be at least 6 characters")
	case len(password) > maxPasswordBytes:
		return apperr.BadRequest("Invalid request data: password must be at most 72 bytes")
	}
	return nil
}

func validEmail(email string) bool {
	addr, err := mail.ParseAddress(email)
	return err == nil && addr.Address == email && strings.Contains(email, ".")
}

// timingHash is a bcrypt hash at the service's cost, used to equalize login timing.
func (s *AuthService) timingHash() []byte {
	s.dummyHashOnce.Do(func() {
		s.dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), s.bcryptCost)
	})
	return s.dummyHash
}
