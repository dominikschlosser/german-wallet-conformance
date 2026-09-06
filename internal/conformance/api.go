package conformance

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type API struct {
	BaseURL, Token string
	Client         *http.Client
}

func NewAPI(baseURL, token string) *API {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The harness talks to local services with development certificates.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return &API{strings.TrimRight(baseURL, "/"), token, &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}
func (a *API) Request(ctx context.Context, method, path, contentType string, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		data, err := a.request(ctx, method, path, contentType, body)
		if err == nil || method != "GET" || attempt == 2 || ctx.Err() != nil || !retryableRead(err) {
			return data, err
		}
		log.Printf("Retrying suite read after %v", err)
		if err := pause(ctx, time.Duration(attempt+1)*time.Second); err != nil {
			return nil, err
		}
	}
}

type httpError struct {
	method, path string
	status       int
}

func (e *httpError) Error() string { return fmt.Sprintf("%s %s: HTTP %d", e.method, e.path, e.status) }
func retryableRead(err error) bool {
	var network net.Error
	var response *httpError
	return (errors.As(err, &network) && network.Timeout()) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &response) && (response.status == 502 || response.status == 503 || response.status == 504))
}
func (a *API) request(ctx context.Context, method, path, contentType string, body []byte) ([]byte, error) {
	target := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		target = a.BaseURL + "/" + strings.TrimLeft(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if a.Token != "" && strings.HasPrefix(target, a.BaseURL+"/") {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpError{method, req.URL.Path, resp.StatusCode}
	}
	return data, nil
}
func (a *API) JSON(ctx context.Context, method, path string, body []byte, out any) error {
	data, err := a.Request(ctx, method, path, "application/json", body)
	if err != nil {
		return err
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

type Variant map[string]string

type Plan struct {
	ID       string   `json:"id"`
	StoredID string   `json:"_id"`
	Name     string   `json:"planName"`
	Variant  Variant  `json:"variant"`
	Modules  []Module `json:"modules"`
}
type Module struct {
	Name      string   `json:"testModule"`
	Variant   Variant  `json:"variant"`
	Instances []string `json:"instances"`
}
type ModuleInfo struct {
	ID      string  `json:"id"`
	Name    string  `json:"testName"`
	Status  string  `json:"status"`
	Result  string  `json:"result"`
	Variant Variant `json:"variant"`
	Config  Config  `json:"config"`
	BaseURL string  `json:"baseUrl"`
}
type LogEntry struct {
	Result   string `json:"result"`
	Message  string `json:"msg"`
	Source   string `json:"src"`
	BaseURL  string `json:"baseUrl"`
	Upload   string `json:"upload"`
	Redirect string `json:"redirect_to"`
	OfferURL string `json:"credential_offer_redirect_url"`
}

func (m ModuleInfo) Terminal() bool { return m.Status == "FINISHED" || m.Status == "INTERRUPTED" }
func (m ModuleInfo) Verdict() string {
	if m.Status == "FINISHED" {
		if m.Result != "" {
			return m.Result
		}
		return "UNKNOWN"
	}
	if m.Status == "INTERRUPTED" && (m.Result == "FAILED" || m.Result == "WARNING" || m.Result == "REVIEW") {
		return m.Result
	}
	if m.Status != "" {
		return m.Status
	}
	return "UNKNOWN"
}
