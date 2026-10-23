package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

type LocalClient struct{ client *http.Client }

func NewLocalClient(socket string) *LocalClient {
	return &LocalClient{client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}
func (c *LocalClient) Snapshot(ctx context.Context) (any, error) {
	return c.Action(ctx, "diagnostics", nil)
}
func (c *LocalClient) Action(ctx context.Context, kind string, payload json.RawMessage) (any, error) {
	data, err := json.Marshal(struct {
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}{kind, payload})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://updater.local/action", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, errors.New("local operation failed")
	}
	var result any
	err = json.NewDecoder(io.LimitReader(r.Body, 128*1024)).Decode(&result)
	return result, err
}
