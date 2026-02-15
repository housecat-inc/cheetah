package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/cockroachdb/errors"
)

type Client struct {
	URL string
}

func NewClient(url string) *Client {
	return &Client{URL: url}
}

func (c *Client) AppDelete(space string) {
	req, _ := http.NewRequest(http.MethodDelete, c.URL+"/api/apps/"+space, nil)
	http.DefaultClient.Do(req)
}

func (c *Client) AppPost(req AppIn) (*AppOut, error) {
	bs, err := json.Marshal(req)
	if err != nil {
		return nil, errors.Wrap(err, "marshal")
	}

	res, err := http.Post(c.URL+"/api/apps", "application/json", bytes.NewReader(bs))
	if err != nil {
		return nil, errors.Wrap(err, "post")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return nil, errors.Newf("register failed: %s", res.Status)
	}

	var out AppOut
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, errors.Wrap(err, "decode")
	}
	return &out, nil
}

func (c *Client) LogPost(space string, entries []Log) {
	body, _ := json.Marshal(entries)
	http.Post(c.URL+"/api/apps/"+space+"/logs", "application/json", bytes.NewReader(body))
}

func (c *Client) HealthUpdate(space string, portActive int, status string) {
	body, _ := json.Marshal(map[string]any{
		"port_active": portActive,
		"status":      status,
	})
	req, _ := http.NewRequest(http.MethodPut, c.URL+"/api/apps/"+space+"/health", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	http.DefaultClient.Do(req)
}
