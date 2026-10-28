package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"shareto/internal/config"
	"shareto/internal/crypto"
	"shareto/internal/httpd"
	"shareto/internal/ipc"
	"shareto/internal/service"
	"shareto/internal/store"

	"golang.org/x/term"
)

var Version = "3.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	firstArg := os.Args[1]

	switch firstArg {
	case "init":
		runInit()
	case "daemon":
		runDaemon()
	case "list":
		runList()
	case "revoke":
		runRevoke()
	case "status":
		runStatus()
	case "service":
		runService()
	case "help", "-h", "--help":
		printUsage()
	case "version", "-v", "--version":
		fmt.Printf("shareto version %s\n", Version)
	default:
		// Default to share command
		runShare(os.Args[1:])
	}
}

func printUsage() {
	fmt.Printf(`shareto v%s - Safe and minimal large file distribution tool for Linux and macOS

Usage:
  shareto [flags] <file_path>  Share a file (default command)
  shareto init                 Run interactive setup wizard
  shareto list                 List all active shares
  shareto revoke <token|file>  Revoke an active share by token or file path
  shareto status               Check daemon status
  shareto service <action>     Install, start, stop, restart or uninstall the user service
  shareto daemon               Start the daemon in the foreground

Share Flags:
  -t string          Expiration duration (e.g. 30m, 12h, 7d; default "24h")
  -m int             Max download sessions allowed (1-1000; default 10)
  -p                 Prompt for password protection
  --password-stdin   Read password from standard input
  --base-url string  Override public base URL (e.g. http://your-domain.com:38080)
`, Version)
}

func parseDurationCustom(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.ParseFloat(daysStr, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func checkAndEnsureDaemonRunning() *ipc.Client {
	client := ipc.NewClient()
	if client.Ping() {
		return client
	}

	if err := service.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start user service: %v\n", err)
		return nil
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if client.Ping() {
			return client
		}
	}

	return nil
}

func checkLingerWarning() {
	if runtime.GOOS != "linux" {
		return
	}
	currUser, err := user.Current()
	if err != nil {
		return
	}
	lingerPath := filepath.Join("/var/lib/systemd/linger", currUser.Username)
	if _, err := os.Stat(lingerPath); os.IsNotExist(err) {
		// Output yellow warning
		fmt.Printf("\033[33m[WARN] Systemd user lingering is NOT enabled. Shares will stop when you log out of SSH.\033[0m\n")
		fmt.Printf("\033[33m       Run once to fix: sudo loginctl enable-linger %s\033[0m\n\n", currUser.Username)
	}
}

func runShare(args []string) {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	durationStr := fs.String("t", "24h", "Expiration duration")
	maxDownloads := fs.Int("m", 10, "Max download sessions")
	promptPass := fs.Bool("p", false, "Enable password protection")
	stdinPass := fs.Bool("password-stdin", false, "Read password from stdin")
	baseURLFlag := fs.String("base-url", "", "Override public base URL")

	_ = fs.Parse(args)
	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Error: missing file path to share.")
		printUsage()
		os.Exit(1)
	}

	publicBaseURL := *baseURLFlag
	if publicBaseURL == "" {
		publicBaseURL = os.Getenv("SHARETO_PUBLIC_BASE_URL")
	}

	filePath := remaining[0]
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving path: %v\n", err)
		os.Exit(1)
	}

	dur, err := parseDurationCustom(*durationStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing duration '%s': %v\n", *durationStr, err)
		os.Exit(1)
	}
	if dur < 1*time.Minute || dur > 720*time.Hour {
		fmt.Fprintln(os.Stderr, "Error: duration must be between 1m and 720h (30d).")
		os.Exit(1)
	}

	if *maxDownloads < 1 || *maxDownloads > 1000 {
		fmt.Fprintln(os.Stderr, "Error: max downloads must be between 1 and 1000.")
		os.Exit(1)
	}

	var password string
	if *stdinPass {
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		password = strings.TrimRight(line, "\r\n")
	} else if *promptPass {
		if envPass := os.Getenv("SHARETO_PASSWORD"); envPass != "" {
			password = envPass
		} else {
			fmt.Print("Enter share password: ")
			passBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to read password: %v\n", err)
				os.Exit(1)
			}
			password = string(passBytes)
		}
	}

	var passwordHash string
	if password != "" {
		hash, err := crypto.HashPassword(password)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to hash password: %v\n", err)
			os.Exit(1)
		}
		passwordHash = hash
	}

	client := checkAndEnsureDaemonRunning()
	if client == nil {
		fmt.Fprintln(os.Stderr, "Error: shareto daemon is not available.")
		fmt.Fprintln(os.Stderr, service.Instructions())
		os.Exit(1)
	}

	checkLingerWarning()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Share(ctx, ipc.ShareRequest{
		FilePath:      absPath,
		ExpiresSec:    int64(dur.Seconds()),
		MaxDownloads:  *maxDownloads,
		PasswordHash:  passwordHash,
		PublicBaseURL: publicBaseURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create share: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("File successfully shared!")
	fmt.Printf("  Token:      %s\n", resp.Token)
	fmt.Printf("  Public URL: %s\n", resp.URL)
	if password == "" {
		fmt.Printf("  Direct URL: %s\n", resp.RawURL)
	}
	fmt.Printf("  Expires:    %s\n", resp.ExpiresAt)
	fmt.Printf("  Max Claims: %d\n", *maxDownloads)
	if password != "" {
		fmt.Println("  Security:   Password protected")
	}

	if strings.Contains(resp.URL, "127.0.0.1") || strings.Contains(resp.URL, "localhost") {
		fmt.Printf("\n\033[33m[Notice] URL uses loopback address (127.0.0.1) and cannot be opened from other devices.\033[0m\n")
		fmt.Printf("\033[33m         Run 'shareto init' to configure your public domain, port and HTTPS certificate.\033[0m\n")
		fmt.Printf("\033[33m         Or specify temporarily: shareto --base-url http://your-domain:port <file>\033[0m\n")
	}
}

func runList() {
	client := ipc.NewClient()
	if !client.Ping() {
		fmt.Fprintln(os.Stderr, "Error: shareto daemon is not running.")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shares, err := client.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query shares: %v\n", err)
		os.Exit(1)
	}

	if len(shares) == 0 {
		fmt.Println("No active shares.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TOKEN\tFILE\tSIZE\tREMAINING\tSTREAMS\tAUTH\tEXPIRES")
	for _, s := range shares {
		tokenDisplay := s.Token
		if len(tokenDisplay) > 10 {
			tokenDisplay = tokenDisplay[:8] + ".."
		}
		authDisplay := "None"
		if s.HasPassword {
			authDisplay = "Password"
		}
		remainingDisplay := fmt.Sprintf("%d/%d", s.RemainingDownloads, s.MaxDownloads)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			tokenDisplay,
			s.FileName,
			httpd.FormatBytes(s.FileSize),
			remainingDisplay,
			s.ActiveStreams,
			authDisplay,
			s.ExpiresAt,
		)
	}
	_ = w.Flush()
}

func runRevoke() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: shareto revoke <token|token_prefix|file_path>")
		os.Exit(1)
	}
	target := os.Args[2]

	client := ipc.NewClient()
	if !client.Ping() {
		fmt.Fprintln(os.Stderr, "Error: shareto daemon is not running.")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := client.Revoke(ctx, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Revoke failed: %v\n", err)
		os.Exit(1)
	}

	if len(res.RevokedTokens) == 0 {
		fmt.Printf("No matching shares found for target '%s'.\n", target)
	} else {
		fmt.Printf("Successfully revoked %d share(s):\n", len(res.RevokedTokens))
		for _, t := range res.RevokedTokens {
			fmt.Printf(" - %s\n", t)
		}
	}
}

func runStatus() {
	client := ipc.NewClient()
	if !client.Ping() {
		fmt.Println("shareto daemon: NOT RUNNING")
		checkLingerWarning()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.Status(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get daemon status: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("shareto daemon: ACTIVE (PID %d)\n", status.PID)
	fmt.Printf("  Version:        %s\n", status.Version)
	fmt.Printf("  Uptime:         %s\n", (time.Duration(status.UptimeSec) * time.Second).String())
	fmt.Printf("  Active Shares:  %d\n", status.ActiveSharesCount)
	fmt.Printf("  Active Streams: %d\n", status.GlobalActiveConns)
	if runtime.GOOS == "darwin" {
		fmt.Println("  User Service:   launchd LaunchAgent (active while logged in)")
		return
	}
	if runtime.GOOS == "windows" {
		fmt.Println("  User Service:   Windows Task Scheduler (active while logged in)")
		return
	}

	currUser, err := user.Current()
	if err == nil {
		lingerPath := filepath.Join("/var/lib/systemd/linger", currUser.Username)
		if _, err := os.Stat(lingerPath); err == nil {
			fmt.Println("  Linger Status:  Enabled")
		} else {
			fmt.Println("  Linger Status:  Disabled")
			checkLingerWarning()
		}
	}
}

func runService() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "Usage: shareto service <install|start|stop|restart|uninstall|status>")
		os.Exit(1)
	}
	var err error
	switch os.Args[2] {
	case "install":
		err = service.Install()
	case "start":
		err = service.Start()
	case "stop":
		err = service.Stop()
	case "restart":
		err = service.Restart()
	case "uninstall":
		err = service.Uninstall()
	case "status":
		var active bool
		active, err = service.Active()
		if err == nil {
			fmt.Printf("User service loaded: %t\n", active)
			return
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown service action: %s\n", os.Args[2])
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "User service operation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("User service %s completed.\n", os.Args[2])
}

func runDaemon() {
	cfg, err := config.LoadOrInit()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	storeMgr, err := store.NewManager(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize store manager: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Periodic purge loop
	storeMgr.StartCleanupLoop(ctx, 1*time.Minute)

	// HTTP server
	httpSrv, err := httpd.NewServer(cfg, storeMgr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create HTTP server: %v\n", err)
		os.Exit(1)
	}

	// IPC server
	ipcSrv, err := ipc.NewServer(cfg, storeMgr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create IPC server: %v\n", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 2)

	go func() {
		fmt.Printf("Starting HTTP server on %s (public base: %s)\n", cfg.ListenAddr, cfg.PublicBaseURL)
		if err := httpSrv.Start(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	go func() {
		fmt.Printf("Starting IPC server on %s\n", ipc.DefaultSocketPath())
		if err := ipcSrv.Start(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("IPC server error: %w", err)
		}
	}()

	select {
	case sig := <-sigCh:
		fmt.Printf("Received signal %s, gracefully shutting down...\n", sig)
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "Fatal server error: %v\n", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	_ = httpSrv.Stop(shutdownCtx)
	_ = ipcSrv.Stop(shutdownCtx)
	_ = storeMgr.SaveState()

	fmt.Println("Daemon stopped cleanly.")
}
