// MW4W-Setup.exe: the whole repo (install.ps1, uninstall.ps1, pack/, wallpapers/)
// in one file. It unpacks itself to %TEMP% and runs install.ps1, or uninstall.ps1
// with --uninstall. Other arguments go to the script (-NoStart, -KeepWallpaper).
// Build: build.sh (copies the payload next to this file, then go build).
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed all:payload
var payload embed.FS

var version = "dev"

func main() {
	fmt.Printf("MW4W %s - ML4W-style Hyprland on Windows (GlazeWM + Zebar)\n\n", version)
	code := run(os.Args[1:])
	if !quiet(os.Args[1:]) {
		fmt.Print("\nPress Enter to close...")
		fmt.Scanln()
	}
	os.Exit(code)
}

func quiet(args []string) bool {
	for _, a := range args {
		if strings.EqualFold(a, "--quiet") || strings.EqualFold(a, "/quiet") {
			return true
		}
	}
	return false
}

func run(args []string) int {
	script := "install.ps1"
	var pass []string
	for _, a := range args {
		switch strings.ToLower(a) {
		case "--uninstall", "/uninstall", "-uninstall":
			script = "uninstall.ps1"
		case "--quiet", "/quiet":
		case "--help", "/?", "-h":
			fmt.Println("Usage: MW4W-Setup.exe [-NoStart] [-KeepWallpaper] [--uninstall] [--quiet]")
			return 0
		default:
			pass = append(pass, a)
		}
	}

	dir, err := os.MkdirTemp("", "mw4w-setup-")
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := unpack(dir); err != nil {
		fmt.Println("error unpacking:", err)
		return 1
	}

	cmd := exec.Command("powershell", append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(dir, script)}, pass...)...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("\nMW4W setup failed:", err)
		return 1
	}
	return 0
}

func unpack(dir string) error {
	root, err := fs.Sub(payload, "payload")
	if err != nil {
		return err
	}
	return fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
