// Package registry looks package names up on PyPI and npm.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/krish-vd/phantom/internal/extract"
)

// Info is what a registry says about a name right now.
type Info struct {
	Exists   bool      `json:"exists"`
	Created  time.Time `json:"created,omitempty"`  // first upload / creation time
	Latest   string    `json:"latest,omitempty"`   // latest version
	Releases int       `json:"releases,omitempty"` // number of versions published
	Summary  string    `json:"summary,omitempty"`
	Note     string    `json:"note,omitempty"` // e.g. "unpublished", "no files"
}

// Client checks names against PyPI and npm with a small in-memory cache.
type Client struct {
	HTTP    *http.Client
	PyPIURL string // default https://pypi.org
	NPMURL  string // default https://registry.npmjs.org

	mu    sync.Mutex
	cache map[string]Info
}

func New() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		PyPIURL: "https://pypi.org",
		NPMURL:  "https://registry.npmjs.org",
		cache:   map[string]Info{},
	}
}

// Lookup returns registry info for one name.
func (c *Client) Lookup(ctx context.Context, eco extract.Ecosystem, name string) (Info, error) {
	key := string(eco) + "/" + name
	c.mu.Lock()
	if in, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return in, nil
	}
	c.mu.Unlock()

	var in Info
	var err error
	switch eco {
	case extract.PyPI:
		in, err = c.pypi(ctx, name)
	case extract.NPM:
		in, err = c.npm(ctx, name)
	default:
		return Info{}, fmt.Errorf("unknown ecosystem %q", eco)
	}
	if err == nil {
		c.mu.Lock()
		c.cache[key] = in
		c.mu.Unlock()
	}
	return in, err
}

func (c *Client) get(ctx context.Context, u string, v any) (found bool, err error) {
	for attempt := 0; attempt < 4; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "phantom (+https://github.com/krish-vd/phantom)")
		req.Header.Set("Accept", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if attempt == 3 {
				return false, err
			}
			sleep(ctx, attempt)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return false, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			resp.Body.Close()
			sleep(ctx, attempt)
			continue
		case resp.StatusCode != http.StatusOK:
			resp.Body.Close()
			return false, fmt.Errorf("GET %s: %s", u, resp.Status)
		}
		err = json.NewDecoder(resp.Body).Decode(v)
		resp.Body.Close()
		return true, err
	}
	return false, fmt.Errorf("GET %s: gave up after retries", u)
}

func sleep(ctx context.Context, attempt int) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
	}
}

func (c *Client) pypi(ctx context.Context, name string) (Info, error) {
	var doc struct {
		Info struct {
			Version string `json:"version"`
			Summary string `json:"summary"`
		} `json:"info"`
		Releases map[string][]struct {
			Upload time.Time `json:"upload_time_iso_8601"`
		} `json:"releases"`
	}
	found, err := c.get(ctx, c.PyPIURL+"/pypi/"+url.PathEscape(name)+"/json", &doc)
	if err != nil || !found {
		return Info{}, err
	}
	in := Info{Exists: true, Latest: doc.Info.Version, Summary: trim(doc.Info.Summary), Releases: len(doc.Releases)}
	for _, files := range doc.Releases {
		for _, f := range files {
			if !f.Upload.IsZero() && (in.Created.IsZero() || f.Upload.Before(in.Created)) {
				in.Created = f.Upload
			}
		}
	}
	if in.Created.IsZero() {
		in.Note = "no files"
	}
	return in, nil
}

func (c *Client) npm(ctx context.Context, name string) (Info, error) {
	var doc struct {
		DistTags map[string]string          `json:"dist-tags"`
		Versions map[string]json.RawMessage `json:"versions"`
		Time     map[string]any             `json:"time"`
		Desc     string                     `json:"description"`
	}
	escaped := strings.Replace(url.PathEscape(name), "%40", "@", 1)
	found, err := c.get(ctx, c.NPMURL+"/"+escaped, &doc)
	if err != nil || !found {
		return Info{}, err
	}
	if _, gone := doc.Time["unpublished"]; gone {
		return Info{Exists: false, Note: "unpublished"}, nil
	}
	in := Info{Exists: true, Latest: doc.DistTags["latest"], Summary: trim(doc.Desc), Releases: len(doc.Versions)}
	if s, ok := doc.Time["created"].(string); ok {
		in.Created, _ = time.Parse(time.RFC3339, s)
	}
	return in, nil
}

func trim(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 140 {
		s = s[:137] + "..."
	}
	return s
}
