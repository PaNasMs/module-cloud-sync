package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "dropbox" {
		fmt.Fprintln(os.Stderr, "Usage: go run panasms-cloud-authorize.go dropbox [output.json]")
		os.Exit(1)
	}
	output := "panasms-dropbox-account.json"
	if len(os.Args) > 2 {
		output = os.Args[2]
	}
	fmt.Println("A browser will open. Choose the Dropbox account to connect to PaNasMs.")
	cmd := exec.Command("rclone", "authorize", "dropbox")
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Authorization failed. Install rclone and try again.")
		os.Exit(1)
	}
	var token map[string]any
	for i, c := range raw {
		if c != '{' {
			continue
		}
		var candidate map[string]any
		if json.NewDecoder(bytes.NewReader(raw[i:])).Decode(&candidate) == nil && candidate["access_token"] != nil && candidate["refresh_token"] != nil {
			token = candidate
			break
		}
	}
	if token == nil {
		fmt.Fprintln(os.Stderr, "No offline authorization received.")
		os.Exit(1)
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot create private output file. Choose a new filename.")
		os.Exit(1)
	}
	err = json.NewEncoder(f).Encode(map[string]any{"format": 1, "provider": "dropbox", "token": token})
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		fmt.Fprintln(os.Stderr, "Cannot save authorization.")
		os.Exit(1)
	}
	fmt.Println("Import", output, "in Cloud Sync. Keep it private and never commit it to Git.")
}
