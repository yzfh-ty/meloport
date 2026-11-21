// Package cloud115 reads files and temporary download addresses through 115 OpenAPI.
package cloud115

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL             = "https://proapi.115.com"
	pageSize            = 200
	maxPages            = 500
	maxDirectoryEntries = pageSize * maxPages
	maxResponseBytes    = 4 << 20
)

var (
	directoryID = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})$`)
	pickCode    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	bearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
	sha1Digest  = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

	// ErrUnauthorized indicates the token must be replaced through authorization.
	ErrUnauthorized = errors.New("115 authorization expired or invalid; supply a fresh access token")
)

type Client struct {
	baseURL     string
	accessToken string
	http        *http.Client
}

type User struct {
	ID   string
	Name string
}

type File struct {
	ID         string
	ParentID   string
	Name       string
	PickCode   string
	SHA1       string
	Size       int64
	IsDir      bool
	Incomplete bool // Explicitly unavailable according to the listing.
}

// New keeps the token in memory and sends it only to the fixed OpenAPI origin.
func New(accessToken string, timeout time.Duration) (*Client, error) {
	if len(accessToken) > 8192 || !bearerToken.MatchString(accessToken) {
		return nil, errors.New("115 requires a valid access token from the configured environment variable")
	}
	if timeout <= 0 {
		return nil, errors.New("115 request timeout must be positive")
	}
	return &Client{
		baseURL:     baseURL,
		accessToken: accessToken,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) UserInfo(ctx context.Context) (User, error) {
	response, err := c.request(ctx, "user info", http.MethodGet, "/open/user/info", nil, nil, "Meloport")
	if err != nil {
		return User{}, err
	}
	var data struct {
		ID   json.Number `json:"user_id"`
		Name string      `json:"user_name"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return User{}, errors.New("115 user info returned invalid data")
	}
	id := data.ID.String()
	if !directoryID.MatchString(id) || id == "0" {
		return User{}, errors.New("115 user info returned an invalid user ID")
	}
	return User{ID: id, Name: data.Name}, nil
}

// List returns the complete immediate contents of cid, including directories.
// Never return a partial listing, since a duplicate or missing entry can make
// the caller bind a staged file to the wrong remote object.
func (c *Client) List(ctx context.Context, cid string) ([]File, error) {
	if !directoryID.MatchString(cid) {
		return nil, errors.New("invalid 115 directory ID")
	}
	query := url.Values{
		"cid": {cid}, "offset": {"0"}, "limit": {strconv.Itoa(pageSize)},
		"show_dir": {"1"}, "cur": {"1"}, "o": {"file_name"}, "asc": {"1"},
	}
	files := make([]File, 0)
	seen := make(map[string]struct{})
	var total int64
	for page := 0; page < maxPages; page++ {
		offset := len(files)
		query.Set("offset", strconv.Itoa(offset))
		response, err := c.request(ctx, "list", http.MethodGet, "/open/ufile/files", query, nil, "Meloport")
		if err != nil {
			return nil, err
		}
		var entries []fileEntry
		if err := json.Unmarshal(response.Data, &entries); err != nil {
			return nil, errors.New("115 directory listing returned invalid data")
		}
		count, err := response.Count.Int64()
		if err != nil || count < 0 || count > maxDirectoryEntries {
			return nil, errors.New("115 directory count is invalid or exceeds the listing limit")
		}
		if page == 0 {
			total = count
		}
		if count != total || (response.CID != "" && response.CID.String() != cid) ||
			(response.Offset != "" && response.Offset.String() != strconv.Itoa(offset)) ||
			len(entries) > pageSize || int64(offset+len(entries)) > total {
			return nil, errors.New("115 directory listing changed or returned inconsistent pagination")
		}
		for _, entry := range entries {
			file, err := entry.file(cid)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[file.ID]; duplicate {
				return nil, errors.New("115 directory listing returned duplicate file IDs")
			}
			seen[file.ID] = struct{}{}
			files = append(files, file)
		}
		if int64(len(files)) == total {
			return files, nil
		}
		if len(entries) == 0 {
			return nil, errors.New("115 directory listing ended before the reported count")
		}
	}
	return nil, errors.New("115 directory listing exceeds the page limit")
}

// DownURL obtains a fresh, User-Agent-bound CDN address for one pick code.
func (c *Client) DownURL(ctx context.Context, code, userAgent string) (string, error) {
	if !pickCode.MatchString(code) {
		return "", errors.New("invalid 115 pick code")
	}
	form := url.Values{"pick_code": {code}}
	response, err := c.request(ctx, "download address", http.MethodPost, "/open/ufile/downurl", nil, form, userAgent)
	if err != nil {
		return "", err
	}
	var files map[string]struct {
		PickCode string `json:"pick_code"`
		URL      struct {
			URL string `json:"url"`
		} `json:"url"`
	}
	if err := json.Unmarshal(response.Data, &files); err != nil {
		return "", errors.New("115 returned invalid download address data")
	}
	if len(files) == 1 {
		for id, entry := range files {
			if directoryID.MatchString(id) && id != "0" && entry.PickCode == code && c.validDownloadURL(entry.URL.URL) {
				return entry.URL.URL, nil
			}
		}
	}
	return "", errors.New("115 returned an invalid or mismatched download address")
}

func (c *Client) validDownloadURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" ||
		u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\\ \x7f") {
		return false
	}
	// The response may contain temporary CDN credentials. It must never echo
	// the OpenAPI bearer token into the player's Location header.
	decoded, err := url.QueryUnescape(raw)
	return err == nil && !strings.Contains(raw, c.accessToken) && !strings.Contains(decoded, c.accessToken)
}

type apiResponse struct {
	State  *bool           `json:"state"`
	Code   json.Number     `json:"code"`
	Data   json.RawMessage `json:"data"`
	CID    json.Number     `json:"cid"`
	Count  json.Number     `json:"count"`
	Offset json.Number     `json:"offset"`
}

type fileEntry struct {
	ID       json.Number `json:"fid"`
	ParentID json.Number `json:"pid"`
	Kind     json.Number `json:"fc"`
	Name     string      `json:"fn"`
	PickCode string      `json:"pc"`
	SHA1     string      `json:"sha1"`
	Size     json.Number `json:"fs"`
	Area     json.Number `json:"aid"`
	Transfer json.Number `json:"fta"`
}

func (entry fileEntry) file(cid string) (File, error) {
	id := entry.ID.String()
	if !directoryID.MatchString(id) || id == "0" || entry.ParentID.String() != cid ||
		entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\\x00\r\n") ||
		(entry.Kind != "0" && entry.Kind != "1") {
		return File{}, errors.New("115 directory listing returned an invalid file entry")
	}
	file := File{ID: id, ParentID: cid, Name: entry.Name, IsDir: entry.Kind == "0"}
	if file.IsDir {
		return file, nil
	}
	if entry.Size != "" {
		size, err := entry.Size.Int64()
		if err != nil || size < 0 {
			return File{}, errors.New("115 directory listing returned an invalid file size")
		}
		file.Size = size
	}
	if pickCode.MatchString(entry.PickCode) {
		file.PickCode = entry.PickCode
	}
	if sha1Digest.MatchString(entry.SHA1) {
		file.SHA1 = strings.ToLower(entry.SHA1)
	}
	file.Incomplete = (entry.Transfer != "" && entry.Transfer != "1") || (entry.Area != "" && entry.Area != "1")
	return file, nil
}

func (c *Client) request(ctx context.Context, operation, method, path string, query, form url.Values, userAgent string) (apiResponse, error) {
	var result apiResponse
	address := c.baseURL + path
	if len(query) != 0 {
		address += "?" + query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return result, fmt.Errorf("cannot construct 115 %s request", operation)
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("115 %s connection, TLS or timeout failure", operation)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return result, ErrUnauthorized
	case http.StatusOK:
	default:
		return result, fmt.Errorf("115 %s HTTP %d", operation, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("cannot read 115 %s response", operation)
	}
	if len(data) > maxResponseBytes {
		return result, fmt.Errorf("115 %s response exceeds 4 MiB", operation)
	}
	if err := json.Unmarshal(data, &result); err != nil || result.State == nil {
		return apiResponse{}, fmt.Errorf("115 %s response is invalid", operation)
	}
	if *result.State {
		return result, nil
	}
	code, _ := result.Code.Int64()
	if code == 99 || strings.HasPrefix(strconv.FormatInt(code, 10), "401") {
		return apiResponse{}, ErrUnauthorized
	}
	return apiResponse{}, fmt.Errorf("115 %s rejected request (code %d)", operation, code)
}
