// Command workorder is a sample ticketing app built on OWL's API: it files work orders, shows what OWL found
// when it checked each one against the live switch, and shows the evidence OWL kept while the work was done.
package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed index.html
var page []byte

type pairing struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	CertSHA256 string `json:"cert_sha256"`
}

func main() {
	pairingPath := flag.String("pairing", "data/workorder-pairing.json", "pairing JSON from `owl pair -name workorder -json FILE`")
	addr := flag.String("addr", "127.0.0.1:8090", "address to serve the app on")
	flag.Parse()
	data, err := os.ReadFile(*pairingPath)
	if err != nil {
		log.Fatal(err)
	}
	var p pairing
	if err := json.Unmarshal(data, &p); err != nil {
		log.Fatalf("%s: %v", *pairingPath, err)
	}
	base := "https://" + strings.TrimPrefix(p.URL, "wss://")
	owl := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: pinned(p.CertSHA256)}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	// The browser only ever talks to this app; the token stays here and is added on the way to OWL.
	mux.HandleFunc("/api/workorders/", func(w http.ResponseWriter, r *http.Request) { forward(owl, base, p.Token, w, r) })
	mux.HandleFunc("/api/workorders", func(w http.ResponseWriter, r *http.Request) { forward(owl, base, p.Token, w, r) })
	mux.HandleFunc("GET /api/evidence/", func(w http.ResponseWriter, r *http.Request) { forward(owl, base, p.Token, w, r) })
	log.Printf("work order app on http://%s, using OWL at %s", *addr, base)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// OWL serves a self-signed certificate, so it is trusted by the fingerprint from pairing rather than by a CA.
func pinned(fingerprint string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("OWL sent no certificate")
			}
			sum := sha256.Sum256(raw[0])
			if hex.EncodeToString(sum[:]) != fingerprint {
				return errors.New("OWL's certificate does not match the pairing")
			}
			return nil
		},
	}
}

func forward(owl *http.Client, base, token string, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := owl.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("OWL unreachable: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
