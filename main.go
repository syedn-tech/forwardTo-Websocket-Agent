package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// loadDotEnv attempts to read key=value pairs from a local .env file if it exists.
// Existing environment variables are never overwritten.
func loadDotEnv() {
	envFiles := []string{".env", "../.env"}
	for _, file := range envFiles {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				if os.Getenv(k) == "" {
					_ = os.Setenv(k, v)
				}
			}
		}
		break
	}
}

// Pusher protocol structures
type PusherEnvelope struct {
	Event   string          `json:"event"`
	Data    json.RawMessage `json:"data"`
	Channel string          `json:"channel,omitempty"`
}

type WebhookPayload struct {
	RequestID  int64               `json:"request_id"`
	Endpoint   string              `json:"endpoint"`
	Method     string              `json:"method"`
	Headers    map[string][]string `json:"headers"`
	Query      map[string]string   `json:"query"`
	Body       json.RawMessage     `json:"body"`
	ReceivedAt string              `json:"received_at"`
}

type VerifyResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Team struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"team"`
		Client struct {
			ID        int    `json:"id"`
			ClientID  string `json:"client_id"`
			Name      string `json:"name"`
			TargetURL string `json:"target_url"`
		} `json:"client"`
		Endpoint struct {
			ID   int    `json:"id"`
			Path string `json:"path"`
		} `json:"endpoint"`
		WebSocket struct {
			Host    string `json:"host"`
			Port    string `json:"port"`
			Scheme  string `json:"scheme"`
			AppKey  string `json:"app_key"`
			Channel string `json:"channel"`
		} `json:"websocket"`
	} `json:"data"`
}

func main() {
	loadDotEnv()

	// Resolve default server URL: check FORWARDTO_SERVER, then APP_URL from .env, then fallback
	defaultServer := getEnv("FORWARDTO_SERVER", getEnv("APP_URL", "https://forwardto.cloud"))

	var (
		serverURL = flag.String("server", defaultServer, "ForwardTo server base URL")
		teamKey   = flag.String("team-key", getEnv("FORWARDTO_TEAM_KEY", getEnv("FORWARDTO_TEAM_ID", "")), "ForwardTo Team API Key or Slug (required)")
		clientID  = flag.String("client-id", getEnv("FORWARDTO_CLIENT_ID", ""), "Client Machine ID registered in ForwardTo (required)")
		token     = flag.String("token", getEnv("FORWARDTO_TOKEN", ""), "Client Secret Token (required)")
		overrideTarget = flag.String("target", getEnv("TARGET_URL", ""), "Optional override for local target URL (defaults to server config)")
	)
	flag.Parse()

	// Prompt interactively if missing required flags
	if *teamKey == "" {
		fmt.Print("Enter Team Key / API Key: ")
		fmt.Scanln(teamKey)
	}
	if *clientID == "" {
		fmt.Print("Enter Client ID: ")
		fmt.Scanln(clientID)
	}
	if *token == "" {
		fmt.Print("Enter Client Secret Token: ")
		fmt.Scanln(token)
	}

	if *teamKey == "" || *clientID == "" || *token == "" {
		log.Fatal("ERROR: --team-key, --client-id, and --token are required to start forwardto-agent.")
	}

	cleanServerURL := strings.TrimRight(*serverURL, "/")

	log.Println("==================================================")
	log.Println("               FORWARDTO RELAY AGENT              ")
	log.Println("==================================================")
	log.Printf("[*] Verifying client '%s' on %s...", *clientID, cleanServerURL)

	verifyData, err := verifyCredentials(cleanServerURL, *teamKey, *clientID, *token)
	if err != nil {
		log.Fatalf("[!] Verification failed: %v", err)
	}

	// Use target from server, unless user passed explicit local CLI override
	activeTarget := strings.TrimSpace(verifyData.Data.Client.TargetURL)
	if *overrideTarget != "" {
		activeTarget = strings.TrimSpace(*overrideTarget)
	}

	if activeTarget == "" {
		log.Println("--------------------------------------------------")
		log.Println("[!] ERROR: Target URL is not configured.")
		log.Printf("    Please configure the Target Forward URL for client '%s'", *clientID)
		log.Println("    in ForwardTo Endpoint Settings or specify it using --target=http://... or in .env")
		log.Println("--------------------------------------------------")
		os.Exit(1)
	}

	log.Printf("[✓] Verified successfully!")
	log.Printf("    Team:     %s (ID: %d)", verifyData.Data.Team.Name, verifyData.Data.Team.ID)
	log.Printf("    Client:   %s", verifyData.Data.Client.ClientID)
	log.Printf("    Endpoint: /%s", verifyData.Data.Endpoint.Path)
	log.Printf("    Target:   %s (loaded from ForwardTo server)", activeTarget)
	log.Printf("    Channel:  %s", verifyData.Data.WebSocket.Channel)
	log.Println("==================================================")

	wsScheme := verifyData.Data.WebSocket.Scheme
	if wsScheme == "https" {
		wsScheme = "wss"
	} else if wsScheme == "http" {
		wsScheme = "ws"
	}
	wsHost := verifyData.Data.WebSocket.Host
	wsPort := verifyData.Data.WebSocket.Port
	appKey := verifyData.Data.WebSocket.AppKey
	channel := verifyData.Data.WebSocket.Channel

	var wsURL string
	if (wsScheme == "wss" && wsPort == "443") || (wsScheme == "ws" && wsPort == "80") || wsPort == "" {
		wsURL = fmt.Sprintf("%s://%s/app/%s?protocol=7&client=js&version=8.4.0-reverb&flash=false", wsScheme, wsHost, appKey)
	} else {
		wsURL = fmt.Sprintf("%s://%s:%s/app/%s?protocol=7&client=js&version=8.4.0-reverb&flash=false", wsScheme, wsHost, wsPort, appKey)
	}

	// Listen for Ctrl+C (SIGINT) and SIGTERM in a background listener that exits cleanly immediately
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interrupt
		log.Println("\n[*] Shutting down forwardto-agent...")
		os.Exit(0)
	}()

	targetHolder := &TargetHolder{URL: activeTarget}

	for {
		log.Printf("[*] Connecting to WebSocket: %s://%s...", wsScheme, wsHost)
		err := runWebSocket(cleanServerURL, *token, wsURL, channel, targetHolder)
		if err != nil {
			log.Printf("[!] Disconnected: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
		} else {
			return
		}
	}
}

type TargetHolder struct {
	URL string
}

func (t *TargetHolder) Get() string {
	return t.URL
}

func (t *TargetHolder) Set(newURL string) {
	t.URL = newURL
}

func verifyCredentials(serverURL, teamKey, clientID, token string) (*VerifyResponse, error) {
	verifyEndpoint := fmt.Sprintf("%s/api/agent/verify", serverURL)

	payload := map[string]string{
		"api_key":   teamKey,
		"team_id":   teamKey,
		"client_id": clientID,
		"token":     token,
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", verifyEndpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Create HTTP client that allows local development certificates (like forwardto.test / Herd / Valet)
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: strings.HasSuffix(serverURL, ".test") || strings.Contains(serverURL, "localhost") || strings.Contains(serverURL, "127.0.0.1")},
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		if errResp.Message != "" {
			return nil, fmt.Errorf("%s (HTTP %d)", errResp.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var res VerifyResponse
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, fmt.Errorf("invalid response json: %w", err)
	}

	return &res, nil
}

func runWebSocket(serverURL, token, wsURL, channel string, targetHolder *TargetHolder) error {
	u, err := url.Parse(wsURL)
	if err != nil {
		return err
	}

	dialer := websocket.DefaultDialer
	if u.Scheme == "wss" && (strings.HasSuffix(u.Host, ".test") || strings.Contains(u.Host, "localhost") || strings.Contains(u.Host, "127.0.0.1")) {
		dialer = &websocket.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("[✓] Connected to WebSocket! Subscribing to channel '%s'...", channel)

	subscribeMsg := map[string]interface{}{
		"event": "pusher:subscribe",
		"data": map[string]string{
			"channel": channel,
		},
	}
	if err := conn.WriteJSON(subscribeMsg); err != nil {
		return err
	}

	// Keepalive ping ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[!] Read error: %v", err)
				return
			}

			var env PusherEnvelope
			if err := json.Unmarshal(msg, &env); err != nil {
				continue
			}

			if env.Event == "pusher:ping" {
				_ = conn.WriteJSON(map[string]string{"event": "pusher:pong", "data": ""})
				continue
			}

			if env.Event == "webhook.received" || strings.HasSuffix(env.Event, "WebhookReceived") {
				// Check if it's a dynamic config update from ForwardTo server
				var systemCheck struct {
					SystemEvent string `json:"_system_event"`
					TargetURL   string `json:"target_url"`
				}

				if len(env.Data) > 0 && env.Data[0] == '"' {
					var unquoted string
					if err := json.Unmarshal(env.Data, &unquoted); err == nil {
						_ = json.Unmarshal([]byte(unquoted), &systemCheck)
					}
				} else {
					_ = json.Unmarshal(env.Data, &systemCheck)
				}

				if systemCheck.SystemEvent == "client.config_updated" && systemCheck.TargetURL != "" {
					targetHolder.Set(systemCheck.TargetURL)
					log.Printf("[⟳ CONFIG UPDATED] Target URL updated on-the-fly to: %s", systemCheck.TargetURL)
					continue
				}

				var payload WebhookPayload
				if len(env.Data) > 0 && env.Data[0] == '"' {
					var unquoted string
					if err := json.Unmarshal(env.Data, &unquoted); err == nil {
						_ = json.Unmarshal([]byte(unquoted), &payload)
					}
				} else {
					_ = json.Unmarshal(env.Data, &payload)
				}

				// Forward locally in a non-blocking goroutine so WebSocket loop has 0ms latency
				go forwardLocally(serverURL, token, targetHolder.Get(), payload)
			}
		}
	}()

	for {
		select {
		case <-done:
			return fmt.Errorf("connection closed")
		case <-ticker.C:
			if err := conn.WriteJSON(map[string]string{"event": "pusher:ping", "data": ""}); err != nil {
				return err
			}
		}
	}
}

func forwardLocally(serverURL, token, targetURL string, payload WebhookPayload) {
	method := payload.Method
	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequest(method, targetURL, bytes.NewBuffer(payload.Body))
	if err != nil {
		log.Printf("[!] Local request construction failed: %v", err)
		go reportDelivery(serverURL, token, payload.RequestID, "FAILED", 0, targetURL, method, "", fmt.Sprintf("Request build failed: %v", err))
		return
	}

	for k, vals := range payload.Headers {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("X-ForwardTo-Relay", "forwardto-agent")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[!] Dispatch to %s failed: %v", targetURL, err)
		go reportDelivery(serverURL, token, payload.RequestID, "FAILED", 0, targetURL, method, "", fmt.Sprintf("Dispatch failed: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	respStr := string(respBody)
	if len(respStr) > 5000 {
		respStr = respStr[:5000] + "... (truncated)"
	}

	status := "SUCCEEDED"
	if resp.StatusCode >= 400 {
		status = "FAILED"
	}

	log.Printf("[✓ DELIVERED] HTTP %d <- %s (req #%d)", resp.StatusCode, targetURL, payload.RequestID)

	// Asynchronously notify ForwardTo of delivery receipt so user sees it in the dashboard UI
	go reportDelivery(serverURL, token, payload.RequestID, status, resp.StatusCode, targetURL, method, respStr, "")
}

func reportDelivery(serverURL, token string, requestID int64, status string, httpStatus int, targetURL, method, respBody, errMsg string) {
	reportEndpoint := fmt.Sprintf("%s/api/agent/delivery-report", serverURL)

	payload := map[string]interface{}{
		"token":         token,
		"request_id":    requestID,
		"status":        status,
		"http_status":   httpStatus,
		"target_url":    targetURL,
		"method":        method,
		"response_body": respBody,
		"error_message": errMsg,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: strings.HasSuffix(serverURL, ".test") || strings.Contains(serverURL, "localhost") || strings.Contains(serverURL, "127.0.0.1")},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	req, err := http.NewRequest("POST", reportEndpoint, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
