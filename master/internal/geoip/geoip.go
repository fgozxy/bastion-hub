// Package geoip resolves a public IP to a country code/name using the free
// ip-api.com endpoint, with an in-memory cache.
package geoip

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
)

type Lookup struct {
	CountryCode string
	Country     string
}

type Resolver struct {
	cache map[string]cacheEntry
	mu    sync.Mutex
	client *http.Client
}

type cacheEntry struct {
	lookup Lookup
	ts     time.Time
}

func New() *Resolver {
	return &Resolver{
		cache:  map[string]cacheEntry{},
		client: &http.Client{Timeout: 6 * time.Second},
	}
}

// LookupIP resolves an IP. Private/empty IPs return empty Lookup.
func (r *Resolver) LookupIP(ctx context.Context, ip string) Lookup {
	ip = net.ParseIP(ip).String()
	if ip == "" || ip == "<nil>" {
		return Lookup{}
	}
	if isPrivate(ip) {
		return Lookup{}
	}
	r.mu.Lock()
	if e, ok := r.cache[ip]; ok && time.Since(e.ts) < 24*time.Hour {
		r.mu.Unlock()
		return e.lookup
	}
	r.mu.Unlock()

	code, country := r.query(ctx, ip)
	r.mu.Lock()
	r.cache[ip] = cacheEntry{Lookup{code, country}, time.Now()}
	r.mu.Unlock()
	return Lookup{code, country}
}

func (r *Resolver) query(ctx context.Context, ip string) (string, string) {
	url := "http://ip-api.com/json/" + ip + "?fields=status,message,countryCode,country"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", ""
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	var body struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
		Country     string `json:"country"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", ""
	}
	if body.Status != "success" {
		return "", ""
	}
	return body.CountryCode, body.Country
}

func isPrivate(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	return false
}
