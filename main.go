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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Version is injected via -ldflags during build/release or defaults to local dev
var Version = "0.2.0"

const (
	githubOwner = "syedn-tech"
	githubRepo  = "forwardTo-Websocket-Agent"
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

type ForwardRule struct {
	ID                 int64             `json:"id"`
	Type               string            `json:"type"` // HTTP Method (GET, POST, PUT, etc.)
	URL                string            `json:"url"`  // Forward target URL
	ParameterType      *string           `json:"parameter_type"`
	ConditionKey       *string           `json:"condition_key"`
	ConditionSeperator *string           `json:"condition_seperator"`
	ConditionValue     *string           `json:"condition_value"`
	Headers            map[string]string `json:"headers"`
	Active             bool              `json:"active"`
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
		Rules     []ForwardRule `json:"rules"`
		WebSocket struct {
			Host    string `json:"host"`
			Port    string `json:"port"`
			Scheme  string `json:"scheme"`
			AppKey  string `json:"app_key"`
			Channel string `json:"channel"`
		} `json:"websocket"`
	} `json:"data"`
}

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type ClientConfig struct {
	ClientID string
	Token    string
}

// ConfigHolder safely stores dynamic target URL and active forwarding rules for a client
type ConfigHolder struct {
	mu        sync.RWMutex
	clientID  string
	targetURL string
	rules     []ForwardRule
}

func (c *ConfigHolder) GetTargetURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.targetURL
}

func (c *ConfigHolder) SetTargetURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targetURL = url
}

func (c *ConfigHolder) GetRules() []ForwardRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	copied := make([]ForwardRule, len(c.rules))
	copy(copied, c.rules)
	return copied
}

func (c *ConfigHolder) SetRules(rules []ForwardRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = rules
}

func parseClients(clientsFlag, clientIDFlag, tokenFlag string) []ClientConfig {
	var result []ClientConfig
	seen := make(map[string]bool)

	// 1. Check clientsFlag (e.g. "billing:tok1, auth:tok2" or "billing, auth")
	if strings.TrimSpace(clientsFlag) != "" {
		parts := strings.Split(clientsFlag, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			var id, tok string
			if strings.Contains(part, ":") {
				sub := strings.SplitN(part, ":", 2)
				id = strings.TrimSpace(sub[0])
				tok = strings.TrimSpace(sub[1])
			} else {
				id = part
			}
			if id != "" && !seen[id] {
				seen[id] = true
				result = append(result, ClientConfig{ClientID: id, Token: tok})
			}
		}
	}

	// 2. Check clientIDFlag and tokenFlag (comma-separated or single)
	if strings.TrimSpace(clientIDFlag) != "" {
		ids := strings.Split(clientIDFlag, ",")
		var toks []string
		if strings.TrimSpace(tokenFlag) != "" {
			toks = strings.Split(tokenFlag, ",")
		}

		for i, rawID := range ids {
			id := strings.TrimSpace(rawID)
			if id == "" || seen[id] {
				continue
			}
			var tok string
			if i < len(toks) {
				tok = strings.TrimSpace(toks[i])
			} else if len(toks) == 1 {
				tok = strings.TrimSpace(toks[0])
			}
			seen[id] = true
			result = append(result, ClientConfig{ClientID: id, Token: tok})
		}
	}

	return result
}

func main() {
	loadDotEnv()

	// Default server URL: default to forwardto.cloud, but if FORWARDTO_SERVER or APP_URL is in .env / environment, use that
	defaultServer := getEnv("FORWARDTO_SERVER", getEnv("APP_URL", "https://forwardto.cloud"))

	var (
		serverURL   = flag.String("server", defaultServer, "ForwardTo server base URL")
		teamKey     = flag.String("team-key", getEnv("FORWARDTO_TEAM_KEY", getEnv("FORWARDTO_TEAM_ID", "")), "ForwardTo Team API Key or Slug (required)")
		clientsFlag = flag.String("clients", getEnv("FORWARDTO_CLIENTS", ""), "Comma-separated list of clients in 'client_id:token' or 'client_id' format")
		clientID    = flag.String("client-id", getEnv("FORWARDTO_CLIENT_ID", ""), "Client Machine ID(s), comma-separated")
		token       = flag.String("token", getEnv("FORWARDTO_TOKEN", ""), "Client Secret Token(s), comma-separated")
		checkUpdate = flag.Bool("update", false, "Check for updates and self-update to latest release")
		showVersion = flag.Bool("version", false, "Print forwardto-agent version")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("forwardto-agent %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	if *checkUpdate {
		if err := executeSelfUpdate(true); err != nil {
			log.Fatalf("[!] Update failed: %v", err)
		}
		return
	}

	// Non-blocking background check for latest version on startup
	go checkLatestVersionNotice()

	clients := parseClients(*clientsFlag, *clientID, *token)

	// Prompt interactively if missing required arguments
	if *teamKey == "" {
		fmt.Print("Enter Team Key / API Key: ")
		fmt.Scanln(teamKey)
	}

	if len(clients) == 0 {
		var inputID, inputToken string
		fmt.Print("Enter Client ID: ")
		fmt.Scanln(&inputID)
		fmt.Print("Enter Client Secret Token: ")
		fmt.Scanln(&inputToken)
		if strings.TrimSpace(inputID) != "" {
			clients = append(clients, ClientConfig{
				ClientID: strings.TrimSpace(inputID),
				Token:    strings.TrimSpace(inputToken),
			})
		}
	}

	if *teamKey == "" || len(clients) == 0 {
		log.Fatal("ERROR: --team-key and at least one client (via --client-id/--clients) are required to start forwardto-agent.")
	}

	cleanServerURL := strings.TrimRight(*serverURL, "/")

	log.Println("==================================================")
	log.Printf("           FORWARDTO RELAY AGENT (v%s)           \n", Version)
	log.Println("==================================================")
	log.Printf("[*] Initializing %d client worker(s)...", len(clients))

	// Listen for Ctrl+C (SIGINT) and SIGTERM in a background listener that exits cleanly immediately
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interrupt
		log.Println("\n[*] Shutting down forwardto-agent...")
		os.Exit(0)
	}()

	var wg sync.WaitGroup

	for _, client := range clients {
		wg.Add(1)
		go func(cfg ClientConfig) {
			defer wg.Done()
			runClientWorker(cleanServerURL, *teamKey, cfg)
		}(client)
	}

	wg.Wait()
}

func runClientWorker(serverURL, teamKey string, cfg ClientConfig) {
	prefix := fmt.Sprintf("[%s]", cfg.ClientID)
	log.Printf("%s Verifying credentials on %s...", prefix, serverURL)

	for {
		verifyData, err := verifyCredentials(serverURL, teamKey, cfg.ClientID, cfg.Token)
		if err != nil {
			log.Printf("%s [!] Verification failed: %v. Retrying in 10s...", prefix, err)
			time.Sleep(10 * time.Second)
			continue
		}

		activeTarget := strings.TrimSpace(verifyData.Data.Client.TargetURL)
		if activeTarget == "" {
			log.Printf("%s [!] ERROR: Target URL is not configured in dashboard. Retrying in 15s...", prefix)
			time.Sleep(15 * time.Second)
			continue
		}

		configHolder := &ConfigHolder{
			clientID:  cfg.ClientID,
			targetURL: activeTarget,
			rules:     verifyData.Data.Rules,
		}

		log.Printf("%s [✓] Verified! Endpoint: /%s | Default URL: %s | Rules: %d",
			prefix,
			verifyData.Data.Endpoint.Path,
			activeTarget,
			len(verifyData.Data.Rules),
		)

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

		log.Printf("%s Connecting to WebSocket: %s://%s (channel: %s)...", prefix, wsScheme, wsHost, channel)
		err = runWebSocket(serverURL, cfg.Token, wsURL, channel, configHolder)
		if err != nil {
			log.Printf("%s [!] Disconnected: %v. Reconnecting in 5s...", prefix, err)
			time.Sleep(5 * time.Second)
		}
	}
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

func runWebSocket(serverURL, token, wsURL, channel string, configHolder *ConfigHolder) error {
	prefix := fmt.Sprintf("[%s]", configHolder.clientID)

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

	log.Printf("%s [✓] Connected to WebSocket! Subscribed to '%s'", prefix, channel)

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
				log.Printf("%s [!] Read error: %v", prefix, err)
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
				// 1. Check for dynamic config/rules updates
				var systemCheck struct {
					SystemEvent string        `json:"_system_event"`
					TargetURL   string        `json:"target_url"`
					Rules       []ForwardRule `json:"rules"`
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
					configHolder.SetTargetURL(systemCheck.TargetURL)
					log.Printf("%s [⟳ CONFIG UPDATED] Target URL updated on-the-fly to: %s", prefix, systemCheck.TargetURL)
					continue
				}

				if systemCheck.SystemEvent == "rules.updated" {
					configHolder.SetRules(systemCheck.Rules)
					log.Printf("%s [⟳ RULES UPDATED] Forwarding rules synchronized (%d active rules)", prefix, len(systemCheck.Rules))
					continue
				}

				// 2. Parse incoming webhook payload
				var payload WebhookPayload
				if len(env.Data) > 0 && env.Data[0] == '"' {
					var unquoted string
					if err := json.Unmarshal(env.Data, &unquoted); err == nil {
						_ = json.Unmarshal([]byte(unquoted), &payload)
					}
				} else {
					_ = json.Unmarshal(env.Data, &payload)
				}

				// 3. Client-side Rule Matching
				rules := configHolder.GetRules()
				matchedRule := matchRuleLocally(rules, payload)

				defaultTarget := configHolder.GetTargetURL()

				// Forward locally in a non-blocking goroutine so WebSocket loop has 0ms latency
				go forwardLocally(serverURL, token, configHolder.clientID, defaultTarget, payload, matchedRule)
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

// matchRuleLocally evaluates client-side rules against the incoming webhook payload.
// Returns the first matching rule, or nil if no rules matched.
func matchRuleLocally(rules []ForwardRule, payload WebhookPayload) *ForwardRule {
	if len(rules) == 0 {
		return nil
	}

	// Parse JSON body once if possible
	var parsedBody map[string]interface{}
	if len(payload.Body) > 0 {
		_ = json.Unmarshal(payload.Body, &parsedBody)
	}

	for _, rule := range rules {
		if !rule.Active {
			continue
		}
		if rule.ConditionKey == nil || strings.TrimSpace(*rule.ConditionKey) == "" {
			continue
		}

		condKey := strings.TrimSpace(*rule.ConditionKey)
		paramType := ""
		if rule.ParameterType != nil {
			paramType = strings.ToUpper(strings.TrimSpace(*rule.ParameterType))
		}

		var (
			actualVal interface{}
			found     bool
		)

		switch paramType {
		case "QUERY_PARAMS", "QUERY":
			val, ok := payload.Query[condKey]
			if ok {
				actualVal = val
				found = true
			}
		case "BODY":
			if parsedBody != nil {
				actualVal, found = getNestedValue(parsedBody, condKey)
			}
		default:
			// Search both body and query params
			if parsedBody != nil {
				actualVal, found = getNestedValue(parsedBody, condKey)
			}
			if !found {
				val, ok := payload.Query[condKey]
				if ok {
					actualVal = val
					found = true
				}
			}
		}

		if !found {
			continue
		}

		if evaluateCondition(actualVal, rule.ConditionValue, rule.ConditionSeperator) {
			r := rule
			return &r
		}
	}

	return nil
}

// getNestedValue retrieves a key using dot notation (e.g., "data.customer.id")
func getNestedValue(data interface{}, path string) (interface{}, bool) {
	if data == nil || path == "" {
		return nil, false
	}
	parts := strings.Split(path, ".")
	var current interface{} = data

	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		val, exists := m[part]
		if !exists {
			return nil, false
		}
		current = val
	}
	return current, true
}

// evaluateCondition evaluates whether the extracted value satisfies the rule condition
func evaluateCondition(actual interface{}, expected *string, separator *string) bool {
	if expected == nil {
		return actual == nil
	}
	expectedStr := *expected
	sep := ""
	if separator != nil {
		sep = strings.TrimSpace(*separator)
	}

	// Case: contains operator
	if strings.EqualFold(sep, "contains") {
		if actual == nil {
			return false
		}
		if arr, ok := actual.([]interface{}); ok {
			for _, item := range arr {
				if fmt.Sprintf("%v", item) == expectedStr {
					return true
				}
			}
			return false
		}
		return strings.Contains(fmt.Sprintf("%v", actual), expectedStr)
	}

	// Case: delimiter separation (e.g. ",", "|", ";")
	if sep != "" && sep != "=" && sep != "==" {
		if strVal, ok := actual.(string); ok {
			items := strings.Split(strVal, sep)
			for _, it := range items {
				if strings.TrimSpace(it) == expectedStr {
					return true
				}
			}
			return false
		}
		if arr, ok := actual.([]interface{}); ok {
			for _, item := range arr {
				if fmt.Sprintf("%v", item) == expectedStr {
					return true
				}
			}
			return false
		}
		return false
	}

	// Case: array value comparison
	if arr, ok := actual.([]interface{}); ok {
		for _, item := range arr {
			if fmt.Sprintf("%v", item) == expectedStr {
				return true
			}
		}
		return false
	}

	// Case: boolean comparison
	if b, ok := actual.(bool); ok {
		if expBool, err := strconv.ParseBool(expectedStr); err == nil {
			return b == expBool
		}
	}

	return fmt.Sprintf("%v", actual) == expectedStr
}

func forwardLocally(serverURL, token, clientID, defaultTarget string, payload WebhookPayload, matchedRule *ForwardRule) {
	prefix := fmt.Sprintf("[%s]", clientID)
	destinationURL := defaultTarget
	method := payload.Method
	var ruleID *int64

	if matchedRule != nil {
		if matchedRule.URL != "" {
			destinationURL = matchedRule.URL
		}
		if matchedRule.Type != "" {
			method = matchedRule.Type
		}
		id := matchedRule.ID
		ruleID = &id
	}

	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequest(method, destinationURL, bytes.NewBuffer(payload.Body))
	if err != nil {
		log.Printf("%s [!] Local request construction failed: %v", prefix, err)
		go reportDelivery(serverURL, token, payload.RequestID, ruleID, "FAILED", 0, destinationURL, method, "", fmt.Sprintf("Request build failed: %v", err))
		return
	}

	// Apply inbound headers
	for k, vals := range payload.Headers {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	// If matched rule has custom headers, apply them
	if matchedRule != nil && len(matchedRule.Headers) > 0 {
		for k, v := range matchedRule.Headers {
			req.Header.Set(k, v)
		}
	}

	req.Header.Set("X-ForwardTo-Relay", fmt.Sprintf("forwardto-agent: %s", clientID))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("%s [!] Dispatch to %s failed: %v", prefix, destinationURL, err)
		go reportDelivery(serverURL, token, payload.RequestID, ruleID, "FAILED", 0, destinationURL, method, "", fmt.Sprintf("Dispatch failed: %v", err))
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

	matchedNotice := ""
	if matchedRule != nil {
		matchedNotice = fmt.Sprintf(" [Matched Rule #%d]", matchedRule.ID)
	} else {
		matchedNotice = " [Default Target]"
	}

	log.Printf("%s [✓ DELIVERED] HTTP %d <- %s (req #%d)%s", prefix, resp.StatusCode, destinationURL, payload.RequestID, matchedNotice)

	// Asynchronously notify ForwardTo of delivery receipt so user sees it in the dashboard UI
	go reportDelivery(serverURL, token, payload.RequestID, ruleID, status, resp.StatusCode, destinationURL, method, respStr, "")
}

func reportDelivery(serverURL, token string, requestID int64, ruleID *int64, status string, httpStatus int, targetURL, method, respBody, errMsg string) {
	reportEndpoint := fmt.Sprintf("%s/api/agent/delivery-report", serverURL)

	payload := map[string]interface{}{
		"token":         token,
		"request_id":    requestID,
		"rule_id":       ruleID,
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

// -------------------------------------------------------------------------
// Self-update and version checking logic
// -------------------------------------------------------------------------

func fetchLatestRelease() (*GitHubRelease, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOwner, githubRepo)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "forwardto-agent/"+Version)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func parseSemVer(v string) (major, minor, patch int) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) > 0 {
		major, _ = strconv.Atoi(parts[0])
	}
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		patchParts := strings.Split(parts[2], "-")
		patch, _ = strconv.Atoi(patchParts[0])
	}
	return
}

func isNewerVersion(latest, current string) bool {
	lMaj, lMin, lPatch := parseSemVer(latest)
	cMaj, cMin, cPatch := parseSemVer(current)

	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPatch > cPatch
}

// checkLatestVersionNotice prints a notification banner if a newer version is available
func checkLatestVersionNotice() {
	rel, err := fetchLatestRelease()
	if err != nil {
		return
	}

	if isNewerVersion(rel.TagName, Version) {
		log.Println("------------------------------------------------------------------")
		log.Printf("[!] A newer version of forwardto-agent is available: %s (current: v%s)", rel.TagName, Version)
		log.Println("    To auto-update immediately, run:")
		log.Println("      forwardto-agent --update")
		log.Println("    Or re-run the installer:")
		log.Println("      curl -sSL https://forwardto.cloud/install.sh | bash")
		log.Println("------------------------------------------------------------------")
	}
}

// executeSelfUpdate performs an in-place download and binary replacement
func executeSelfUpdate(verbose bool) error {
	if verbose {
		fmt.Printf("[*] Checking for updates (current version: v%s)...\n", Version)
	}

	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to check latest release: %w", err)
	}

	if !isNewerVersion(rel.TagName, Version) {
		if verbose {
			fmt.Printf("[✓] forwardto-agent is already up to date (%s).\n", Version)
		}
		return nil
	}

	fmt.Printf("[*] Found newer version: %s. Locating asset for %s/%s...\n", rel.TagName, runtime.GOOS, runtime.GOARCH)

	expectedAssetName := fmt.Sprintf("forwardto-agent-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		expectedAssetName += ".exe"
	}

	var downloadURL string
	for _, asset := range rel.Assets {
		if strings.EqualFold(asset.Name, expectedAssetName) {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("release %s does not contain a binary matching %s", rel.TagName, expectedAssetName)
	}

	fmt.Printf("[*] Downloading %s from %s...\n", expectedAssetName, downloadURL)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download: HTTP %d", resp.StatusCode)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlink: %w", err)
	}

	if err := applyUpdate(exePath, resp.Body); err != nil {
		return fmt.Errorf("failed to apply binary update: %w", err)
	}

	fmt.Printf("[✓] Successfully updated forwardto-agent to %s!\n", rel.TagName)
	fmt.Printf("    Updated executable location: %s\n", exePath)
	return nil
}

// applyUpdate replaces the binary at exePath safely and atomically
func applyUpdate(exePath string, r io.Reader) error {
	dir := filepath.Dir(exePath)
	tmpFile, err := os.CreateTemp(dir, "forwardto-agent-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", dir, err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmpFile, r); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write update binary: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpName, 0755); err != nil {
		return fmt.Errorf("failed to set executable permissions: %w", err)
	}

	oldPath := exePath + ".old"
	_ = os.Remove(oldPath)

	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("failed to move existing binary (check file permissions or run with sudo): %w", err)
	}

	if err := os.Rename(tmpName, exePath); err != nil {
		// Rollback if replacement failed
		_ = os.Rename(oldPath, exePath)
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	_ = os.Remove(oldPath)
	return nil
}
