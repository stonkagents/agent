// Package main: standalone keypair generator for installers and scripts.
// Usage:
//
//	genkeys                     → prints base64(pub)\nbase64(priv) to stdout
//	genkeys --out <secrets.env> → writes STONKAGENTS_PRIVATE_KEY=<base64>\n to file (for MSI/installers)
//
// Safe to run from MSI custom action (SYSTEM account); no panic → exit 1.
package main

import (
	"encoding/base64"
	"fmt"
	"os"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "genkeys panic: %v\n", r)
			os.Exit(1)
		}
	}()

	pub, priv, err := crypto.GenerateKeypair()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// --out path: write secrets.env directly (used by setuphelper so we don't rely on stdout parsing)
	if len(os.Args) >= 3 && os.Args[1] == "--out" {
		outPath := os.Args[2]
		privB64 := base64.StdEncoding.EncodeToString(priv)
		content := "STONKAGENTS_PRIVATE_KEY=" + privB64 + "\n"
		if err := os.WriteFile(outPath, []byte(content), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "genkeys: write %s: %v\n", outPath, err)
			os.Exit(1)
		}
		return
	}

	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	fmt.Println(base64.StdEncoding.EncodeToString(priv))
}
