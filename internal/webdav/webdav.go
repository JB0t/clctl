// Package webdav is the small part of WebDAV that sessions export needs against
// Nextcloud: PROPFIND to see whether a folder exists, MKCOL to create it, PUT to upload.
package webdav

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one WebDAV root, such as <nextcloud>/remote.php/dav/files/<account>.
type Client struct {
	Root           string // no trailing slash
	User, Password string
	HTTP           *http.Client

	server   string // set by Nextcloud: Root is corrected to the account's user ID on first use
	resolved bool
}

// New returns a client for root with basic auth.
func New(root, user, password string) *Client {
	return &Client{Root: strings.TrimRight(root, "/"), User: user, Password: password,
		HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Nextcloud returns a client for an account's files on a Nextcloud server. user is the
// login name; the file paths use the account's user ID, which can differ (login
// "data-bot", ID "Data Bot"), so the first request asks Nextcloud for it.
func Nextcloud(server, user, password string) *Client {
	server = strings.TrimRight(server, "/")
	c := New(server+"/remote.php/dav/files/"+url.PathEscape(user), user, password)
	c.server = server
	return c
}

// resolveRoot points Root at the account's user ID, from Nextcloud's OCS user endpoint. If
// the endpoint cannot say (an older server, a test stub), the login name stays in Root. A
// 401 is not remembered, so the request that follows reports it.
func (c *Client) resolveRoot() {
	if c.server == "" || c.resolved {
		return
	}
	request, err := http.NewRequest(http.MethodGet, c.server+"/ocs/v2.php/cloud/user?format=json", nil)
	if err != nil {
		return
	}
	request.SetBasicAuth(c.User, c.Password)
	request.Header.Set("OCS-APIRequest", "true")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	var body struct {
		OCS struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&body) == nil && body.OCS.Data.ID != "" {
		c.Root = c.server + "/remote.php/dav/files/" + url.PathEscape(body.OCS.Data.ID)
	}
	if response.StatusCode != http.StatusUnauthorized {
		c.resolved = true
	}
}

// URL is the escaped address of a slash-separated path below the root.
func (c *Client) URL(path string) string {
	var segments []string
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment != "" {
			segments = append(segments, url.PathEscape(segment))
		}
	}
	return c.Root + "/" + strings.Join(segments, "/")
}

func (c *Client) do(method, path string, body []byte, header map[string]string) (*http.Response, error) {
	c.resolveRoot()
	request, err := http.NewRequest(method, c.URL(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.User, c.Password)
	for key, value := range header {
		request.Header.Set(key, value)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func statusError(method, path string, response *http.Response) error {
	text, _ := io.ReadAll(io.LimitReader(response.Body, 300))
	return fmt.Errorf("%s %s: HTTP %d %s", method, path, response.StatusCode, strings.TrimSpace(string(text)))
}

// Exists reports whether path exists (PROPFIND, depth 0).
func (c *Client) Exists(path string) (bool, error) {
	response, err := c.do("PROPFIND", path, nil, map[string]string{"Depth": "0"})
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusMultiStatus, http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, statusError("PROPFIND", path, response)
}

// MkdirAll creates path and any missing parents (MKCOL). One PROPFIND when path exists;
// otherwise each level is checked from the top down.
func (c *Client) MkdirAll(path string) error {
	if exists, err := c.Exists(path); err != nil || exists {
		return err
	}
	current := ""
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		current += "/" + segment
		exists, err := c.Exists(current)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		response, err := c.do("MKCOL", current, nil, nil)
		if err != nil {
			return err
		}
		response.Body.Close()
		// 405: created meanwhile by another run.
		if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed {
			return statusError("MKCOL", current, response)
		}
	}
	return nil
}

// Put uploads data to path, replacing what is there.
func (c *Client) Put(path string, data []byte, contentType string) error {
	response, err := c.do("PUT", path, data, map[string]string{"Content-Type": contentType})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	}
	return statusError("PUT", path, response)
}
