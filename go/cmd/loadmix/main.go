// Mixed load generator for local profiling. Not part of the app binary.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	baseURL  = "http://127.0.0.1:8080"
	hostHdr  = "u.isucon.local"
	username = "test001"
	password = "test"
)

func main() {
	duration := 25 * time.Second
	if s := os.Getenv("LOAD_DURATION"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			duration = d
		}
	}
	workers := 40
	if s := os.Getenv("LOAD_WORKERS"); s != "" {
		if n, err := fmt.Sscanf(s, "%d", &workers); err != nil || n != 1 {
			workers = 40
		}
	}

	livestreamID := os.Getenv("LOAD_LIVESTREAM_ID")
	if livestreamID == "" {
		livestreamID = "999"
	}

	client := &http.Client{Timeout: 30 * time.Second}
	jar := &cookieJar{cookies: map[string][]*http.Cookie{}}
	client.Jar = jar

	if err := login(client); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}

	// Warmup
	for _, path := range []string{
		"/api/livestream/search?limit=5",
		fmt.Sprintf("/api/livestream/%s/livecomment?limit=10", livestreamID),
		fmt.Sprintf("/api/livestream/%s/reaction?limit=10", livestreamID),
	} {
		_, _ = get(client, path)
	}

	var ok, fail uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			n := 0
			for {
				if time.Now().After(deadline) {
					return
				}
				select {
				case <-stop:
					return
				default:
				}
				var err error
				switch n % 4 {
				case 0:
					_, err = get(client, fmt.Sprintf("/api/livestream/%s/livecomment?limit=20", livestreamID))
				case 1:
					_, err = get(client, fmt.Sprintf("/api/livestream/%s/reaction?limit=20", livestreamID))
				case 2:
					_, err = get(client, "/api/livestream/search?limit=10")
				case 3:
					err = postLivecomment(client, livestreamID, fmt.Sprintf("load-%d-%d", id, n))
				}
				n++
				if err != nil {
					atomic.AddUint64(&fail, 1)
				} else {
					atomic.AddUint64(&ok, 1)
				}
			}
		}(i)
	}

	time.Sleep(duration)
	close(stop)
	wg.Wait()
	fmt.Printf("loadmix done workers=%d duration=%s ok=%d fail=%d\n", workers, duration, ok, fail)
}

func login(client *http.Client) error {
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

func get(client *http.Client, path string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Host = hostHdr
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func postLivecomment(client *http.Client, livestreamID, comment string) error {
	body, _ := json.Marshal(map[string]interface{}{"comment": comment, "tip": 0})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/livestream/"+livestreamID+"/livecomment", bytes.NewReader(body))
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
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
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
	host := u.Host
	if host == "127.0.0.1:8080" {
		host = hostHdr
	}
	return j.cookies[host]
}
