// Package dpc is a client for the Radar-DPC REST API of the Italian Civil
// Protection Department (https://dpc-radar.readthedocs.io/it/latest/api.html).
//
// Data: Radar-DPC – Dipartimento della Protezione Civile, CC-BY-SA 4.0.
package dpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Attribution must accompany any use of the data.
const Attribution = "Radar-DPC – Dipartimento della Protezione Civile (CC-BY-SA 4.0)"

const (
	DefaultBaseURL = "https://radar-api.protezionecivile.it"
	DefaultOrigin  = "https://radar.protezionecivile.it"
)

// ErrNotFound is returned (wrapped in *APIError) when the API answers 404.
var ErrNotFound = errors.New("prodotto non trovato")

// APIError is a non-2xx answer from the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API Radar-DPC: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("API Radar-DPC: HTTP %d", e.Status)
}

func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// Client talks to the Radar-DPC API. The zero value is not usable: use New.
type Client struct {
	BaseURL   string
	Origin    string // sent as Origin and, with a trailing slash, as Referer
	UserAgent string
	HTTP      *http.Client
	// RetryWait is the pause before the single retry of a request that failed
	// with a network error or a 5xx/429 answer.
	RetryWait time.Duration
}

// New returns a client with production defaults and a 20 s timeout per request.
func New(userAgent string) *Client {
	return &Client{
		BaseURL:   DefaultBaseURL,
		Origin:    DefaultOrigin,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		RetryWait: 2 * time.Second,
	}
}

// Product identifies one available product instant.
type Product struct {
	Type   string
	Time   time.Time // UTC
	Period string    // ISO 8601 duration, e.g. "PT5M"
}

// Download is a pre-signed S3 location of a product file.
type Download struct {
	Bucket  string
	Key     string
	URL     string
	Expires time.Duration
}

// FindLast returns the most recent available instant of a product type.
func (c *Client) FindLast(ctx context.Context, productType string) (Product, error) {
	var out struct {
		Total        int `json:"total"`
		LastProducts []struct {
			ProductType string `json:"productType"`
			Time        int64  `json:"time"`
			Period      string `json:"period"`
		} `json:"lastProducts"`
	}
	u := c.BaseURL + "/findLastProductByType?type=" + productType
	if err := c.do(ctx, http.MethodGet, u, nil, &out); err != nil {
		return Product{}, err
	}
	for _, p := range out.LastProducts {
		if strings.EqualFold(p.ProductType, productType) {
			return Product{Type: p.ProductType, Time: time.UnixMilli(p.Time).UTC(), Period: p.Period}, nil
		}
	}
	return Product{}, &APIError{Status: http.StatusNotFound, Message: "nessun prodotto " + productType + " nella risposta"}
}

// DownloadURL asks for a pre-signed URL of the product at time t. The API
// rounds t down to the product period.
func (c *Client) DownloadURL(ctx context.Context, productType string, t time.Time) (Download, error) {
	body, _ := json.Marshal(map[string]any{"productType": productType, "productDate": t.UnixMilli()})
	var out struct {
		Bucket         string `json:"bucket"`
		Key            string `json:"key"`
		URL            string `json:"url"`
		ExpiresSeconds int    `json:"expiresSeconds"`
	}
	if err := c.do(ctx, http.MethodPost, c.BaseURL+"/downloadProduct", body, &out); err != nil {
		return Download{}, err
	}
	if out.URL == "" {
		return Download{}, errors.New("API Radar-DPC: risposta senza url")
	}
	return Download{Bucket: out.Bucket, Key: out.Key, URL: out.URL, Expires: time.Duration(out.ExpiresSeconds) * time.Second}, nil
}

// Fetch downloads a pre-signed URL into w and returns the bytes written.
// No API headers are sent: S3 does not need them.
func (c *Client) Fetch(ctx context.Context, url string, w io.Writer) (int64, error) {
	var n int64
	err := c.retry(ctx, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return retriable(resp.StatusCode), &APIError{Status: resp.StatusCode, Message: "download: " + strings.TrimSpace(string(msg))}
		}
		// A partially written w cannot be rewound, so a body error is final.
		n, err = io.Copy(w, resp.Body)
		return false, err
	})
	return n, err
}

func (c *Client) do(ctx context.Context, method, url string, body []byte, out any) error {
	return c.retry(ctx, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return false, err
		}
		req.Header.Set("Origin", c.Origin)
		req.Header.Set("Referer", strings.TrimSuffix(c.Origin, "/")+"/")
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return true, err
		}
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error string `json:"error"`
			}
			msg := strings.TrimSpace(string(data))
			if json.Unmarshal(data, &e) == nil && e.Error != "" {
				msg = e.Error
			}
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			return retriable(resp.StatusCode), &APIError{Status: resp.StatusCode, Message: msg}
		}
		if err := json.Unmarshal(data, out); err != nil {
			return false, fmt.Errorf("API Radar-DPC: risposta non valida: %w", err)
		}
		return false, nil
	})
}

func retriable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retry runs f, and once more after RetryWait if f reports a retriable error.
func (c *Client) retry(ctx context.Context, f func() (retry bool, err error)) error {
	again, err := f()
	if err == nil || !again {
		return err
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(c.RetryWait):
	}
	_, err = f()
	return err
}
