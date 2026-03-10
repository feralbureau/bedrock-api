package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"example/grpc/models"
	"example/grpc/pkg/hasher"
	"example/grpc/pkg/token"
	"example/grpc/repository"
)

// auth_service provides the logic for user authentication and registration.
type AuthService struct {
	repo repository.UserRepository
	jwt  *token.JWTManager

	// pending stores temporary registration data (email -> pending_reg).
	pending map[string]pendingReg
	mu      sync.RWMutex
}

// pending_reg holds the temporary registration code and password.
type pendingReg struct {
	code     string
	password string
	expiry   time.Time
}

// new_auth_service initializes a new auth_service.
func NewAuthService(repo repository.UserRepository, jwt *token.JWTManager) *AuthService {
	return &AuthService{
		repo:    repo,
		jwt:     jwt,
		pending: make(map[string]pendingReg),
	}
}

// initiate_registration checks if the user exists, mocks an email send, and stores the req.
func (s *AuthService) InitiateRegistration(ctx context.Context, email, password string) (bool, error) {
	// check if user already exists
	_, err := s.repo.GetByEmail(ctx, email)
	if err == nil {
		return false, errors.New("user already exists")
	}
	
	// if there's an error not equal to "user not found", we should ideally return it,
	// but to keep it simple, we assume it's just a simple check.

	code := "123123"

	// store password and code temporarily
	s.mu.Lock()
	s.pending[email] = pendingReg{
		code:     code,
		password: password,
		expiry:   time.Now().Add(10 * time.Minute),
	}
	s.mu.Unlock()

	// mock email sending
	fmt.Printf("email sent to %s with code %s\n", email, code)

	return true, nil
}

// complete_registration verifies the code, hashes the password, creates the user, and returns a token.
func (s *AuthService) CompleteRegistration(ctx context.Context, email, code string) (string, error) {
	s.mu.Lock()
	reg, exists := s.pending[email]
	if !exists {
		s.mu.Unlock()
		return "", errors.New("no pending registration found")
	}

	if time.Now().After(reg.expiry) {
		delete(s.pending, email)
		s.mu.Unlock()
		return "", errors.New("registration code expired")
	}

	// strictly enforce the "123123" code constraint
	if code != "123123" || reg.code != code {
		s.mu.Unlock()
		return "", errors.New("invalid registration code")
	}

	// cleanup the map entry
	delete(s.pending, email)
	s.mu.Unlock()

	// hash the password
	hash, err := hasher.HashPassword(reg.password)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}

	// create user in db
	user := &models.User{
		Email:        email,
		PasswordHash: hash,
		Role:         "user",
		IsVerified:   true,
	}

	if err := s.repo.Create(ctx, user); err != nil {
		return "", fmt.Errorf("failed to create user: %w", err)
	}

	// generate token
	tk, err := s.jwt.Generate(user.ID, user.Role)
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	return tk, nil
}

// login authenticates a user and returns a web token.
func (s *AuthService) Login(ctx context.Context, email, password string) (string, error) {
	// get user by email
	user, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return "", errors.New("invalid credentials")
	}

	// check password hash
	if !hasher.CheckPassword(password, user.PasswordHash) {
		return "", errors.New("invalid credentials")
	}

	// generate and return token
	tk, err := s.jwt.Generate(user.ID, user.Role)
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	return tk, nil
}
