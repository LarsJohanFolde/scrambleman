package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

var AppDataDir string
var FontDir string
var IpFile string
var IP string
var ScrambleURL string
var CompetitorListURL string
var ClientCrt string
var ClientKey string
var CaCrt string

func init() {
	appName := "ScrambleDesk"

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}

	switch runtime.GOOS {
	case "linux":
		AppDataDir = filepath.Join(homeDir, ".local", "share", appName)
	case "darwin":
		AppDataDir = filepath.Join(homeDir, "Library", "Application Support", appName)
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		AppDataDir = filepath.Join(appData, appName)
	default:
		AppDataDir = filepath.Join(homeDir, appName)
	}

	IpFile = fmt.Sprintf("%s/ip.txt", AppDataDir)
	ipBytes, err := os.ReadFile(IpFile)
	if err != nil {
		log.Fatalf("Could not read IP from file: %v", err)
	}
	IP = string(ipBytes)
	CompetitorListURL = fmt.Sprintf("https://%s:2013/group", IP)
	ScrambleURL = fmt.Sprintf("https://%s:2013/upload", IP)
	FontDir = filepath.Join(AppDataDir, "fonts")

	err = os.MkdirAll(AppDataDir, 0755)
	if err != nil {
		log.Fatal(err)
	}

	directories := []string{"archive", "avatars", "fonts", "certificates"}
	for _, d := range directories {
		dir := fmt.Sprintf("%s/%s", AppDataDir, d)
		err = os.MkdirAll(dir, 0755)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Certificates
	ClientCrt = filepath.Join(AppDataDir, "certificates", "client.crt")
	ClientKey = filepath.Join(AppDataDir, "certificates", "client.key")
	CaCrt = filepath.Join(AppDataDir, "certificates", "ca.crt")
}
