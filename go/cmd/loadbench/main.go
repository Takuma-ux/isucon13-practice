// Sustained mixed load approximating bench scenario proportions (local profiling only).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
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
	password = "test"
)

// errOpSkipped marks moderate/reserve picked for a non-streamer worker (no HTTP).
var errOpSkipped = errors.New("op skipped")

// Weights tuned from bench/scenario (viewer loop: heavy GET comments/reactions/icons,
// periodic POST comment/reaction; visit_top search; streamer moderate/reserve rare).
var opWeights = []struct {
	name   string
	weight int
}{
	{"get_livecomments", 250},
	{"get_reactions", 150},
	{"search", 140},
	{"get_icon", 200},
	{"post_livecomment", 120},
	{"post_reaction", 80},
	{"enter", 25},
	{"exit", 15},
	{"get_tag", 30},
	{"register_login", 8},
	{"post_icon", 5},
	{"moderate", 4},
	{"reserve", 6},
	{"get_ngwords", 10},
	{"get_reports", 10},
}

type worker struct {
	client       *http.Client
	id           int
	livestreamID string
	streamer     string
	username     string
	seq          uint64
}

func main() {
	duration := 3 * time.Minute
	if s := os.Getenv("LOAD_DURATION"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			duration = d
		}
	}
	workers := 40
	if s := os.Getenv("LOAD_WORKERS"); s != "" {
		fmt.Sscanf(s, "%d", &workers)
	}

	livestreamID := envOr("LOAD_LIVESTREAM_ID", "999")
	streamer := envOr("LOAD_STREAMER", "test001")
	reserveStart := int64(1700874000)
	reserveEnd := int64(1700877600)
	if v := os.Getenv("RESERVE_START_AT"); v != "" {
		fmt.Sscanf(v, "%d", &reserveStart)
	}
	if v := os.Getenv("RESERVE_END_AT"); v != "" {
		fmt.Sscanf(v, "%d", &reserveEnd)
	}

	totalWeight := 0
	for _, o := range opWeights {
		totalWeight += o.weight
	}

	// Shared login for most workers; a few will register fresh users.
	baseClient := newClient()
	if err := login(baseClient, streamer, password); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap login failed: %v\n", err)
		os.Exit(1)
	}

	var ok, fail, skipped uint64
	opOK := make([]uint64, len(opWeights))
	opFail := make([]uint64, len(opWeights))
	opSkip := make([]uint64, len(opWeights))
	var latSumUs uint64
	var latCount uint64

	stop := make(chan struct{})
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	start := time.Now()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		w := &worker{
			client:       cloneClient(baseClient),
			id:           i,
			livestreamID: livestreamID,
			streamer:     streamer,
			username:     streamer,
		}
		if i%8 == 7 {
			name := fmt.Sprintf("loadbench%d%d", i, time.Now().UnixNano()%100000)
			if err := registerAndLogin(w.client, name); err == nil {
				w.username = name
			}
		}
		go func(w *worker) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w.id) + time.Now().UnixNano()))
			for {
				if time.Now().After(deadline) {
					return
				}
				select {
				case <-stop:
					return
				default:
				}
				opIdx := pickOp(rng, totalWeight)
				t0 := time.Now()
				err := runOp(w, opIdx, reserveStart, reserveEnd, rng)
				elapsed := time.Since(t0)
				atomic.AddUint64(&latSumUs, uint64(elapsed.Microseconds()))
				atomic.AddUint64(&latCount, 1)
				if errors.Is(err, errOpSkipped) {
					atomic.AddUint64(&skipped, 1)
					atomic.AddUint64(&opSkip[opIdx], 1)
				} else if err != nil {
					atomic.AddUint64(&fail, 1)
					atomic.AddUint64(&opFail[opIdx], 1)
				} else {
					atomic.AddUint64(&ok, 1)
					atomic.AddUint64(&opOK[opIdx], 1)
				}
			}
		}(w)
	}

	// Progress + latency snapshot every 30s
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case t := <-tick.C:
				elapsed := t.Sub(start)
				lc := atomic.LoadUint64(&latCount)
				var avgMs float64
				if lc > 0 {
					avgMs = float64(atomic.LoadUint64(&latSumUs)) / float64(lc) / 1000.0
				}
				fmt.Printf("[loadbench %s] ok=%d fail=%d skipped=%d avg_ms=%.2f\n", elapsed.Round(time.Second), atomic.LoadUint64(&ok), atomic.LoadUint64(&fail), atomic.LoadUint64(&skipped), avgMs)
			}
		}
	}()

	time.Sleep(duration)
	close(stop)
	wg.Wait()

	fmt.Printf("loadbench done workers=%d duration=%s ok=%d fail=%d skipped=%d\n",
		workers, duration, atomic.LoadUint64(&ok), atomic.LoadUint64(&fail), atomic.LoadUint64(&skipped))
	for i, o := range opWeights {
		fmt.Printf("  op %-18s ok=%d fail=%d skipped=%d\n", o.name, atomic.LoadUint64(&opOK[i]), atomic.LoadUint64(&opFail[i]), atomic.LoadUint64(&opSkip[i]))
	}
	if lc := atomic.LoadUint64(&latCount); lc > 0 {
		fmt.Printf("  avg_latency_ms=%.2f\n", float64(atomic.LoadUint64(&latSumUs))/float64(lc)/1000.0)
	}
}

func pickOp(rng *rand.Rand, total int) int {
	r := rng.Intn(total)
	c := 0
	for i, o := range opWeights {
		c += o.weight
		if r < c {
			return i
		}
	}
	return 0
}

func runOp(w *worker, opIdx int, reserveStart, reserveEnd int64, rng *rand.Rand) error {
	w.seq++
	name := opWeights[opIdx].name
	switch name {
	case "get_livecomments":
		_, err := get(w.client, fmt.Sprintf("/api/livestream/%s/livecomment?limit=20", w.livestreamID))
		return err
	case "get_reactions":
		_, err := get(w.client, fmt.Sprintf("/api/livestream/%s/reaction?limit=20", w.livestreamID))
		return err
	case "search":
		if rng.Intn(3) == 0 {
			_, err := get(w.client, "/api/livestream/search?tag=椅子")
			return err
		}
		_, err := get(w.client, "/api/livestream/search?limit=50")
		return err
	case "get_icon":
		_, err := get(w.client, "/api/user/"+w.streamer+"/icon")
		return err
	case "post_livecomment":
		return postJSON(w.client, fmt.Sprintf("/api/livestream/%s/livecomment", w.livestreamID), map[string]interface{}{
			"comment": fmt.Sprintf("lb-%d-%d", w.id, w.seq),
			"tip":     0,
		}, http.StatusCreated)
	case "post_reaction":
		return postJSON(w.client, fmt.Sprintf("/api/livestream/%s/reaction", w.livestreamID), map[string]interface{}{
			"emoji_name": ":tada:",
		}, http.StatusCreated)
	case "enter":
		return postEmpty(w.client, fmt.Sprintf("/api/livestream/%s/enter", w.livestreamID), http.StatusOK)
	case "exit":
		return deleteReq(w.client, fmt.Sprintf("/api/livestream/%s/exit", w.livestreamID), http.StatusOK)
	case "get_tag":
		_, err := get(w.client, "/api/tag")
		return err
	case "register_login":
		name := fmt.Sprintf("lbu%d%d", w.id, w.seq)
		return registerAndLogin(w.client, name)
	case "post_icon":
		// tiny JPEG-ish payload; bench uses real images — enough to exercise path
		img := bytes.Repeat([]byte{0xFF}, 512)
		return postJSON(w.client, "/api/icon", map[string]interface{}{"image": img}, http.StatusCreated)
	case "moderate":
		if w.username != w.streamer {
			return errOpSkipped // only streamer can moderate
		}
		return postJSON(w.client, fmt.Sprintf("/api/livestream/%s/moderate", w.livestreamID), map[string]interface{}{
			"ng_word": fmt.Sprintf("spam%d", w.seq%1000),
		}, http.StatusCreated)
	case "reserve":
		if w.username != w.streamer {
			return errOpSkipped
		}
		title := fmt.Sprintf("lb-res-%d-%d", w.id, w.seq)
		err := postJSON(w.client, "/api/livestream/reservation", map[string]interface{}{
			"tags":          []int64{1},
			"title":         title,
			"description":   "loadbench",
			"playlist_url":  "https://media.xiii.isucon.dev/api/2/playlist.m3u8",
			"thumbnail_url": "https://media.xiii.isucon.dev/yoru.webp",
			"start_at":      reserveStart,
			"end_at":        reserveEnd,
		}, http.StatusCreated)
		if err != nil && isReserveSlotFull(err) {
			return nil
		}
		return err
	case "get_ngwords":
		_, err := get(w.client, fmt.Sprintf("/api/livestream/%s/ngwords", w.livestreamID))
		return err
	case "get_reports":
		_, err := get(w.client, fmt.Sprintf("/api/livestream/%s/report", w.livestreamID))
		return err
	default:
		return nil
	}
}

func isReserveSlotFull(err error) bool {
	return err != nil && (contains(err.Error(), "予約") || contains(err.Error(), "400"))
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || len(sub) == 0 || bytesContains(s, sub)) }

func bytesContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func newClient() *http.Client {
	jar := &cookieJar{cookies: map[string][]*http.Cookie{}}
	return &http.Client{Timeout: 45 * time.Second, Jar: jar}
}

func cloneClient(src *http.Client) *http.Client {
	j := &cookieJar{cookies: map[string][]*http.Cookie{}}
	if src.Jar != nil {
		if cj, ok := src.Jar.(*cookieJar); ok {
			cj.mu.Lock()
			for k, v := range cj.cookies {
				j.cookies[k] = v
			}
			cj.mu.Unlock()
		}
	}
	return &http.Client{Timeout: 45 * time.Second, Jar: j}
}

func login(client *http.Client, user, pass string) error {
	return postJSON(client, "/api/login", map[string]string{"username": user, "password": pass}, http.StatusOK)
}

func registerAndLogin(client *http.Client, name string) error {
	body := map[string]interface{}{
		"name":         name,
		"display_name": name,
		"description":  "loadbench",
		"password":     password,
		"theme":        map[string]bool{"dark_mode": false},
	}
	if err := postJSON(client, "/api/register", body, http.StatusCreated); err != nil {
		return err
	}
	return login(client, name, password)
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
		return resp.StatusCode, fmt.Errorf("GET %s status %d", path, resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func postJSON(client *http.Client, path string, body interface{}, want int) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(b))
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
	if resp.StatusCode != want {
		return fmt.Errorf("POST %s status %d want %d", path, resp.StatusCode, want)
	}
	return nil
}

func postEmpty(client *http.Client, path string, want int) error {
	req, err := http.NewRequest(http.MethodPost, baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Host = hostHdr
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		return fmt.Errorf("POST %s status %d", path, resp.StatusCode)
	}
	return nil
}

func deleteReq(client *http.Client, path string, want int) error {
	req, err := http.NewRequest(http.MethodDelete, baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Host = hostHdr
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		return fmt.Errorf("DELETE %s status %d", path, resp.StatusCode)
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
