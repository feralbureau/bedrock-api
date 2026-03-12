package main_test

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestIntegrationRunner runs the existing integration test runner (the tests/ main)
// as a subprocess. This test is skipped by default to avoid hitting external
// APIs during normal unit-test CI runs. To enable, set RUN_INTEGRATION_TESTS=1
// in the environment (and provide any provider secrets via env as needed).
func TestIntegrationRunner(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "1" {
		t.Skip("skipping integration tests; set RUN_INTEGRATION_TESTS=1 to enable")
	}

	addr := os.Getenv("BEDROCK_ADDR")
	if addr == "" {
		addr = "localhost:50052"
	}

	// optional token to skip auto-auth on the integration runner
	token := os.Getenv("TEST_TOKEN")

	// run the existing integration test runner (tests/main.go)
	args := []string{"run", "./tests", "-addr=" + addr}
	if token != "" {
		args = append(args, "-token="+token)
	}

	cmd := exec.Command("go", args...)
	cmd.Env = os.Environ()
	// give the process ample time
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start integration runner: %v", err)
	}

	done := make(chan error)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			// the integration runner process exited with a non-zero status
			t.Fatalf("integration runner failed: %v", err)
		}
	case <-time.After(20 * time.Minute):
		// kill long-running integration job
		_ = cmd.Process.Kill()
		t.Fatalf("integration runner timed out after 20m")
	}
}
