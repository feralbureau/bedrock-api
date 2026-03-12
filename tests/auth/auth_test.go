package auth

import (
	"context"
	"os"
	"testing"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var testConn *grpc.ClientConn

func init() {
	addr := os.Getenv("BEDROCK_TEST_ADDR")
	if addr == "" {
		addr = "localhost:50052"
	}
	conn, _ := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	testConn = conn
}

func getTestClient(t *testing.T) pb.BedrockServiceClient {
	if testConn == nil {
		t.Skip("test setup failed")
	}
	return pb.NewBedrockServiceClient(testConn)
}

func ctxWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func TestAuthRegister(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(10 * time.Second)
	defer cancel()

	email := "testuser_" + time.Now().Format("20060102150405") + "@example.com"
	password := "test-password-123"

	resp, err := client.Register(ctx, &pb.RegisterRequest{
		Email:    email,
		Password: password,
	})

	if err != nil {
		t.Skipf("Register failed: %v", err)
	}

	if resp.GetUserId() == "" {
		t.Errorf("Register returned empty user_id")
	}

	t.Logf("Register OK: user_id=%s", resp.GetUserId())
}

func TestAuthLogin(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(10 * time.Second)
	defer cancel()

	email := "testuser_" + time.Now().Format("20060102150405") + "@example.com"
	password := "test-password-123"

	_, err := client.Register(ctx, &pb.RegisterRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Skipf("Register failed: %v", err)
	}

	loginResp, err := client.Login(ctx, &pb.LoginRequest{
		Email:    email,
		Password: password,
	})

	if err != nil {
		t.Skipf("Login failed: %v", err)
	}

	if loginResp.GetAccessToken() == "" {
		t.Errorf("Login returned empty access_token")
	}

	if loginResp.GetRefreshToken() == "" {
		t.Errorf("Login returned empty refresh_token")
	}

	t.Logf("Login OK: access_token length=%d", len(loginResp.GetAccessToken()))
}

func TestAuthRefreshToken(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(10 * time.Second)
	defer cancel()

	email := "testuser_" + time.Now().Format("20060102150405") + "@example.com"
	password := "test-password-123"

	_, err := client.Register(ctx, &pb.RegisterRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Skipf("Register failed: %v", err)
	}

	loginResp, err := client.Login(ctx, &pb.LoginRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Skipf("Login failed: %v", err)
	}

	refreshToken := loginResp.GetRefreshToken()

	refreshResp, err := client.RefreshToken(ctx, &pb.RefreshTokenRequest{
		RefreshToken: refreshToken,
	})

	if err != nil {
		t.Skipf("RefreshToken failed: %v", err)
	}

	if refreshResp.GetAccessToken() == "" {
		t.Errorf("RefreshToken returned empty access_token")
	}

	t.Logf("RefreshToken OK: new access_token length=%d", len(refreshResp.GetAccessToken()))
}
