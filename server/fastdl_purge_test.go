package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFastDlPurgeURLs(t *testing.T) {
	urls, err := fastDlPurgeURLs("http://fastdl.example.com/", []string{
		"abcd1234/maps/de_dust2.bsp",
		"abcd1234/sound/my file.wav",
		"abcd1234/maps/de_dust2.bsp", // duplicate
		"",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"http://fastdl.example.com/abcd1234/maps/de_dust2.bsp",
		"https://fastdl.example.com/abcd1234/maps/de_dust2.bsp",
		"http://fastdl.example.com/abcd1234/sound/my%20file.wav",
		"https://fastdl.example.com/abcd1234/sound/my%20file.wav",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Fatalf("got %v\nwant %v", urls, want)
	}

	urls, err = fastDlPurgeURLs("cdn.example.com/fastdl", []string{"x/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if urls[0] != "http://cdn.example.com/fastdl/x/a.txt" || urls[1] != "https://cdn.example.com/fastdl/x/a.txt" {
		t.Fatalf("unexpected urls %v", urls)
	}

	if _, err := fastDlPurgeURLs("http://", []string{"a"}); err == nil {
		t.Fatal("expected an error for a URL without host")
	}
}

func TestPurgeFastDlCacheBatchesAndRetries(t *testing.T) {
	var calls, limited atomic.Int32
	var batches [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones/zone123/purge_cache" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		calls.Add(1)
		if limited.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var body struct {
			Files []string `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		batches = append(batches, body.Files)
		_, _ = w.Write([]byte(`{"success":true,"errors":[]}`))
	}))
	defer srv.Close()

	oldBase, oldDelay := cloudflareAPIBase, fastDlPurgeRetryDelay
	cloudflareAPIBase, fastDlPurgeRetryDelay = srv.URL, time.Millisecond
	defer func() { cloudflareAPIBase, fastDlPurgeRetryDelay = oldBase, oldDelay }()

	paths := make([]string, 20) // 40 URLs -> 2 batches
	for i := range paths {
		paths[i] = "abcd1234/maps/m" + strings.Repeat("x", i) + ".bsp"
	}

	s := &Server{}
	p := &fastDlProgress{}
	s.purgeFastDlCache(context.Background(), &FastDlPurgeConfig{Provider: "cloudflare", ZoneID: "zone123", APIToken: "tok", BaseURL: "http://fastdl.example.com"}, paths, p)

	st := p.snapshot()
	if st.PurgeError != "" {
		t.Fatalf("unexpected purge error %q", st.PurgeError)
	}
	if st.Purged != 40 || len(batches) != 2 || len(batches[0]) != fastDlPurgeBatch || len(batches[1]) != 10 {
		t.Fatalf("purged=%d batches=%d", st.Purged, len(batches))
	}
	if calls.Load() != 3 {
		t.Fatalf("expected one retry, got %d calls", calls.Load())
	}
}

func TestPurgeFastDlCacheReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	}))
	defer srv.Close()
	oldBase := cloudflareAPIBase
	cloudflareAPIBase = srv.URL
	defer func() { cloudflareAPIBase = oldBase }()

	s := &Server{}
	p := &fastDlProgress{}
	s.purgeFastDlCache(context.Background(), &FastDlPurgeConfig{Provider: "cloudflare", ZoneID: "z", APIToken: "t", BaseURL: "http://f.example.com"}, []string{"a/b.bsp"}, p)

	st := p.snapshot()
	if !strings.Contains(st.PurgeError, "Authentication error") || st.Purged != 0 {
		t.Fatalf("unexpected status %+v", st)
	}

	// Disabled config: nothing happens.
	p2 := &fastDlProgress{}
	s.purgeFastDlCache(context.Background(), nil, []string{"a/b.bsp"}, p2)
	if p2.snapshot().PurgeError != "" {
		t.Fatal("nil config must not purge")
	}
}
