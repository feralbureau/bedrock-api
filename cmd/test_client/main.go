package main

import (
	"context"
	"fmt"
	"log"
	"time"

	pb "example/grpc/bedrock" // ensure your protobuf generated package is correct
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	// 1. Connect
	serverAddr := "localhost:50052"
	log.Printf("Dialing %s...", serverAddr)
	
	conn, err := grpc.Dial(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("FAILED to connect: %v", err)
	}
	defer conn.Close()

	// Currently assumes AuthService exists in your pb layer.
	// If you merged Auth into BedrockService, use pb.NewBedrockServiceClient instead.
	authClient := pb.NewBedrockServiceClient(conn)
	
	email := fmt.Sprintf("test_%d@example.com", time.Now().UnixNano())
	password := "password123"

	// 2. Register
	ctx := context.Background()
	log.Printf("Registering user: %s", email)
	
	regResp, err := authClient.Register(ctx, &pb.RegisterRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		log.Fatalf("FAILED to register: %v", err)
	}
	
	// Assuming RegisterResponse returns UserID/Token (adjust field name if needed).
	log.Printf("SUCCESS: User registered! Response: %v", regResp)

	// 3. Login
	log.Printf("Logging in with user: %s", email)
	
	loginResp, err := authClient.Login(ctx, &pb.LoginRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		log.Fatalf("FAILED to login: %v", err)
	}
	
	token := loginResp.GetAccessToken()
	log.Printf("SUCCESS: Logged in! Access Token: %s", token)

	// 4. Test Protection
	log.Println("Testing protected endpoint...")
	
	// Attach token to outgoing context
	md := metadata.Pairs("authorization", "Bearer "+token)
	authCtx := metadata.NewOutgoingContext(ctx, md)

	bedrockClient := pb.NewBedrockServiceClient(conn)
	
	// Calling GetServiceStatus (or Ping if added) as a test
	statusResp, err := bedrockClient.GetServiceStatus(authCtx, &pb.ServiceStatusRequest{
		ForceRefresh: false,
	})
	if err != nil {
		log.Fatalf("FAILED protected method call: %v", err)
	}

	log.Printf("SUCCESS: Protected method accessed! Response status: %s", statusResp.GetStatus())
}
