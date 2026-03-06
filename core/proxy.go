package core

import (
	"math/rand"
	"sync"

	"github.com/sirupsen/logrus"
)

// ProxyPool manages a list of proxy URLs and rotates through them.
// This distributes requests across multiple IPs, dramatically reducing
// the chance of any single IP getting blocked or CAPTCHA'd.
type ProxyPool struct {
	mu      sync.Mutex
	proxies []ProxyEntry
	index   int
}

// ProxyEntry represents a single proxy with usage tracking
type ProxyEntry struct {
	URL        string
	FailCount  int
	IsDisabled bool
}

// NewProxyPool creates a proxy pool from a list of proxy URLs.
// Accepts formats like:
//   - http://user:pass@host:port
//   - socks5://host:port
//   - http://host:port
func NewProxyPool(proxyURLs []string) *ProxyPool {
	entries := make([]ProxyEntry, 0, len(proxyURLs))
	for _, url := range proxyURLs {
		if url != "" {
			entries = append(entries, ProxyEntry{URL: url})
		}
	}

	// Shuffle to avoid all instances starting with the same proxy
	rand.Shuffle(len(entries), func(i, j int) {
		entries[i], entries[j] = entries[j], entries[i]
	})

	return &ProxyPool{
		proxies: entries,
		index:   0,
	}
}

// Next returns the next available proxy URL using round-robin rotation.
// Returns empty string if no proxies are available.
func (p *ProxyPool) Next() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.proxies) == 0 {
		return ""
	}

	// Try to find an enabled proxy, cycling through all of them
	for i := 0; i < len(p.proxies); i++ {
		idx := (p.index + i) % len(p.proxies)
		if !p.proxies[idx].IsDisabled {
			p.index = (idx + 1) % len(p.proxies)
			logrus.Debugf("Proxy rotation: using proxy #%d", idx)
			return p.proxies[idx].URL
		}
	}

	// All proxies disabled — re-enable them all and try again
	logrus.Warn("All proxies disabled, re-enabling all")
	for i := range p.proxies {
		p.proxies[i].IsDisabled = false
		p.proxies[i].FailCount = 0
	}

	proxy := p.proxies[p.index].URL
	p.index = (p.index + 1) % len(p.proxies)
	return proxy
}

// ReportFailure marks a proxy as having failed.
// After 3 consecutive failures, the proxy is temporarily disabled.
func (p *ProxyPool) ReportFailure(proxyURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.proxies {
		if p.proxies[i].URL == proxyURL {
			p.proxies[i].FailCount++
			if p.proxies[i].FailCount >= 3 {
				p.proxies[i].IsDisabled = true
				logrus.Warnf("Proxy disabled after %d failures: %s", p.proxies[i].FailCount, maskProxy(proxyURL))
			}
			return
		}
	}
}

// ReportSuccess resets the failure count for a proxy
func (p *ProxyPool) ReportSuccess(proxyURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.proxies {
		if p.proxies[i].URL == proxyURL {
			p.proxies[i].FailCount = 0
			return
		}
	}
}

// Size returns the total number of proxies in the pool
func (p *ProxyPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.proxies)
}

// ActiveCount returns the number of currently enabled proxies
func (p *ProxyPool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	count := 0
	for _, proxy := range p.proxies {
		if !proxy.IsDisabled {
			count++
		}
	}
	return count
}

// Stats returns proxy pool statistics
func (p *ProxyPool) Stats() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	active := 0
	disabled := 0
	for _, proxy := range p.proxies {
		if proxy.IsDisabled {
			disabled++
		} else {
			active++
		}
	}

	return map[string]interface{}{
		"total":    len(p.proxies),
		"active":   active,
		"disabled": disabled,
	}
}

// maskProxy hides credentials in proxy URLs for safe logging
func maskProxy(proxyURL string) string {
	if len(proxyURL) > 20 {
		return proxyURL[:10] + "****" + proxyURL[len(proxyURL)-6:]
	}
	return "****"
}
