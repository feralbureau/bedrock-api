package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	pb "example/grpc/bedrock"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// cli flags 

var (
	addr           = flag.String("addr", "localhost:50052", "bedrock service address")
	perCallTimeout = flag.Duration("timeout", 10*time.Second, "per-rpc deadline")
	testEmail      = flag.String("email", "", "test email (random if empty)")
	testPass       = flag.String("pass", "test-pass-123!", "test password")
)

// colour palette 

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cCyan   = "\033[36m"
	cGray   = "\033[90m"
)

// result tracking 

type outcome int

const (
	outPass outcome = iota
	outFail
)

type testResult struct {
	name    string
	out     outcome
	detail  string
	latency time.Duration
}

var results []testResult

func recordResult(name string, out outcome, detail string, latency time.Duration) {
	results = append(results, testResult{name: name, out: out, detail: detail, latency: latency})
}

// log helpers 

func section(title string) {
	fmt.Printf("\n%s─── %s ───%s\n", cCyan, title, cReset)
}

func logf(prefix, color, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s%s%s %s\n", color, prefix, cReset, msg)
}

func pass(format string, args ...any) { logf("[+]", cGreen, format, args...) }
func fail(format string, args ...any) { logf("[-]", cRed, format, args...) }
func info(format string, args ...any) { logf("[i]", cGray, format, args...) }

func invoke(fn func(ctx context.Context) (any, error)) (any, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), *perCallTimeout)
	defer cancel()
	start := time.Now()
	v, err := fn(ctx)
	return v, time.Since(start), err
}

//  main 

func main() {
	flag.Parse()

	log.SetFlags(0)
	fmt.Printf("%s%sBedrock Auth System Integration Test%s\n", cBold, cCyan, cReset)
	fmt.Printf("%sTarget: %s%s\n", cGray, *addr, cReset)

	// generate random email if not provided
	if *testEmail == "" {
		rand.Seed(time.Now().UnixNano())
		*testEmail = fmt.Sprintf("testuser_%d@example.com", rand.Intn(1000000))
	}
	info("test email:    %s", *testEmail)

	// connect to grpc server
	conn, err := grpc.Dial(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect to server: %v", err)
	}
	defer conn.Close()

	client := pb.NewBedrockServiceClient(conn)

	//  Test Sequence 
	
	// 1. Register
	userID := testRegister(client)
	if userID == "" {
		finish()
		os.Exit(1)
	}

	// 2. Login
	tokens := testLogin(client)
	if tokens == nil {
		finish()
		os.Exit(1)
	}

	// 3. Test Auth Required Method (Invalid Token)
	testAuthRequiredFailure(client)

	// 4. Test Auth Required Method (Valid Token)
	testAuthRequiredSuccess(client, tokens.AccessToken)

	// 5. Refresh Token
	newAccessToken := testRefreshToken(client, tokens.RefreshToken)
	if newAccessToken == "" {
		finish()
		os.Exit(1)
	}

	// 6. Test Auth Required Method (New Access Token)
	testAuthRequiredSuccess(client, newAccessToken)

	finish()
}

func testRegister(c pb.BedrockServiceClient) string {
	name := "Registration"
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.Register(ctx, &pb.RegisterRequest{
			Email:    *testEmail,
			Password: *testPass,
		})
	})

	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error: %s (code: %s)", st.Message(), st.Code())
		recordResult(name, outFail, st.Message(), lat)
		return ""
	}

	r := resp.(*pb.RegisterResponse)
	pass("user registered successfully. userID: %s", r.GetUserId())
	recordResult(name, outPass, r.GetUserId(), lat)
	return r.GetUserId()
}

type authTokens struct {
	AccessToken  string
	RefreshToken string
}

func testLogin(c pb.BedrockServiceClient) *authTokens {
	name := "Login"
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.Login(ctx, &pb.LoginRequest{
			Email:    *testEmail,
			Password: *testPass,
		})
	})

	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error: %s (code: %s)", st.Message(), st.Code())
		recordResult(name, outFail, st.Message(), lat)
		return nil
	}

	r := resp.(*pb.LoginResponse)
	if r.GetAccessToken() == "" || r.GetRefreshToken() == "" {
		fail("received empty tokens in login response")
		recordResult(name, outFail, "empty tokens", lat)
		return nil
	}

	pass("login successful. received access and refresh tokens.")
	info("access_token: %s", r.GetAccessToken())
	info("refresh_token: %s", r.GetRefreshToken())
	
	recordResult(name, outPass, "tokens received", lat)
	return &authTokens{
		AccessToken:  r.GetAccessToken(),
		RefreshToken: r.GetRefreshToken(),
	}
}

func testAuthRequiredFailure(c pb.BedrockServiceClient) {
	name := "Auth Shield (Missing/Invalid Token)"
	section(name)

	// try without metadata
	_, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetListeningHistory(ctx, &pb.ListeningHistoryRequest{Limit: 1})
	})

	if err == nil {
		fail("expected error for unauthenticated request, but got success")
		recordResult(name, outFail, "gate bypassed without token", lat)
		return
	}

	st, _ := status.FromError(err)
	pass("gate worked: unauthorized request blocked with code: %s", st.Code())
	recordResult(name, outPass, "blocked as expected", lat)
}

func testAuthRequiredSuccess(client pb.BedrockServiceClient, token string) {
	name := "Authenticated Request"
	section(name)

	// inject token into metadata
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)

	// we DON'T use invoke here because invoke uses its own context.Background() which loses the metadata
	start := time.Now()
	resp, err := client.GetListeningHistory(ctx, &pb.ListeningHistoryRequest{Limit: 1})
	lat := time.Since(start)

	if err != nil {
		st, _ := status.FromError(err)
		fail("authenticated request failed: %s (code: %s)", st.Message(), st.Code())
		recordResult(name, outFail, st.Message(), lat)
		return
	}

	r := resp
	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("request failed with app-level error: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	pass("authenticated request succeeded.")
	info("events found: %d", len(r.GetEvents()))
	recordResult(name, outPass, "success", lat)
}

func testRefreshToken(c pb.BedrockServiceClient, refresh string) string {
	name := "Token Refresh"
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.RefreshToken(ctx, &pb.RefreshTokenRequest{
			RefreshToken: refresh,
		})
	})

	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error: %s (code: %s)", st.Message(), st.Code())
		recordResult(name, outFail, st.Message(), lat)
		return ""
	}

	r := resp.(*pb.RefreshTokenResponse)
	if r.GetAccessToken() == "" {
		fail("received empty access token in refresh response")
		recordResult(name, outFail, "empty access token", lat)
		return ""
	}

	pass("token refreshed successfully. received new access token.")
	info("new access_token: %s", r.GetAccessToken())
	recordResult(name, outPass, "new token issued", lat)
	return r.GetAccessToken()
}

func finish() {
	fmt.Printf("\n%s Test Report %s\n", cCyan, cReset)
	
	allPass := true
	for _, res := range results {
		statusStr := fmt.Sprintf("%sPASS%s", cGreen, cReset)
		if res.out == outFail {
			statusStr = fmt.Sprintf("%sFAIL%s", cRed, cReset)
			allPass = false
		}
		
		fmt.Printf("%s  %-30s : %s [%s] %s(%s)%s\n", 
			cGray, res.name, statusStr, res.latency.Round(time.Millisecond), 
			cGray, res.detail, cReset)
	}

	if allPass {
		fmt.Printf("\n%sSummary: %sALLL TESTS PASSED%s\n", cGreen, cBold, cReset)
	} else {
		fmt.Printf("\n%sSummary: %sSOME TESTS FAILED%s\n", cRed, cBold, cReset)
	}
}
