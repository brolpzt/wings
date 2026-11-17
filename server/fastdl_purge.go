package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"emperror.dev/errors"
)

// FastDlPurgeConfig tells Wings how to clear the CDN cache of the files a
// synchronization overwrote or deleted, so players never download stale copies.
type FastDlPurgeConfig struct {
	Provider string `json:"provider"` // only "cloudflare"
	ZoneID   string `json:"zone_id"`
	APIToken string `json:"api_token"`
	// BaseURL is the public URL the files are downloaded from; a file's URL is
	// BaseURL + "/" + <server short id> + "/" + <path in the server>.
	BaseURL string `json:"base_url"`
}

func (c *FastDlPurgeConfig) enabled() bool {
	return c != nil && strings.EqualFold(c.Provider, "cloudflare") && c.ZoneID != "" && c.APIToken != "" && c.BaseURL != ""
}

// Cloudflare accepts at most 30 URLs per purge request outside Enterprise.
const fastDlPurgeBatch = 30

var (
	cloudflareAPIBase     = "https://api.cloudflare.com/client/v4"
	fastDlPurgeHTTPClient = &http.Client{Timeout: 30 * time.Second}
	fastDlPurgeRetryDelay = 5 * time.Second
)

// fastDlPurgeList collects, from concurrent upload workers, the paths whose
// cached copy became stale.
type fastDlPurgeList struct {
	mu    sync.Mutex
	paths []string
}

func (l *fastDlPurgeList) add(paths ...string) {
	l.mu.Lock()
	l.paths = append(l.paths, paths...)
	l.mu.Unlock()
}

func (l *fastDlPurgeList) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

// fastDlPurgeURLs builds the public URLs of the given paths. The CDN caches the
// http and https variants separately (game clients usually download over http),
// so both are returned.
func fastDlPurgeURLs(baseURL string, paths []string) ([]string, error) {
	raw := strings.TrimSpace(baseURL)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.Errorf("fastdl: invalid public URL %q for cache purge", baseURL)
	}
	hostPath := u.Host + strings.TrimRight(u.EscapedPath(), "/")

	seen := make(map[string]struct{}, len(paths))
	urls := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		segments := strings.Split(p, "/")
		for i, seg := range segments {
			segments[i] = url.PathEscape(seg)
		}
		escaped := strings.Join(segments, "/")
		if _, dup := seen[escaped]; dup {
			continue
		}
		seen[escaped] = struct{}{}
		urls = append(urls, "http://"+hostPath+"/"+escaped, "https://"+hostPath+"/"+escaped)
	}
	return urls, nil
}

// purgeFastDlCache clears the CDN cache of the given paths. A failed purge does
// not fail the synchronization (the files were uploaded): it is logged and
// reported in the sync status instead.
func (s *Server) purgeFastDlCache(ctx context.Context, cfg *FastDlPurgeConfig, paths []string, progress *fastDlProgress) {
	if !cfg.enabled() || len(paths) == 0 {
		return
	}

	urls, err := fastDlPurgeURLs(cfg.BaseURL, paths)
	if err == nil && len(urls) > 0 {
		progress.update(func(st *FastDlSyncStatus) { st.Phase = "purging" })
		for start := 0; start < len(urls); start += fastDlPurgeBatch {
			end := start + fastDlPurgeBatch
			if end > len(urls) {
				end = len(urls)
			}
			if err = cloudflarePurgeURLs(ctx, cfg.ZoneID, cfg.APIToken, urls[start:end]); err != nil {
				break
			}
			n := int64(end - start)
			progress.update(func(st *FastDlSyncStatus) { st.Purged += n })
		}
	}

	if err != nil {
		s.Log().WithField("error", err).WithField("files", len(paths)).Error("fastdl: failed to purge CDN cache")
		progress.update(func(st *FastDlSyncStatus) { st.PurgeError = err.Error() })
		return
	}
	s.Log().WithField("files", len(paths)).WithField("urls", len(urls)).Info("fastdl: purged CDN cache of changed files")
}

type cloudflareAPIResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// cloudflarePurgeURLs purges one batch of URLs, retrying when rate limited.
func cloudflarePurgeURLs(ctx context.Context, zoneID, token string, urls []string) error {
	body, err := json.Marshal(map[string][]string{"files": urls})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/zones/%s/purge_cache", strings.TrimRight(cloudflareAPIBase, "/"), url.PathEscape(zoneID))

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		res, err := fastDlPurgeHTTPClient.Do(req)
		if err != nil {
			return errors.WrapIf(err, "fastdl: cloudflare purge request failed")
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		_ = res.Body.Close()

		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500) && attempt < 3 {
			delay := fastDlPurgeRetryDelay * time.Duration(attempt+1)
			if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 && s <= 60 {
				delay = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		var parsed cloudflareAPIResponse
		_ = json.Unmarshal(raw, &parsed)
		if res.StatusCode/100 == 2 && parsed.Success {
			return nil
		}
		msg := fmt.Sprintf("HTTP %d", res.StatusCode)
		if len(parsed.Errors) > 0 {
			msg = fmt.Sprintf("%s: %s (code %d)", msg, parsed.Errors[0].Message, parsed.Errors[0].Code)
		}
		return errors.New("fastdl: cloudflare cache purge failed: " + msg)
	}
}
