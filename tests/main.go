// package main is the top-level integration test runner for the bedrock gRPC server.
//
// it:
//   1. auto-authenticates (register + login) against the running server to obtain a JWT,
//      unless -token is passed directly.
//   2. runs every platform test suite (youtube, spotify, deezer, soundcloud) as a subprocess,
//      forwarding the token via -token so each suite can attach it to RPC calls.
//   3. prints an aggregated pass/fail summary across all suites.
//
// usage:
//
//	go run ./tests/                                    # auto-auth + all suites
//	go run ./tests/ -addr=10.0.0.1:50052              # custom server
//	go run ./tests/ -token=<jwt>                      # skip auto-auth
//	go run ./tests/ -email=me@example.com -pass=abc   # use specific credentials
//	go run ./tests/ -verbose                          # forward -verbose to sub-tests
//	go run ./tests/ -suites=youtube,spotify           # run only specific suites
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	pb "example/grpc/bedrock"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ── cli flags ────────────────────────────────────────────────────────

var (
	addr           = flag.String("addr", "localhost:50052", "bedrock service address")
	perCallTimeout = flag.Duration("timeout", 20*time.Second, "per-rpc timeout (forwarded to sub-tests)")
	accessToken    = flag.String("token", "", "JWT access token — skips auto-auth when set")
	testEmail      = flag.String("email", "", "registration email (random if empty)")
	testPass       = flag.String("pass", "test-pass-123!", "registration password")
	verbose        = flag.Bool("verbose", false, "pass -verbose to each sub-test")
	suitesFlag     = flag.String("suites", "youtube,spotify,deezer,soundcloud", "comma-separated list of suites to run")
)

// ── colour palette ───────────────────────────────────────────────────

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cCyan   = "\033[36m"
	cGray   = "\033[90m"
)

// ── auto-auth ────────────────────────────────────────────────────────

// autoAuth registers a fresh test user and logs in, returning the access token.
// if registration fails (e.g. email already exists) it falls through to login.
func autoAuth() string {
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("[auth] dial %s: %v", *addr, err)
	}
	defer conn.Close()

	c := pb.NewBedrockServiceClient(conn)

	if *testEmail == "" {
		*testEmail = fmt.Sprintf("testrunner_%d@example.com", rand.Intn(10_000_000))
	}

	fmt.Printf("  %s[auth]%s registering %s\n", cGray, cReset, *testEmail)

	regCtx, regCancel := context.WithTimeout(context.Background(), *perCallTimeout)
	defer regCancel()
	_, regErr := c.Register(regCtx, &pb.RegisterRequest{
		Email:    *testEmail,
		Password: *testPass,
	})
	if regErr != nil {
		// not fatal — user may already exist
		fmt.Printf("  %s[auth]%s register skipped: %v\n", cYellow, cReset, regErr)
	}

	loginCtx, loginCancel := context.WithTimeout(context.Background(), *perCallTimeout)
	defer loginCancel()
	loginResp, err := c.Login(loginCtx, &pb.LoginRequest{
		Email:    *testEmail,
		Password: *testPass,
	})
	if err != nil {
		log.Fatalf("[auth] login failed: %v", err)
	}

	token := loginResp.GetAccessToken()
	if token == "" {
		log.Fatal("[auth] server returned empty access token")
	}

	fmt.Printf("  %s[auth]%s token obtained\n", cGreen, cReset)
	return token
}

// ── sub-test runner ──────────────────────────────────────────────────

type suiteResult struct {
	name    string
	passed  bool
	elapsed time.Duration
}

// allSuites is the ordered registry of available platform suites.
var allSuites = []struct {
	name string
	pkg  string
	env  []string
}{
	{"youtube", "./tests/youtube/", []string{"YOUTUBE_INTEGRATION_TEST=1"}},
	{"spotify", "./tests/spotify/", nil},
	{"deezer", "./tests/deezer/", nil},
	{"soundcloud", "./tests/soundcloud/", nil},
}

func runSuite(name, pkg, token string, extraEnv []string) suiteResult {
	fmt.Printf("\n%s─── %s ───%s\n", cCyan, strings.ToUpper(name), cReset)

	args := []string{
		"run", pkg,
		"-addr=" + *addr,
		"-timeout=" + perCallTimeout.String(),
		"-token=" + token,
	}
	if *verbose {
		args = append(args, "-verbose")
	}

	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	return suiteResult{
		name:    name,
		passed:  err == nil,
		elapsed: elapsed,
	}
}

// ── main ─────────────────────────────────────────────────────────────

func main() {
	rand.Seed(time.Now().UnixNano()) //nolint:staticcheck
	flag.Parse()
	log.SetFlags(0)

	fmt.Printf("%s%s═══ BEDROCK INTEGRATION TEST SUITE ═══%s\n", cBold, cCyan, cReset)
	fmt.Printf("  target  : %s\n", *addr)
	fmt.Printf("  timeout : %s\n", *perCallTimeout)

	// resolve which suites to run
	wanted := map[string]bool{}
	for _, s := range strings.Split(*suitesFlag, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			wanted[s] = true
		}
	}

	// obtain token
	token := *accessToken
	if token == "" {
		fmt.Printf("\n%s─── AUTO AUTH ───%s\n", cCyan, cReset)
		token = autoAuth()
	} else {
		fmt.Printf("  %s[auth]%s using provided -token\n", cGray, cReset)
	}

	// run suites
	var results []suiteResult
	for _, s := range allSuites {
		if !wanted[s.name] {
			continue
		}
		r := runSuite(s.name, s.pkg, token, s.env)
		results = append(results, r)
	}

	// aggregated summary
	fmt.Printf("\n%s%s═══ SUITE SUMMARY ═══%s\n", cBold, cCyan, cReset)
	allPass := true
	for _, r := range results {
		var statusStr string
		if r.passed {
			statusStr = fmt.Sprintf("%s%sPASS%s", cBold, cGreen, cReset)
		} else {
			statusStr = fmt.Sprintf("%s%sFAIL%s", cBold, cRed, cReset)
			allPass = false
		}
		fmt.Printf("  %-15s %s  %s(%s)%s\n",
			r.name, statusStr,
			cGray, r.elapsed.Round(time.Second), cReset)
	}

	if allPass {
		fmt.Printf("\n%s%sALL SUITES PASSED%s\n\n", cBold, cGreen, cReset)
	} else {
		fmt.Printf("\n%s%sSOME SUITES FAILED%s\n\n", cBold, cRed, cReset)
		os.Exit(1)
	}
}
