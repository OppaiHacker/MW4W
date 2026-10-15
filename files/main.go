// mw4w-files: MW4W's file manager (Nautilus look, Everything-fast search).
//
// One resident process holds the file index (SQLite, FTS5), a local HTTP server for the UI and
// every window (WebView2, all on one UI thread). Later starts hand their folder to it and exit, and
// it keeps a hidden, loaded window ready, so a window opens in a few milliseconds.
//
//	mw4w-files.exe [folder]       open a window (at folder, else home)
//	mw4w-files.exe --background   start resident without a window (sign-in)
//	mw4w-files.exe --serve        no windows: print the UI's URL (any OS, development)
//	mw4w-files.exe --quit         stop the resident process
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var version = "dev"

type serverInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	Pid   int    `json:"pid"`
}

func main() {
	var path string
	serve, quit, background := false, false, false
	for _, a := range os.Args[1:] {
		switch a {
		case "--serve":
			serve = true
		case "--quit":
			quit = true
		case "--background":
			background = true
		default:
			if !strings.HasPrefix(a, "--") {
				path = a
			}
		}
	}
	if path != "" {
		path = resolvePath(path)
	}
	data := dataDir()
	os.MkdirAll(data, 0o755)
	infoFile := filepath.Join(data, "server.json")

	if !serve {
		if handOff(infoFile, path, quit, background) || quit {
			return
		}
		if !singleInstance() {
			// another one is starting right now: give it a moment, then hand off
			for i := 0; i < 50; i++ {
				time.Sleep(100 * time.Millisecond)
				if handOff(infoFile, path, false, background) {
					return
				}
			}
			return
		}
	}

	a, err := newApp(data)
	if err != nil {
		fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	a.token = hex.EncodeToString(tok)
	a.port = ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, a.routes())
	go a.ix.start()

	if serve {
		fmt.Printf("http://127.0.0.1:%d/?t=%s\n", a.port, a.token)
		select {}
	}
	b, _ := json.Marshal(serverInfo{a.port, a.token, os.Getpid()})
	os.WriteFile(infoFile, b, 0o600)
	defer os.Remove(infoFile)
	runUI(a, path, !background)
}

// handOff gives the folder to a running mw4w-files. False when none answers.
func handOff(infoFile, path string, quit, background bool) bool {
	b, err := os.ReadFile(infoFile)
	if err != nil {
		return false
	}
	var si serverInfo
	if json.Unmarshal(b, &si) != nil || si.Port == 0 {
		return false
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/window", si.Port)
	body, _ := json.Marshal(map[string]string{"path": path})
	if quit {
		url = fmt.Sprintf("http://127.0.0.1:%d/api/quit", si.Port)
	} else if background {
		url = fmt.Sprintf("http://127.0.0.1:%d/api/ping", si.Port)
	}
	allowForeground() // the resident process may bring its window to the front
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("X-Token", si.Token)
	c := http.Client{Timeout: 2 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func resolvePath(p string) string {
	p = strings.Trim(p, `"`)
	if strings.HasPrefix(p, "file:") {
		p = strings.TrimPrefix(strings.TrimPrefix(p, "file:"), "///")
	}
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	return p // a file opens its folder with it selected (see list)
}

func dataDir() string {
	if d := os.Getenv("MW4W_FILES_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "mw4w-files")
	}
	d, _ := os.UserCacheDir()
	return filepath.Join(d, "mw4w-files")
}

func fatal(err error) {
	os.WriteFile(filepath.Join(dataDir(), "error.log"), []byte(time.Now().Format(time.RFC3339)+" "+err.Error()+"\n"), 0o644)
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
