package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config is what a device needs to find its server: written by `shanframe
// join`, read by everything else.
type Config struct {
	Server   string `json:"server"`    // http(s)://host[:port]
	Token    string `json:"token"`     // this device's own key, minted at link time
	DeviceID string `json:"device_id"` // stable id, generated at join
	Name     string `json:"name"`      // human name shown in the device list

}

func configDir() string {
	if d := os.Getenv("SHANFRAME_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "shanframe")
}

func configPath() string { return filepath.Join(configDir(), "config.json") }

func loadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath())
	if err != nil {
		return c, errors.New("this device hasn't joined a server yet — run: shanframe join <server-url> <token>")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Server == "" || c.Token == "" || c.DeviceID == "" {
		return c, errors.New("config incomplete — run: shanframe join <server-url> <token>")
	}
	return c, nil
}

func saveConfig(c Config) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), b, 0o600)
}

// join links this machine to an account: it asks the server for a short
// code, the user approves it from any signed-in shanframe screen, and the
// server answers with this device's own key.
func join(server, name string) (Config, error) {
	server = strings.TrimRight(server, "/")
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "https://" + server
	}
	if name == "" {
		name = defaultName()
	}
	body, _ := json.Marshal(map[string]string{"Name": name, "OS": osName()})
	resp, err := http.Post(server+"/v1/link/start", "application/json", bytes.NewReader(body))
	if err != nil {
		return Config{}, fmt.Errorf("can't reach %s: %w", server, err)
	}
	defer resp.Body.Close()
	var start struct{ Code, Poll string }
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil || start.Code == "" {
		return Config{}, fmt.Errorf("unexpected answer from %s", server)
	}
	appURL := strings.Replace(server, "://api.", "://app.", 1)
	fmt.Printf("\n  On a device where you're signed in, open  %s/link\n  and enter this code:\n\n      %s\n\n  waiting…\n", appURL, start.Code)
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		wr, err := http.Get(server + "/v1/link/wait?poll=" + start.Poll)
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		var res struct {
			Key      string `json:"key"`
			DeviceID string `json:"device_id"`
		}
		code := wr.StatusCode
		json.NewDecoder(wr.Body).Decode(&res)
		wr.Body.Close()
		switch {
		case code == 200 && res.Key != "":
			c := Config{Server: server, Token: res.Key, DeviceID: res.DeviceID, Name: name}
			return c, saveConfig(c)
		case code == http.StatusGone:
			return Config{}, fmt.Errorf("the code expired — run join again")
		case code != 200 && code != http.StatusRequestTimeout:
			return Config{}, fmt.Errorf("unexpected answer from %s (%d)", server, code)
		}
	}
	return Config{}, fmt.Errorf("nobody approved the code — run join again")
}

func (c Config) wsURL() string {
	u := c.Server + "/v1/ws"
	if strings.HasPrefix(u, "https://") {
		return "wss://" + strings.TrimPrefix(u, "https://")
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}

// defaultName is what a machine is called when `join` isn't given --name:
// the name its owner already knows it by. macOS: the computer name from
// Sharing ("Shawn's MacBook Pro"). Single-board computers: the board model
// ("Raspberry Pi 5"). Everything else: the hostname.
func defaultName() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	case "linux":
		if b, err := os.ReadFile("/sys/firmware/devicetree/base/model"); err == nil {
			n := strings.TrimRight(string(b), "\x00\n ")
			for _, cut := range []string{" Model ", " Rev "} {
				if i := strings.Index(n, cut); i > 0 {
					n = n[:i]
				}
			}
			if n != "" {
				return n
			}
		}
	}
	h, _ := os.Hostname()
	h = strings.ToLower(strings.TrimSuffix(h, ".local"))
	return strings.ReplaceAll(h, ".", "-")
}

func osName() string { return runtime.GOOS }
