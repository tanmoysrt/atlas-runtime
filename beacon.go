package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// BeaconClient talks to the beacon KV service.
// It supports PUT / DELETE / LIST over HTTP and live change streaming
// over WebSocket on /events.
type BeaconClient struct {
	endpoint string
	client   *http.Client
}

// BeaconObject is one entry from the beacon store.
type BeaconObject struct {
	Key       string            `json:"key"`
	Timestamp int64             `json:"timestamp"`
	Value     string            `json:"value"`
	Labels    map[string]string `json:"labels"`
	Deleted   bool              `json:"deleted"`
}

// NewBeaconClient creates a client for the given beacon base URL.
func NewBeaconClient(endpoint string) *BeaconClient {
	return &BeaconClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Put writes an object to beacon. If the object already exists it is overwritten.
func (b *BeaconClient) Put(key, value string, labels map[string]string) error {
	body, _ := json.Marshal(map[string]any{"value": value, "labels": labels})
	return b.do("PUT", "/objects/"+key, body)
}

// Delete removes an object from beacon. A tombstone is created so that
// watchers see the deletion.
func (b *BeaconClient) Delete(key string) error {
	return b.do("DELETE", "/objects/"+key, nil)
}

// List queries objects by prefix and label filters.
func (b *BeaconClient) List(prefix string, labels map[string]string) ([]BeaconObject, error) {
	u, _ := url.Parse(b.endpoint + "/objects")
	q := u.Query()
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	for k, v := range labels {
		q.Set("label."+k, v)
	}
	u.RawQuery = q.Encode()

	resp, err := b.client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("beacon list: %d", resp.StatusCode)
	}
	var result struct {
		Objects []BeaconObject `json:"objects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Objects, nil
}

// watchOnce opens one WebSocket to /events, subscribes to matching labels,
// and calls handler for every object message until the context is cancelled
// or the connection breaks.
func (b *BeaconClient) watchOnce(ctx context.Context, labels map[string]string, handler func(BeaconObject)) error {
	u, _ := url.Parse(b.endpoint + "/events")
	u.Scheme = "ws"
	if strings.HasPrefix(b.endpoint, "https") {
		u.Scheme = "wss"
	}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	// Subscribe with since=0 to receive all current objects as catch-up.
	if err := conn.WriteJSON(map[string]any{"type": "subscribe", "labels": labels, "since": 0}); err != nil {
		return err
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		var message struct {
			Type string `json:"type"`
			BeaconObject
		}
		if json.Unmarshal(data, &message) != nil || message.Type != "object" {
			continue
		}
		handler(message.BeaconObject)
	}
}

func (b *BeaconClient) do(method, path string, body []byte) error {
	req, err := http.NewRequest(method, b.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("beacon %s %s: %d", method, path, resp.StatusCode)
	}
	return nil
}
