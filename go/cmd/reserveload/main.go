// Concurrent POST /api/livestream/reservation load for local investigation.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	baseURL = "http://127.0.0.1:8080"
	hostHdr = "u.isucon.local"
)

type result struct {
	status int
	ms     float64
	body   string
	err    error
}

func main() {
	workers := 15
	duration := 12 * time.Second
	startAt := int64(1700874000)
	endAt := int64(1700877600)
	if v := os.Getenv("RESERVE_START_AT"); v != "" {
		fmt.Sscanf(v, "%d", &startAt)
	}
	if v := os.Getenv("RESERVE_END_AT"); v != "" {
		fmt.Sscanf(v, "%d", &endAt)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	jar := &cookieJar{cookies: map[string][]*http.Cookie{}}
	client.Jar = jar
	if err := login(client, "test001", "test"); err != nil {
		fmt.Fprintf(os.Stderr, "login: %v\n", err)
		os.Exit(1)
	}

	var results []result
	var mu sync.Mutex
	stop := time.Now().Add(duration)
	var wg sync.WaitGroup
	var seq uint64

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for time.Now().Before(stop) {
				n := atomic.AddUint64(&seq, 1)
				title := fmt.Sprintf("reserveload-%d-%d", wid, n)
				r := doReserve(client, startAt, endAt, title)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	latencies := make([]float64, 0, len(results))
	var (
		ok201, slotFull400, other4xx, err5xx, netErr, timeout int
	)
	for _, r := range results {
		if r.err != nil {
			if os.IsTimeout(r.err) {
				timeout++
			} else {
				netErr++
			}
			continue
		}
		latencies = append(latencies, r.ms)
		switch {
		case r.status == http.StatusCreated:
			ok201++
		case r.status == http.StatusBadRequest && containsSlotReject(r.body):
			slotFull400++
		case r.status >= 500:
			err5xx++
		case r.status >= 400:
			other4xx++
		}
	}

	sort.Float64s(latencies)
	fmt.Printf("reserveload workers=%d duration=%s total=%d\n", workers, duration, len(results))
	fmt.Printf("  201 created: %d\n", ok201)
	fmt.Printf("  400 slot-full (expected): %d\n", slotFull400)
	fmt.Printf("  other 4xx: %d\n", other4xx)
	fmt.Printf("  5xx: %d\n", err5xx)
	fmt.Printf("  network errors: %d\n", netErr)
	fmt.Printf("  timeouts: %d\n", timeout)
	if len(latencies) > 0 {
		fmt.Printf("  latency ms p50=%.1f p95=%.1f p99=%.1f max=%.1f\n",
			percentile(latencies, 50), percentile(latencies, 95), percentile(latencies, 99), latencies[len(latencies)-1])
	}
	if other4xx > 0 || err5xx > 0 || netErr > 0 || timeout > 0 {
		fmt.Println("  sample unexpected bodies:")
		shown := 0
		for _, r := range results {
			if r.err != nil || r.status == 201 || (r.status == 400 && containsSlotReject(r.body)) {
				continue
			}
			if shown >= 5 {
				break
			}
			fmt.Printf("    status=%d err=%v body=%q\n", r.status, r.err, trunc(r.body, 200))
			shown++
		}
	}
}

func containsSlotReject(body string) bool {
	return strings.Contains(body, "予約できません") || strings.Contains(body, "予約期間")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted) - 1) * p / 100
	return sorted[idx]
}

func doReserve(client *http.Client, startAt, endAt int64, title string) result {
	t0 := time.Now()
	body, _ := json.Marshal(map[string]interface{}{
		"tags":          []int64{1},
		"title":         title,
		"description":   "load test",
		"playlist_url":  "https://example.com/playlist",
		"thumbnail_url": "https://example.com/thumb",
		"start_at":      startAt,
		"end_at":        endAt,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/livestream/reservation", bytes.NewReader(body))
	if err != nil {
		return result{err: err, ms: time.Since(t0).Seconds() * 1000}
	}
	req.Host = hostHdr
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return result{err: err, ms: time.Since(t0).Seconds() * 1000}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{
		status: resp.StatusCode,
		ms:     time.Since(t0).Seconds() * 1000,
		body:   string(b),
	}
}

func login(client *http.Client, username, password string) error {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Host = hostHdr
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login status %d", resp.StatusCode)
	}
	return nil
}

type cookieJar struct {
	mu      sync.Mutex
	cookies map[string][]*http.Cookie
}

func (j *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies[hostHdr] = cookies
}

func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	if u.Host == "127.0.0.1:8080" {
		return j.cookies[hostHdr]
	}
	return j.cookies[u.Host]
}
