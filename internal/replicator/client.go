package replicator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TrackerClient struct {
	baseURL string
	client  *http.Client
}

type DaemonClient struct {
	baseURL         string
	client          *http.Client
	liveAgentAPIKey string
}

type RecentAsset struct {
	CID         string    `json:"cid"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	PeerID      string    `json:"peer_id"`
	AnnouncedAt time.Time `json:"announced_at"`
}

type recentAssetsResponse struct {
	Data []struct {
		CID         string `json:"cid"`
		Filename    string `json:"filename"`
		Size        int64  `json:"size"`
		PeerID      string `json:"peer_id"`
		AnnouncedAt string `json:"announced_at"`
	} `json:"data"`
}

type peersResponse struct {
	Data []struct {
		PeerID   string `json:"peer_id"`
		LastSeen string `json:"last_seen"`
	} `json:"data"`
}

type libraryResponse struct {
	Storage struct {
		UsedBytes int64 `json:"used_bytes"`
	} `json:"storage"`
}

type downloadStatusResponse struct {
	Downloads []struct {
		State string `json:"state"`
	} `json:"downloads"`
}

func NewTrackerClient(baseURL string) *TrackerClient {
	return &TrackerClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func NewDaemonClient(baseURL, liveAgentAPIKey string) *DaemonClient {
	return &DaemonClient{
		baseURL:         strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:          &http.Client{Timeout: 30 * time.Second},
		liveAgentAPIKey: strings.TrimSpace(liveAgentAPIKey),
	}
}

func (c *TrackerClient) RecentAssets(ctx context.Context, since time.Time, limit int) ([]RecentAsset, error) {
	u, _ := url.Parse(c.baseURL + "/api/v1/tracker/assets/recent")
	q := u.Query()
	q.Set("limit", fmt.Sprintf("%d", limit))
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("recent assets GET %s: status=%d", u.String(), resp.StatusCode)
	}
	var parsed recentAssetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]RecentAsset, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		at, err := time.Parse(time.RFC3339, item.AnnouncedAt)
		if err != nil {
			continue
		}
		out = append(out, RecentAsset{
			CID:         item.CID,
			Filename:    item.Filename,
			Size:        item.Size,
			PeerID:      item.PeerID,
			AnnouncedAt: at,
		})
	}
	return out, nil
}

func (c *TrackerClient) ReplicaCount(ctx context.Context, cid string, maxSourcePeerAge time.Duration) (int, error) {
	u := fmt.Sprintf("%s/api/v1/tracker/assets/%s/peers", c.baseURL, url.PathEscape(cid))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("asset peers status=%d", resp.StatusCode)
	}
	var parsed peersResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, err
	}
	now := time.Now()
	count := 0
	for _, p := range parsed.Data {
		t, err := time.Parse(time.RFC3339, p.LastSeen)
		if err != nil {
			continue
		}
		if now.Sub(t) <= maxSourcePeerAge {
			count++
		}
	}
	return count, nil
}

const headerLiveAgentKey = "X-Live-Agent-Key"

func (c *DaemonClient) QueueDownload(ctx context.Context, cid string) error {
	u := c.baseURL + "/api/v1/live-agent/download"
	body, _ := json.Marshal(map[string]string{"cid": cid})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.liveAgentAPIKey != "" {
		req.Header.Set(headerLiveAgentKey, c.liveAgentAPIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("queue download status=%d", resp.StatusCode)
	}
	return nil
}

func (c *DaemonClient) DownloadState(ctx context.Context, cid string) (string, error) {
	u := c.baseURL + "/api/v1/downloads/status?cid=" + url.QueryEscape(cid)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status code=%d", resp.StatusCode)
	}
	var parsed downloadStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Downloads) == 0 {
		return "unknown", nil
	}
	return parsed.Downloads[0].State, nil
}

func (c *DaemonClient) LibraryUsage(ctx context.Context) (int64, error) {
	u := c.baseURL + "/api/v1/library?limit=1&offset=0"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("library status=%d", resp.StatusCode)
	}
	var parsed libraryResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, err
	}
	return parsed.Storage.UsedBytes, nil
}
