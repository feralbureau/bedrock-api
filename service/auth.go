package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"example/grpc/pkg/token" // assuming this from previous steps
	"example/grpc/store"
	"example/grpc/util"
)

// assume pb exists with these types in the actual project
type LoginRequest struct {
	Email    string
	Password string
}

type LoginResponse struct {
	Token string
}

type RegisterRequest struct {
	Email    string
	Password string
}

type RegisterResponse struct {
	Token string
}

// bedrock_service implements grpc auth operations.
type BedrockService struct {
	userStore  store.UserStore
	jwtManager *token.JWTManager
}

// new_bedrock_service creates a new bedrock_service instance.
func NewBedrockService(userStore store.UserStore, jwtManager *token.JWTManager) *BedrockService {
	return &BedrockService{
		userStore:  userStore,
		jwtManager: jwtManager,
	}
}

// register handles new user sign-ups.
func (s *BedrockService) Register(ctx context.Context, req *RegisterRequest) (*RegisterResponse, error) {
	// check if user already exists
	if _, err := s.userStore.Find(ctx, req.Email); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "user with this email already exists")
	}

	// hash password
	hashedPassword, err := util.HashPassword(req.Password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password")
	}

	// create user struct
	user := &store.User{
		ID:           uuid.New().String(),
		Email:        req.Email,
		PasswordHash: hashedPassword,
		Role:         "user",
		CreatedAt:    time.Now().Format(time.RFC3339),
	}

	// save to db
	if err := s.userStore.Save(ctx, user); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save user: %v", err)
	}

	// generate token
	tk, err := s.jwtManager.Generate(user.ID, user.Role)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	return &RegisterResponse{Token: tk}, nil
}

// login verifies credentials and returns a token.
func (s *BedrockService) Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	// find user by email
	user, err := s.userStore.Find(ctx, req.Email)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "invalid credentials")
	}

	// verify password
	if err := util.CheckPassword(req.Password, user.PasswordHash); err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid credentials")
	}

	// generate access token
	tk, err := s.jwtManager.Generate(user.ID, user.Role)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	return &LoginResponse{Token: tk}, nil
}
