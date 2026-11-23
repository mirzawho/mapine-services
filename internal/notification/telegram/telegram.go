// Package telegram sends app-open notifications through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DirectURL = "https://api.telegram.org"
)

type Sender interface {
	Send(context.Context, string) error
}

type Config struct {
	URL, Token, ChatID string
	Enabled            bool
	Timeout            time.Duration
	MaxRetries         int
}

type Client struct {
	config Config
	http   *http.Client
}

// New validates configuration and creates one reusable sender. A nil sender
// with no error means notifications are disabled or credentials are absent.
func New(c Config) (Sender, error) {
	if c.URL == "" {
		c.URL = DirectURL
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if c.Timeout < 0 || c.MaxRetries < 0 || c.MaxRetries > 10 {
		return nil, errors.New("invalid Telegram timeout or retry count")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("TELEGRAM_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/sendMessage") {
		return nil, errors.New("TELEGRAM_URL must be a Bot API base URL, not a sendMessage endpoint")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if !c.Enabled || c.Token == "" || c.ChatID == "" {
		return nil, nil
	}
	if c.ChatID, err = ParseChatID(c.ChatID); err != nil {
		return nil, err
	}
	return &Client{config: c, http: &http.Client{Timeout: c.Timeout}}, nil
}

func (c *Client) Send(ctx context.Context, message string) error {
	var endpoint string
	var body []byte
	var err error
	endpoint = c.config.URL + "/bot" + url.PathEscape(c.config.Token) + "/sendMessage"
	body, err = json.Marshal(map[string]string{"chat_id": c.config.ChatID, "text": message})
	if err != nil {
		return errors.New("encode Telegram notification")
	}
	var last error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if reqErr != nil {
			return errors.New("create Telegram notification request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, doErr := c.http.Do(req)
		if doErr != nil {
			last = errors.New("Telegram notification request failed")
			if ctx.Err() != nil {
				return last
			}
		} else {
			last = c.checkResponse(resp)
			if last == nil {
				return nil
			}
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return last
			}
		}
		if attempt < c.config.MaxRetries {
			select {
			case <-ctx.Done():
				return errors.New("Telegram notification request canceled")
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
		}
	}
	return last
}

func (c *Client) checkResponse(resp *http.Response) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.New("read Telegram response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var api telegramResponse
		if json.Unmarshal(data, &api) == nil && api.Description != "" {
			return fmt.Errorf("Telegram returned HTTP %d: %s", resp.StatusCode, c.safeDescription(api.Description))
		}
		return fmt.Errorf("Telegram returned HTTP %d", resp.StatusCode)
	}
	var api telegramResponse
	if err := json.Unmarshal(data, &api); err != nil {
		return errors.New("Telegram returned invalid JSON")
	}
	if api.OK == nil {
		return errors.New("Telegram response is missing ok")
	}
	if !*api.OK {
		if api.Description != "" {
			return fmt.Errorf("Telegram rejected notification: %s", c.safeDescription(api.Description))
		}
		return errors.New("Telegram rejected notification")
	}
	return nil
}

func (c *Client) safeDescription(description string) string {
	if c.config.Token != "" {
		description = strings.ReplaceAll(description, c.config.Token, "[redacted]")
	}
	return description
}

type telegramResponse struct {
	OK          *bool  `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// ParseChatID validates numeric IDs and preserves usernames as supplied.
// Strings remain strings through JSON encoding to avoid integer overflow.
func ParseChatID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("TELEGRAM_CHAT_ID is required")
	}
	if strings.HasPrefix(value, "@") {
		if len(value) < 2 {
			return "", errors.New("invalid Telegram chat username")
		}
		return value, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n == 0 {
		return "", errors.New("TELEGRAM_CHAT_ID must be an integer ID or @channel username")
	}
	return strconv.FormatInt(n, 10), nil
}
