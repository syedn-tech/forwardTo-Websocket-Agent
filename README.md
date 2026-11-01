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

```dotenv
FORWARDTO_SERVER="https://forwardto.cloud"
FORWARDTO_TEAM_KEY="fwd_live_xxxxxxxxxxxxxxxx"
FORWARDTO_CLIENT_ID="cli_xxxxxxxx"
FORWARDTO_TOKEN="fwd_tok_xxxxxxxxxxxxxxxx"
TARGET_URL="http://localhost:8000/api/webhook"
```

Then simply start the agent:
```bash
forwardto-agent
```

### 2. Configure via Command-line Flags
```bash
forwardto-agent \
  -server "https://forwardto.cloud" \
  -key "fwd_live_xxxxxxxx" \
  -client "cli_xxxxxxxx" \
  -token "fwd_tok_xxxxxxxx" \
  -target "http://localhost:8000/api/webhook"
```

---

## ✨ Features
- **Outbound Only**: Connects outwards to ForwardTo via secure WebSocket (`wss://`). No public IP or open ingress ports needed.
- **Dynamic Config**: Updates to the target forwarding URL on the ForwardTo dashboard are applied instantly over WebSocket without agent restarts.
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
