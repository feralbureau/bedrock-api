package main

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "example/grpc/bedrock"
	"example/grpc/pkg/token"
	"example/grpc/store"
	"example/grpc/util"
)

// Register creates a new user account and returns the user_id.
func (s *bedrockServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	if req.GetEmail() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password are required")
	}

	// check if user already exists
	if _, err := s.userStore.Find(ctx, req.GetEmail()); err == nil {
		return nil, status.Error(codes.AlreadyExists, "user with this email already exists")
	}

	// hash the password
	hash, err := util.HashPassword(req.GetPassword())
	if err != nil {
		log.Printf("[Register] failed to hash password: %v", err)
		return nil, status.Error(codes.Internal, "failed to process password")
	}

	// build the user
	user := &store.User{
		ID:           uuid.New().String(),
		Email:        req.GetEmail(),
		PasswordHash: hash,
		Role:         "user",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.userStore.Save(ctx, user); err != nil {
		log.Printf("[Register] failed to save user: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to create user: %v", err)
	}

	log.Printf("[Register] new user created: id=%s email=%s", user.ID, user.Email)
	return &pb.RegisterResponse{UserId: user.ID}, nil
}

// Login verifies credentials and returns signed access + refresh tokens.
func (s *bedrockServer) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if req.GetEmail() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password are required")
	}

	user, err := s.userStore.Find(ctx, req.GetEmail())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	if err := util.CheckPassword(req.GetPassword(), user.PasswordHash); err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	accessToken, err := s.jwtManager.Generate(user.ID, user.Role)
	if err != nil {
		log.Printf("[Login] failed to generate access token: %v", err)
		return nil, status.Error(codes.Internal, "failed to generate token")
	}

	// refresh token uses a longer TTL (managed by a separate manager or same key)
	refreshToken, err := s.refreshManager.Generate(user.ID, user.Role)
	if err != nil {
		log.Printf("[Login] failed to generate refresh token: %v", err)
		return nil, status.Error(codes.Internal, "failed to generate refresh token")
	}

	log.Printf("[Login] user authenticated: id=%s email=%s", user.ID, user.Email)
	return &pb.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}

// RefreshToken validates a refresh token and issues a new access token.
func (s *bedrockServer) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.RefreshTokenResponse, error) {
	if req.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}

	claims, err := s.refreshManager.Validate(req.GetRefreshToken())
	if err != nil {
		log.Printf("[RefreshToken] invalid refresh token: %v", err)
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}

	newAccessToken, err := s.jwtManager.Generate(claims.UserID, claims.Role)
	if err != nil {
		log.Printf("[RefreshToken] failed to generate access token: %v", err)
		return nil, status.Error(codes.Internal, "failed to generate access token")
	}

	return &pb.RefreshTokenResponse{AccessToken: newAccessToken}, nil
}

// newJWTManager creates the short-lived access token manager (15 min).
func newJWTManager(secret string) *token.JWTManager {
	return token.NewJWTManager(secret, 15*time.Minute)
}

// newRefreshManager creates the long-lived refresh token manager (7 days).
func newRefreshManager(secret string) *token.JWTManager {
	return token.NewJWTManager(secret, 7*24*time.Hour)
}
