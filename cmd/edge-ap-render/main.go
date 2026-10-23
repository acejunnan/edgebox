package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"edgebox/internal/ap"
	"edgebox/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-ap-render:", err)
		os.Exit(1)
	}
}
func run() error {
	ssid := flag.String("ssid", "", "reviewed management SSID")
	output := flag.String("output", "", "new trusted staging directory")
	flag.Parse()
	if *output == "" || *ssid == "" {
		return fmt.Errorf("ssid and output are required")
	}
	if _, err := os.Stat(*output); !os.IsNotExist(err) {
		return fmt.Errorf("output must not exist")
	}
	alphabet := "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	password := ""
	for i := 0; i < 12; i++ {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return err
		}
		password += string(alphabet[index.Int64()])
	}
	c := ap.Config{Schema: 1, Revision: 1, SSID: *ssid, Password: password}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	s := hex.EncodeToString(raw)
	uuid := s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	profile, err := c.Profile(uuid)
	if err != nil {
		return err
	}
	metadata, _ := json.MarshalIndent(c.Redacted(), "", "  ")
	for name, data := range map[string][]byte{"edgebox-management-ap.nmconnection": profile, "config.json": metadata, "dhcp.conf": []byte(ap.DHCPConfig()), "ap-isolation.nft": []byte(ap.IsolationRules())} {
		if err = store.WriteAtomic(filepath.Join(*output, name), data, 0600); err != nil {
			return err
		}
	}
	fmt.Println("AP assets generated; Wi-Fi secret is only in the protected connection profile.")
	return nil
}
