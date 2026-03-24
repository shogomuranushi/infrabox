package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type VMResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	AuthEnabled bool   `json:"auth_enabled"`
	ExecURL     string `json:"exec_url"`
	IngressURL  string `json:"ingress_url"`
	CreatedAt   string `json:"created_at"`
	Error       string `json:"error,omitempty"`
}

type VMListResponse struct {
	VMs []VMResponse `json:"vms"`
}

func apiRequest(method, path string, body interface{}) ([]byte, int, error) {
	data, status, err := doRequest(method, path, body, cfg.APIKey)
	if status == http.StatusUnauthorized {
		fmt.Fprintln(os.Stderr, "ERROR: API key is invalid or expired. Run 'ib init' to set a new key.")
		os.Exit(1)
	}
	return data, status, err
}

func apiRequestNoAuth(method, path string, body interface{}) ([]byte, int, error) {
	return doRequest(method, path, body, "")
}

func doRequest(method, path string, body interface{}, apiKey string) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.Endpoint+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("connection failed: %v\nendpoint: %s", err, cfg.Endpoint)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func mustConfig() {
	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: API key is not set. Run 'ib init' or set INFRABOX_API_KEY.")
		os.Exit(1)
	}
	if cfg.Endpoint == "" {
		fmt.Fprintln(os.Stderr, "ERROR: endpoint is not configured")
		os.Exit(1)
	}
}

