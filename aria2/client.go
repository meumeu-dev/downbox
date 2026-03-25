package aria2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type Client struct {
	url    string
	secret string
	http   *http.Client
}

func NewClient(url, secret string) *Client {
	return &Client{
		url:    url,
		secret: secret,
		http:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Download represents an aria2 download entry.
type Download struct {
	GID             string `json:"gid"`
	Status          string `json:"status"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	UploadSpeed     string `json:"uploadSpeed"`
	Dir             string `json:"dir"`
	Files           []File `json:"files"`
	BitTorrent      *BT    `json:"bittorrent,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

// Name returns a human-readable name for this download.
func (d Download) Name() string {
	if d.BitTorrent != nil && d.BitTorrent.Info.Name != "" {
		return d.BitTorrent.Info.Name
	}
	if len(d.Files) > 0 && len(d.Files[0].URIs) > 0 {
		return d.Files[0].URIs[0].URI
	}
	if len(d.Files) > 0 && d.Files[0].Path != "" {
		return d.Files[0].Path
	}
	return d.GID
}

// Progress returns download progress as 0-100.
func (d Download) Progress() float64 {
	total := ParseSize(d.TotalLength)
	if total == 0 {
		return 0
	}
	return float64(ParseSize(d.CompletedLength)) / float64(total) * 100
}

type File struct {
	Path   string `json:"path"`
	Length string `json:"length"`
	URIs   []URI  `json:"uris"`
}

type URI struct {
	URI    string `json:"uri"`
	Status string `json:"status"`
}

type BT struct {
	Info BTInfo `json:"info"`
}

type BTInfo struct {
	Name string `json:"name"`
}

type GlobalStat struct {
	DownloadSpeed   string `json:"downloadSpeed"`
	UploadSpeed     string `json:"uploadSpeed"`
	NumActive       string `json:"numActive"`
	NumWaiting      string `json:"numWaiting"`
	NumStopped      string `json:"numStopped"`
	NumStoppedTotal string `json:"numStoppedTotal"`
}

// ParseSize converts aria2's string numbers to int64.
func ParseSize(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// --- JSON-RPC primitives ---

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) call(method string, params ...interface{}) (json.RawMessage, error) {
	// Prepend secret token
	allParams := make([]interface{}, 0, len(params)+1)
	if c.secret != "" {
		allParams = append(allParams, "token:"+c.secret)
	}
	allParams = append(allParams, params...)

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      "downbox",
		Method:  method,
		Params:  allParams,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	resp, err := c.http.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("aria2 rpc: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("aria2 error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// multicall executes multiple RPC calls in a single HTTP request.
func (c *Client) multicall(calls []rpcRequest) ([]json.RawMessage, error) {
	body, err := json.Marshal(calls)
	if err != nil {
		return nil, fmt.Errorf("marshal multicall: %w", err)
	}

	resp, err := c.http.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("aria2 rpc: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var responses []rpcResponse
	if err := json.Unmarshal(data, &responses); err != nil {
		return nil, fmt.Errorf("unmarshal multicall: %w", err)
	}

	results := make([]json.RawMessage, len(responses))
	for i, r := range responses {
		if r.Error != nil {
			return nil, fmt.Errorf("aria2 error in call %d: %s", i, r.Error.Message)
		}
		results[i] = r.Result
	}
	return results, nil
}

func (c *Client) makeReq(method string, params ...interface{}) rpcRequest {
	allParams := make([]interface{}, 0, len(params)+1)
	if c.secret != "" {
		allParams = append(allParams, "token:"+c.secret)
	}
	allParams = append(allParams, params...)
	return rpcRequest{
		JSONRPC: "2.0",
		ID:      method,
		Method:  method,
		Params:  allParams,
	}
}

// --- Public API ---

// ListAll returns all downloads (active + waiting + stopped).
func (c *Client) ListAll() ([]Download, error) {
	calls := []rpcRequest{
		c.makeReq("aria2.tellActive"),
		c.makeReq("aria2.tellWaiting", 0, 100),
		c.makeReq("aria2.tellStopped", 0, 100),
	}

	results, err := c.multicall(calls)
	if err != nil {
		return nil, err
	}

	var all []Download
	for _, raw := range results {
		var downloads []Download
		if err := json.Unmarshal(raw, &downloads); err != nil {
			return nil, fmt.Errorf("unmarshal downloads: %w", err)
		}
		all = append(all, downloads...)
	}
	return all, nil
}

// AddURI adds a download by URL or magnet link.
func (c *Client) AddURI(urls []string, opts map[string]string) (string, error) {
	var params []interface{}
	params = append(params, urls)
	if opts != nil {
		params = append(params, opts)
	}
	result, err := c.call("aria2.addUri", params...)
	if err != nil {
		return "", err
	}
	var gid string
	json.Unmarshal(result, &gid)
	return gid, nil
}

// AddTorrent adds a download from a base64-encoded torrent file.
func (c *Client) AddTorrent(torrentB64 string) (string, error) {
	result, err := c.call("aria2.addTorrent", torrentB64)
	if err != nil {
		return "", err
	}
	var gid string
	json.Unmarshal(result, &gid)
	return gid, nil
}

// Remove stops and removes a download.
func (c *Client) Remove(gid string) error {
	_, err := c.call("aria2.remove", gid)
	if err != nil {
		// Try forceRemove if normal remove fails (e.g., already stopped)
		_, err = c.call("aria2.forceRemove", gid)
		if err != nil {
			// If force also fails, try removing from results
			_, err = c.call("aria2.removeDownloadResult", gid)
		}
	}
	return err
}

// Pause pauses a download.
func (c *Client) Pause(gid string) error {
	_, err := c.call("aria2.pause", gid)
	return err
}

// Resume unpauses a download.
func (c *Client) Resume(gid string) error {
	_, err := c.call("aria2.unpause", gid)
	return err
}

// GetGlobalStat returns global download statistics.
func (c *Client) GetGlobalStat() (*GlobalStat, error) {
	result, err := c.call("aria2.getGlobalStat")
	if err != nil {
		return nil, err
	}
	var stat GlobalStat
	if err := json.Unmarshal(result, &stat); err != nil {
		return nil, err
	}
	return &stat, nil
}

// Ping checks if aria2 is reachable.
func (c *Client) Ping() bool {
	_, err := c.call("aria2.getVersion")
	return err == nil
}
