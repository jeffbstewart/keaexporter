// Command ouifetch captures the IEEE MAC-prefix registries (MA-L,
// MA-M, MA-S) into third_party/ieee-oui/. The capture is near-verbatim:
// the only edits are CRLF -> LF (repo-wide line-ending policy) and a
// guaranteed trailing newline. Every file is validated as strict UTF-8
// with no stray control bytes BEFORE anything is written: the vendored
// CSVs are the repo's only exemption from check-ascii, and the deal is
// "validated UTF-8", not "anything goes" (cmd/ouigen -verify re-checks
// the committed files on every presubmit, so the gate holds without
// network access).
//
// Run from the repo root on a dev box -- the CI jail has no egress --
// and only occasionally: IEEE dislikes high-frequency bulk downloads.
//
//	go run ./cmd/ouifetch
//	go run ./cmd/ouigen
//
// A PROVENANCE file records source URLs, fetch date, row counts, and
// sha256 of each capture.
package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var sources = []struct{ name, url string }{
	{"oui.csv", "https://standards-oui.ieee.org/oui/oui.csv"},
	{"mam.csv", "https://standards-oui.ieee.org/oui28/mam.csv"},
	{"oui36.csv", "https://standards-oui.ieee.org/oui36/oui36.csv"},
}

func main() {
	fs := flag.NewFlagSet("ouifetch", flag.ContinueOnError)
	dir := fs.String("dir", "third_party/ieee-oui", "capture directory")
	timeout := fs.Duration("timeout", 2*time.Minute, "per-file download deadline")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2) // flag already printed the message and usage
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fatal("mkdir %s: %v", *dir, err)
	}
	client := &http.Client{Timeout: *timeout}
	prov := &strings.Builder{}
	fmt.Fprintf(prov, "IEEE MAC-prefix registry capture -- written by cmd/ouifetch.\n")
	fmt.Fprintf(prov, "Fetched %s. Regenerate internal/oui with cmd/ouigen.\n\n", time.Now().UTC().Format("2006-01-02"))
	for _, s := range sources {
		body, err := fetch(client, s.url)
		if err != nil {
			fatal("%s: %v", s.url, err)
		}
		body, err = sanitize(body)
		if err != nil {
			fatal("%s: %v", s.url, err)
		}
		path := filepath.Join(*dir, s.name)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			fatal("write %s: %v", path, err)
		}
		rows := strings.Count(string(body), "\n") - 1 // minus header
		fmt.Fprintf(prov, "%s\n  source: %s\n  rows: %d\n  sha256: %x\n",
			s.name, s.url, rows, sha256.Sum256(body))
		fmt.Printf("ouifetch: %s: %d rows\n", s.name, rows)
	}
	provPath := filepath.Join(*dir, "PROVENANCE")
	if err := os.WriteFile(provPath, []byte(prov.String()), 0o644); err != nil {
		fatal("write %s: %v", provPath, err)
	}
}

func fetch(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Identify honestly; this is a rare manual capture, not a crawler.
	req.Header.Set("User-Agent", "homenet-ouifetch (occasional manual capture)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// sanitize normalizes CRLF to LF, guarantees a trailing newline, and
// enforces the capture contract: strict UTF-8, no control bytes except
// tab and newline, and the expected CSV header.
func sanitize(b []byte) ([]byte, error) {
	b = []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if len(b) == 0 || b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("capture is not valid UTF-8")
	}
	for _, r := range string(b) {
		if r < 0x20 && r != '\n' && r != '\t' {
			return nil, fmt.Errorf("capture contains control byte %#x", r)
		}
	}
	if !strings.HasPrefix(string(b), "Registry,Assignment,") {
		return nil, fmt.Errorf("capture does not start with the expected CSV header")
	}
	return b, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ouifetch: "+format+"\n", args...)
	os.Exit(1)
}
