package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/ipc"
)

func TestCLIShareDownloadAndRevoke(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "shareto")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s: %v", output, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := config.DefaultConfig()
	cfg.ListenAddr = address
	cfg.PublicBaseURL = "http://" + address
	configDir := filepath.Join(dir, "config")
	runtimeDir := filepath.Join(dir, "run")
	if err := config.SaveConfig(filepath.Join(configDir, "shareto", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"XDG_CONFIG_HOME="+configDir,
		"XDG_STATE_HOME="+filepath.Join(dir, "state"),
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
	log, err := os.Create(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(binary, "daemon")
	daemon.Env = environment
	daemon.Dir = dir
	daemon.Stdout = log
	daemon.Stderr = log
	if err := daemon.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- daemon.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop after SIGTERM")
			daemon.Process.Kill()
			<-done
		}
		log.Close()
	})
	client := ipc.NewClient(filepath.Join(runtimeDir, "shareto", "ipc.sock"))
	deadline := time.Now().Add(10 * time.Second)
	for !client.Ping() {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}
	command := func(arguments ...string) string {
		cmd := exec.Command(binary, arguments...)
		cmd.Env = environment
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %s: %v", arguments, output, err)
		}
		return string(output)
	}
	content := []byte("shareto real CLI integration content")
	file := filepath.Join(dir, "download.bin")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	command("-m", "1", file)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shares, err := client.List(ctx)
	if err != nil || len(shares) != 1 {
		t.Fatalf("created shares: %v: %v", shares, err)
	}
	url := cfg.PublicBaseURL + "/s/" + shares[0].Token + "?raw=1"
	httpClient := &http.Client{Timeout: 5 * time.Second}
	defer httpClient.CloseIdleConnections()
	response, err := httpClient.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	download, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(content, download) {
		t.Fatalf("download mismatch: status=%d err=%v", response.StatusCode, err)
	}
	if output := command("list"); !strings.Contains(output, "download.bin") || !strings.Contains(output, "0/1") {
		t.Fatalf("CLI list output: %s", output)
	}
	command("status")
	command("revoke", shares[0].Token)
	remaining, err := client.List(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("revoked shares: %v: %v", remaining, err)
	}
}

func TestIssue3Requirements(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "shareto")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s: %v", output, err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()

	cfg := config.DefaultConfig()
	cfg.ListenAddr = address
	cfg.PublicBaseURL = "http://" + address
	configDir := filepath.Join(dir, "config")
	runtimeDir := filepath.Join(dir, "run")
	stateDir := filepath.Join(dir, "state")
	if err := config.SaveConfig(filepath.Join(configDir, "shareto", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"XDG_CONFIG_HOME="+configDir,
		"XDG_STATE_HOME="+stateDir,
		"XDG_RUNTIME_DIR="+runtimeDir,
	)

	startDaemon := func() *exec.Cmd {
		log, err := os.Create(filepath.Join(dir, "daemon.log"))
		if err != nil {
			t.Fatal(err)
		}
		daemon := exec.Command(binary, "daemon")
		daemon.Env = environment
		daemon.Dir = dir
		daemon.Stdout = log
		daemon.Stderr = log
		if err := daemon.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		return daemon
	}

	stopDaemon := func(daemon *exec.Cmd) {
		daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- daemon.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			daemon.Process.Kill()
			<-done
		}
	}

	daemon := startDaemon()
	defer func() {
		if daemon != nil && daemon.Process != nil {
			stopDaemon(daemon)
		}
	}()

	client := ipc.NewClient(filepath.Join(runtimeDir, "shareto", "ipc.sock"))
	deadline := time.Now().Add(10 * time.Second)
	for !client.Ping() {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}

	command := func(arguments ...string) (string, error) {
		cmd := exec.Command(binary, arguments...)
		cmd.Env = environment
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	// 1. Point 2: custom --base-url per share
	subDirA := filepath.Join(dir, "a")
	subDirB := filepath.Join(dir, "b")
	_ = os.MkdirAll(subDirA, 0755)
	_ = os.MkdirAll(subDirB, 0755)
	fileA := filepath.Join(subDirA, "report.txt")
	fileB := filepath.Join(subDirB, "report.txt")
	if err := os.WriteFile(fileA, []byte("report A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("report B"), 0644); err != nil {
		t.Fatal(err)
	}

	outA, err := command("--base-url", "https://alpha.example.invalid", fileA)
	if err != nil {
		t.Fatalf("share fileA: %s: %v", outA, err)
	}
	if !strings.Contains(outA, "https://alpha.example.invalid/s/") {
		t.Fatalf("expected alpha base URL in output: %s", outA)
	}

	outB, err := command("--base-url", "https://beta.example.invalid", fileB)
	if err != nil {
		t.Fatalf("share fileB: %s: %v", outB, err)
	}
	if !strings.Contains(outB, "https://beta.example.invalid/s/") {
		t.Fatalf("expected beta base URL in output: %s", outB)
	}

	// 2. Point 1: shareto list displays full token and URL
	listOut, err := command("list")
	if err != nil {
		t.Fatalf("list failed: %s: %v", listOut, err)
	}
	if !strings.Contains(listOut, "https://alpha.example.invalid/s/") || !strings.Contains(listOut, "https://beta.example.invalid/s/") {
		t.Fatalf("list missing custom URLs: %s", listOut)
	}

	// Verbose list displays full path and direct URL
	listVOut, err := command("list", "-v")
	if err != nil {
		t.Fatalf("list -v failed: %s: %v", listVOut, err)
	}
	if !strings.Contains(listVOut, fileA) || !strings.Contains(listVOut, "Direct URL:") {
		t.Fatalf("list -v missing full path or direct URL: %s", listVOut)
	}

	// Restart daemon and ensure URLs are persisted across restarts
	stopDaemon(daemon)
	daemon = startDaemon()
	for !client.Ping() {
		time.Sleep(25 * time.Millisecond)
	}

	listAfterRestart, err := command("list")
	if err != nil {
		t.Fatalf("list after restart failed: %s: %v", listAfterRestart, err)
	}
	if !strings.Contains(listAfterRestart, "https://alpha.example.invalid/s/") || !strings.Contains(listAfterRestart, "https://beta.example.invalid/s/") {
		t.Fatalf("custom URLs not restored after daemon restart: %s", listAfterRestart)
	}

	// Point 3: Revoke with non-matching target returns exit code 1
	nonMatchOut, err := command("revoke", "nonexistent-token")
	if err == nil {
		t.Fatalf("expected error revoking nonexistent token, got success: %s", nonMatchOut)
	}

	// Point 4: Ambiguous revoke report.txt in non-interactive mode without --yes should fail
	ambigOut, err := command("revoke", "report.txt")
	if err == nil {
		t.Fatalf("expected error revoking ambiguous report.txt without --yes, got: %s", ambigOut)
	}
	if !strings.Contains(ambigOut, "multiple shares match") {
		t.Fatalf("expected multiple shares match error message, got: %s", ambigOut)
	}

	// Point 4: Revoking with relative path resolves properly
	relRevokeOut, err := command("revoke", "./a/report.txt")
	if err != nil {
		t.Fatalf("revoking relative path ./a/report.txt failed: %s: %v", relRevokeOut, err)
	}
	if !strings.Contains(relRevokeOut, "Successfully revoked share") {
		t.Fatalf("unexpected revoke output: %s", relRevokeOut)
	}

	// Point 3: Token with trailing dots matches and revokes
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sharesB, err := client.List(ctx)
	if err != nil || len(sharesB) != 1 {
		t.Fatalf("expected 1 remaining share, got %d: %v", len(sharesB), err)
	}
	tokenWithDots := sharesB[0].Token + ".."
	dotRevokeOut, err := command("revoke", tokenWithDots)
	if err != nil {
		t.Fatalf("revoking token with trailing dots failed: %s: %v", dotRevokeOut, err)
	}

	remainingShares, _ := client.List(ctx)
	if len(remainingShares) != 0 {
		t.Fatalf("expected 0 remaining shares, got %d", len(remainingShares))
	}
}
