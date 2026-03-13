package auth

import (
	"context"
	"fmt"
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
	conn, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

// uniqueEmail generates a unique email using nanosecond precision to avoid collisions.
func uniqueEmail() string {
	return fmt.Sprintf("testuser_%d@example.com", time.Now().UnixNano())
}

func TestAuthRegister(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(10 * time.Second)
	defer cancel()

	resp, err := client.Register(ctx, &pb.RegisterRequest{
		Email:    uniqueEmail(),
		Password: "test-password-123",
	})

	if err != nil {
		t.Fatalf("Register failed: %v", err)
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

	email := uniqueEmail()
	password := "test-password-123"

	_, err := client.Register(ctx, &pb.RegisterRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	loginResp, err := client.Login(ctx, &pb.LoginRequest{
		Email:    email,
		Password: password,
	})

	if err != nil {
		t.Fatalf("Login failed: %v", err)
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

	email := uniqueEmail()
	password := "test-password-123"

	_, err := client.Register(ctx, &pb.RegisterRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	loginResp, err := client.Login(ctx, &pb.LoginRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	refreshResp, err := client.RefreshToken(ctx, &pb.RefreshTokenRequest{
		RefreshToken: loginResp.GetRefreshToken(),
	})

	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}

	if refreshResp.GetAccessToken() == "" {
		t.Errorf("RefreshToken returned empty access_token")
	}

	t.Logf("RefreshToken OK: new access_token length=%d", len(refreshResp.GetAccessToken()))
}
