# ForwardTo WebSocket Agent (`forwardto-agent`)

The official high-performance CLI runner for **ForwardTo**. Forward live webhooks directly to local targets (e.g. `http://localhost:8000/webhook`) through secure outbound WebSockets with zero public ports, zero firewall port-forwarding, and zero Ngrok dependencies.

---

## ⚡ Quick Installation

### Linux & macOS
```bash
curl -sSL https://raw.githubusercontent.com/syedn-tech/forwardTo-Websocket-Agent/main/install.sh | bash
```

### Windows (PowerShell)
```powershell
irm https://raw.githubusercontent.com/syedn-tech/forwardTo-Websocket-Agent/main/install.ps1 | iex
```

---

## 🚀 Usage

### 1. Configure via Environment File (`.env`)
Create a `.env` file in your working directory (or use `.env.example` as a template):

**Single Client:**
```dotenv
FORWARDTO_TEAM_KEY="fwd_live_xxxxxxxxxxxxxxxx"
FORWARDTO_CLIENT_ID="cli_xxxxxxxx"
FORWARDTO_TOKEN="fwd_tok_xxxxxxxxxxxxxxxx"
```

**Multiple Clients (One Agent):**
```dotenv
FORWARDTO_TEAM_KEY="fwd_live_xxxxxxxxxxxxxxxx"
FORWARDTO_CLIENTS="billing:fwd_tok_1111, auth:fwd_tok_2222, webhooks:fwd_tok_3333"
```

> **Note:** Target forwarding URLs and rules are configured directly in your ForwardTo dashboard under Endpoint Settings and sync dynamically to the agent without requiring restarts.

Then simply start the agent:
```bash
forwardto-agent
```

### 2. Configure via Command-line Flags

**Single Client:**
```bash
forwardto-agent \
  --team-key "fwd_live_xxxxxxxx" \
  --client-id "cli_xxxxxxxx" \
  --token "fwd_tok_xxxxxxxx"
```

**Multiple Clients:**
```bash
forwardto-agent \
  --team-key "fwd_live_xxxxxxxx" \
  --clients "billing:tok_1111,auth:tok_2222"
```
Or via comma-separated IDs and tokens:
```bash
forwardto-agent \
  --team-key "fwd_live_xxxxxxxx" \
  --client-id "billing,auth" \
  --token "tok_1111,tok_2222"
```

### 3. Check Version & Self-Update
```bash
# Check installed version
forwardto-agent --version

# Auto-update binary in-place to the latest release
forwardto-agent --update
```

---

## ✨ Features
- **Outbound Only**: Connects outwards to ForwardTo via secure WebSocket (`wss://`). No public IP or open ingress ports needed.
- **Rule-Based Routing**: If an incoming webhook matches forwarding rules defined in your ForwardTo dashboard, it automatically dispatches to the matched rule's URL. If no rules match, it forwards to your client's default target URL.
- **Dynamic Config**: Updates to the default target URL on the ForwardTo dashboard are applied instantly over WebSocket without agent restarts.
- **In-Place Self-Updating**: Automatically detects new releases on startup and can self-update in place via `forwardto-agent --update`.
- **Real-Time Delivery Logging**: Dispatches requests to your local target and asynchronously reports status code and latency back to your ForwardTo workspace.
- **Zero Dependencies**: Single standalone compiled Go binary.
- **Graceful Shutdown**: Instant clean exit on `Ctrl+C`.

---

## 🛠️ Building From Source

```bash
git clone https://github.com/syedn-tech/forwardTo-Websocket-Agent.git
cd forwardTo-Websocket-Agent
go build -o forwardto-agent main.go
./forwardto-agent
```
