package netbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxResponseBytes = 8 << 20

type Ref struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Interface struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Device Ref    `json:"device"`
	Cable  *Ref   `json:"cable"`
}

type tagged struct {
	tags  []string
	found bool
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	tags    map[string]tagged
}

func (c *Client) Do(method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + "/api/" + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("netbox: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("netbox %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("netbox %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(data))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("netbox %s %s: decode: %w", method, path, err)
	}
	return nil
}

func (c *Client) List(path string, query url.Values, out any) error {
	var page struct {
		Count   int             `json:"count"`
		Next    *string         `json:"next"`
		Results json.RawMessage `json:"results"`
	}
	q := url.Values{"limit": {"1000"}}
	for k, v := range query {
		q[k] = v
	}
	if err := c.Do(http.MethodGet, path, q, nil, &page); err != nil {
		return err
	}
	if page.Next != nil {
		return fmt.Errorf("netbox %s: more than 1000 results", path)
	}
	return json.Unmarshal(page.Results, out)
}

func (c *Client) Ensure(path string, query url.Values, body map[string]any) (int, error) {
	var found []struct {
		ID int `json:"id"`
	}
	if err := c.List(path, query, &found); err != nil {
		return 0, err
	}
	switch len(found) {
	case 0:
		var created struct {
			ID int `json:"id"`
		}
		if err := c.Do(http.MethodPost, path, nil, body, &created); err != nil {
			return 0, err
		}
		return created.ID, nil
	case 1:
		return found[0].ID, c.Do(http.MethodPatch, fmt.Sprintf("%s%d/", path, found[0].ID), nil, body, nil)
	}
	return 0, fmt.Errorf("netbox %s %v: %d matches, want at most 1", path, query, len(found))
}

func (c *Client) InterfaceByMAC(mac string) (*Interface, error) {
	var ifaces []Interface
	if err := c.List("dcim/interfaces/", url.Values{"mac_address": {mac}}, &ifaces); err != nil {
		return nil, err
	}
	switch len(ifaces) {
	case 0:
		return nil, nil
	case 1:
		return &ifaces[0], nil
	}
	return nil, fmt.Errorf("netbox: %d interfaces carry MAC %s", len(ifaces), mac)
}

func (c *Client) Tags(mac string) ([]string, bool, error) {
	if t, ok := c.tags[mac]; ok {
		return t.tags, t.found, nil
	}
	iface, err := c.InterfaceByMAC(mac)
	if err != nil {
		return nil, false, err
	}
	var t tagged
	if iface != nil {
		var device struct {
			Tags []struct {
				Slug string `json:"slug"`
			} `json:"tags"`
		}
		if err := c.Do(http.MethodGet, fmt.Sprintf("dcim/devices/%d/", iface.Device.ID), nil, nil, &device); err != nil {
			return nil, false, err
		}
		t.found = true
		for _, tag := range device.Tags {
			t.tags = append(t.tags, tag.Slug)
		}
	}
	if c.tags == nil {
		c.tags = map[string]tagged{}
	}
	c.tags[mac] = t
	return t.tags, t.found, nil
}
