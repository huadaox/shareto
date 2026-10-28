//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"shareto/internal/ipc"
)

const taskName = "shareto"

func Active() (bool, error) {
	client := ipc.NewClient()
	return client.Ping(), nil
}

func Start() error {
	client := ipc.NewClient()
	if client.Ping() {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Prepare log directory: %LOCALAPPDATA%\shareto\logs
	logDir := filepath.Join(filepath.Dir(ipc.DefaultSocketPath()), "logs")
	_ = os.MkdirAll(logDir, 0700)
	stdoutLog, _ := os.OpenFile(filepath.Join(logDir, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	stderrLog, _ := os.OpenFile(filepath.Join(logDir, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)

	cmd := exec.Command(exe, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000 | 0x00000200, // CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP
	}
	if stdoutLog != nil {
		cmd.Stdout = stdoutLog
	}
	if stderrLog != nil {
		cmd.Stderr = stderrLog
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start background daemon: %w", err)
	}

	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}

	// Poll until daemon is responsive
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if client.Ping() {
			return nil
		}
	}

	return fmt.Errorf("daemon process started but did not respond to IPC within 5s (check %s/stderr.log)", logDir)
}

func Stop() error {
	client := ipc.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := client.Status(ctx)
	if err == nil && status.PID > 0 && status.PID != os.Getpid() {
		_ = runCommand("taskkill", "/F", "/PID", fmt.Sprintf("%d", status.PID))
	} else {
		_ = runCommand("schtasks", "/end", "/tn", taskName)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !client.Ping() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

func Restart() error {
	_ = Stop()
	time.Sleep(500 * time.Millisecond)
	return Start()
}

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// 1. Write daemon.vbs in config directory (%APPDATA%\shareto\daemon.vbs)
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	sharetoDir := filepath.Join(appData, "shareto")
	_ = os.MkdirAll(sharetoDir, 0700)

	vbsPath := filepath.Join(sharetoDir, "daemon.vbs")
	vbsContent := fmt.Sprintf(`CreateObject("WScript.Shell").Run """%s"" daemon", 0, False`+"\r\n", exe)
	if err := os.WriteFile(vbsPath, []byte(vbsContent), 0600); err != nil {
		return fmt.Errorf("failed to write daemon.vbs: %w", err)
	}

	// 2. Register in HKCU\Software\Microsoft\Windows\CurrentVersion\Run
	regValue := fmt.Sprintf(`wscript.exe //B "%s"`, vbsPath)
	if err := runCommand("reg", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "shareto", "/t", "REG_SZ", "/d", regValue, "/f"); err != nil {
		// Fallback: write to Startup folder
		startupDir := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		_ = os.MkdirAll(startupDir, 0700)
		_ = os.WriteFile(filepath.Join(startupDir, "shareto.vbs"), []byte(vbsContent), 0600)
	}

	// 3. Optional: Try schtasks in case elevated, ignore failure
	taskRun := fmt.Sprintf(`"%s" daemon`, exe)
	_ = runCommand("schtasks", "/create", "/tn", taskName, "/tr", taskRun, "/sc", "onlogon", "/f")

	// 4. Start daemon immediately
	return Start()
}

func Uninstall() error {
	_ = Stop()
	_ = runCommand("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "shareto", "/f")
	appData := os.Getenv("APPDATA")
	if appData != "" {
		_ = os.Remove(filepath.Join(appData, "shareto", "daemon.vbs"))
		startupDir := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		_ = os.Remove(filepath.Join(startupDir, "shareto.vbs"))
	}
	_ = runCommand("schtasks", "/delete", "/tn", taskName, "/f")
	return nil
}

func Instructions() string {
	return "Run 'shareto service install' to enable automatic startup at user logon, or 'shareto daemon' to run in the foreground."
}
