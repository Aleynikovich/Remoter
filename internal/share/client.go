package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Client sends to other devices. Base URLs look like http://100.95.169.40:8080.
type Client struct {
	HTTP *http.Client
	// From names this device to the receiver.
	From string
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Info asks a device whether it receives shares, and what it is called.
func (c *Client) Info(ctx context.Context, base string) (*Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/share/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s does not receive shares (%s)", base, resp.Status)
	}
	var info Info
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// SendFile sends the file at path as file index of count (both from 1).
// progress, if set, is called with the bytes sent so far.
func (c *Client) SendFile(ctx context.Context, base, path string, index, count int, progress func(sent, total int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a folder; only files can be sent", path)
	}
	var body io.Reader = f
	if progress != nil {
		body = &counter{r: f, total: st.Size(), fn: progress}
	}
	return c.Send(ctx, base, filepath.Base(path), body, st.Size(), index, count)
}

// Send sends size bytes of body (-1 if unknown) as a file called name.
func (c *Client) Send(ctx context.Context, base, name string, body io.Reader, size int64, index, count int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/share/file?name="+url.QueryEscape(name), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(headerIndex, strconv.Itoa(index))
	req.Header.Set(headerCount, strconv.Itoa(count))
	return c.do(req)
}

// SendText sends text or a link.
func (c *Client) SendText(ctx context.Context, base, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to send: the text is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/share/text", strings.NewReader(text))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	return c.do(req)
}

func (c *Client) do(req *http.Request) error {
	req.Header.Set(headerFrom, c.From)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

type counter struct {
	r     io.Reader
	sent  int64
	total int64
	fn    func(sent, total int64)
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.sent += int64(n)
	c.fn(c.sent, c.total)
	return n, err
}
