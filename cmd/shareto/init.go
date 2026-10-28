package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"shareto/internal/config"
	"shareto/internal/service"
)

func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func readLineWithDefault(reader *bufio.Reader, prompt, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Printf("%s: ", prompt)
	}

	text, err := reader.ReadString('\n')
	if err != nil {
		return defaultValue
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return defaultValue
	}
	return text
}

func detectPreferredIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		// First pass: try finding global unicast IPv6
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() == nil && ipNet.IP.IsGlobalUnicast() {
					return ipNet.IP.String()
				}
			}
		}
		// Second pass: private IPv4
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() != nil && ipNet.IP.IsPrivate() {
					return ipNet.IP.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

func isPortAvailable(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func runInit() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("================================================================================")
	fmt.Println("  shareto Interactive Configuration Wizard")
	fmt.Println("================================================================================")
	fmt.Println("This wizard will guide you to configure shareto for local or public sharing.")
	fmt.Println()

	cfgPath := config.DefaultConfigPath()
	currentCfg, err := config.LoadOrInit()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// 1. Listen Address and Port
	defaultListen := currentCfg.ListenAddr
	if defaultListen == "127.0.0.1:38080" {
		defaultListen = "[::]:38080"
	}

	var listenAddr string
	for {
		listenAddr = readLineWithDefault(reader, "1. Listen address and port (e.g. [::]:38080, 0.0.0.0:38080, 127.0.0.1:38080)", defaultListen)
		if isPortAvailable(listenAddr) {
			fmt.Printf("   \033[32m✓ Port check passed for %s\033[0m\n", listenAddr)
			break
		}

		fmt.Printf("   \033[33m⚠ Warning: Port in '%s' is currently in use or cannot be bound.\033[0m\n", listenAddr)
		// Extract port and suggest next
		_, portStr, splitErr := net.SplitHostPort(listenAddr)
		if splitErr == nil {
			if p, err := strconv.Atoi(portStr); err == nil {
				nextListen := fmt.Sprintf("[::]:%d", p+1)
				if isPortAvailable(nextListen) {
					fmt.Printf("   Suggested alternative available: %s\n", nextListen)
					defaultListen = nextListen
				}
			}
		}
		confirm := readLineWithDefault(reader, "   Use this address anyway? (y/N)", "N")
		if strings.ToLower(confirm) == "y" {
			break
		}
	}
	fmt.Println()

	// 2. TLS / HTTPS configuration
	enableTLS := false
	certFile := currentCfg.TLS.CertFile
	keyFile := currentCfg.TLS.KeyFile

	tlsDefault := "N"
	if currentCfg.TLS.Enabled {
		tlsDefault = "y"
	}

	tlsChoice := readLineWithDefault(reader, "2. Enable native HTTPS / TLS encryption? (y/N)", tlsDefault)
	if strings.ToLower(tlsChoice) == "y" {
		enableTLS = true
		for {
			certFile = readLineWithDefault(reader, "   Certificate fullchain path (.crt/.pem)", certFile)
			keyFile = readLineWithDefault(reader, "   Private key path (.key/.pem)", keyFile)

			realCert := expandPath(certFile)
			realKey := expandPath(keyFile)

			tlsKeyPair, err := tls.LoadX509KeyPair(realCert, realKey)
			if err != nil {
				fmt.Printf("   \033[31m✗ Certificate validation failed: %v\033[0m\n", err)
				retry := readLineWithDefault(reader, "   Retry entering certificate paths? (Y/n)", "Y")
				if strings.ToLower(retry) == "n" {
					break
				}
				continue
			}

			// Parse x509 leaf for information
			if len(tlsKeyPair.Certificate) > 0 {
				leaf, err := x509.ParseCertificate(tlsKeyPair.Certificate[0])
				if err == nil {
					fmt.Printf("   \033[32m✓ Valid certificate loaded! CommonName: %s, Expires: %s\033[0m\n",
						leaf.Subject.CommonName, leaf.NotAfter.Format("2006-01-02"))
				}
			}
			break
		}
	}
	fmt.Println()

	// 3. Public Base URL
	_, port, _ := net.SplitHostPort(listenAddr)
	scheme := "http"
	if enableTLS {
		scheme = "https"
	}

	defaultPublic := currentCfg.PublicBaseURL
	if defaultPublic == "http://127.0.0.1:38080" || defaultPublic == "" {
		detected := detectPreferredIP()
		if strings.Contains(detected, ":") {
			// IPv6
			defaultPublic = fmt.Sprintf("%s://[%s]:%s", scheme, detected, port)
		} else {
			defaultPublic = fmt.Sprintf("%s://%s:%s", scheme, detected, port)
		}
	} else if enableTLS && strings.HasPrefix(defaultPublic, "http://") {
		// Auto upgrade scheme if TLS enabled
		defaultPublic = "https://" + strings.TrimPrefix(defaultPublic, "http://")
	}

	publicBaseURL := readLineWithDefault(reader, "3. Public Base URL (URL prefix printed for shared files)", defaultPublic)
	publicBaseURL = strings.TrimRight(publicBaseURL, "/")
	if publicBaseURL != "" && !strings.HasPrefix(publicBaseURL, "http://") && !strings.HasPrefix(publicBaseURL, "https://") {
		if enableTLS {
			publicBaseURL = "https://" + publicBaseURL
		} else {
			publicBaseURL = "http://" + publicBaseURL
		}
	}

	fmt.Println()

	// Apply into configuration
	currentCfg.ListenAddr = listenAddr
	currentCfg.PublicBaseURL = publicBaseURL
	currentCfg.TLS.Enabled = enableTLS
	currentCfg.TLS.CertFile = certFile
	currentCfg.TLS.KeyFile = keyFile

	if err := config.SaveConfig(cfgPath, currentCfg); err != nil {
		fmt.Printf("\033[31mFailed to save configuration: %v\033[0m\n", err)
		os.Exit(1)
	}

	fmt.Println("================================================================================")
	fmt.Printf("\033[32m[✓] Configuration successfully saved to: %s\033[0m\n", cfgPath)
	fmt.Println("================================================================================")
	fmt.Printf("  Listen Address:   %s\n", currentCfg.ListenAddr)
	fmt.Printf("  Public Base URL:  %s\n", currentCfg.PublicBaseURL)
	fmt.Printf("  TLS Encryption:   %v\n", currentCfg.TLS.Enabled)
	if currentCfg.TLS.Enabled {
		fmt.Printf("  Certificate File: %s\n", currentCfg.TLS.CertFile)
		fmt.Printf("  Private Key File: %s\n", currentCfg.TLS.KeyFile)
	}
	fmt.Println()

	active, err := service.Active()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query user service: %v\n", err)
		os.Exit(1)
	}
	if active {
		restartChoice := readLineWithDefault(reader, "The shareto user service is active. Restart it now? (Y/n)", "Y")
		if strings.ToLower(restartChoice) != "n" {
			if err := service.Restart(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to restart user service: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("[✓] User service restarted successfully.")
		}
	} else {
		fmt.Println(service.Instructions())
	}
}
