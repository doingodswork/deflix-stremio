package realdebrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/doingodswork/deflix-stremio/pkg/debrid"
)

type ClientOptions struct {
	BaseURL      string
	Timeout      time.Duration
	CacheAge     time.Duration
	ExtraHeaders []string
}

func NewClientOpts(baseURL string, timeout, cacheAge time.Duration, extraHeaders []string) ClientOptions {
	return ClientOptions{
		BaseURL:      baseURL,
		Timeout:      timeout,
		CacheAge:     cacheAge,
		ExtraHeaders: extraHeaders,
	}
}

var DefaultClientOpts = ClientOptions{
	BaseURL:  "https://api.real-debrid.com",
	Timeout:  5 * time.Second,
	CacheAge: 24 * time.Hour,
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	// For API token validity
	tokenCache debrid.Cache
	// For info_hash instant availability
	availabilityCache debrid.Cache
	cacheAge          time.Duration
	extraHeaders      map[string]string
	logger            *zap.Logger
}

func NewClient(opts ClientOptions, tokenCache, availabilityCache debrid.Cache, logger *zap.Logger) (*Client, error) {
	// Precondition check
	if opts.BaseURL == "" {
		return nil, errors.New("opts.BaseURL must not be empty")
	}
	for _, extraHeader := range opts.ExtraHeaders {
		if extraHeader != "" {
			colonIndex := strings.Index(extraHeader, ":")
			if colonIndex <= 0 || colonIndex == len(extraHeader)-1 {
				return nil, errors.New("opts.ExtraHeaders elements must have a format like \"X-Foo: bar\"")
			}
		}
	}

	extraHeaderMap := make(map[string]string, len(opts.ExtraHeaders))
	for _, extraHeader := range opts.ExtraHeaders {
		if extraHeader != "" {
			extraHeaderParts := strings.SplitN(extraHeader, ":", 2)
			extraHeaderMap[extraHeaderParts[0]] = extraHeaderParts[1]
		}
	}

	return &Client{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
		},
		tokenCache:        tokenCache,
		availabilityCache: availabilityCache,
		cacheAge:          opts.CacheAge,
		extraHeaders:      extraHeaderMap,
		logger:            logger,
	}, nil
}

func (c *Client) TestToken(ctx context.Context, apiToken string) error {
	zapFieldDebridSite := zap.String("debridSite", "RealDebrid")
	zapFieldAPItoken := zap.String("apiToken", apiToken)
	c.logger.Debug("Testing token...", zapFieldDebridSite, zapFieldAPItoken)

	// Check cache first.
	// Note: Only when a token is valid a cache item was created, because a token is probably valid for another 24 hours, while when a token is invalid it's likely that the user makes a payment to RealDebrid to extend his premium status and make his token valid again *within* 24 hours.
	created, found, err := c.tokenCache.Get(apiToken)
	if err != nil {
		c.logger.Error("Couldn't decode token cache item", zap.Error(err), zapFieldDebridSite, zapFieldAPItoken)
	} else if !found {
		c.logger.Debug("API token not found in cache", zapFieldDebridSite, zapFieldAPItoken)
	} else if time.Since(created) > (24 * time.Hour) {
		expiredSince := time.Since(created.Add(24 * time.Hour))
		c.logger.Debug("Token cached as valid, but item is expired", zap.Duration("expiredSince", expiredSince), zapFieldDebridSite, zapFieldAPItoken)
	} else {
		c.logger.Debug("Token cached as valid", zapFieldDebridSite, zapFieldAPItoken)
		return nil
	}

	resBytes, err := c.get(ctx, c.baseURL+"/rest/1.0/user", apiToken)
	if err != nil {
		return fmt.Errorf("Couldn't fetch user info from real-debrid.com with the provided token: %v", err)
	}
	if !gjson.GetBytes(resBytes, "id").Exists() {
		return fmt.Errorf("Couldn't parse user info response from real-debrid.com")
	}

	c.logger.Debug("Token OK", zapFieldDebridSite, zapFieldAPItoken)

	// Create cache item
	if err = c.tokenCache.Set(apiToken); err != nil {
		c.logger.Error("Couldn't cache API token", zap.Error(err), zapFieldDebridSite, zapFieldAPItoken)
	}

	return nil
}

func (c *Client) CheckInstantAvailability(ctx context.Context, apiToken string, infoHashes ...string) []string {
	zapFieldDebridSite := zap.String("debridSite", "RealDebrid")
	zapFieldAPItoken := zap.String("apiToken", apiToken)

	// Precondition check
	if len(infoHashes) == 0 {
		return nil
	}

	url := c.baseURL + "/rest/1.0/torrents/instantAvailability"
	// Only check the ones of which we don't know that they're valid (or which our knowledge that they're valid is more than 24 hours old).
	// We don't cache unavailable ones, because that might change often!
	var result []string
	infoHashesNotFound := false
	infoHashesExpired := false
	infoHashesValid := false
	requestRequired := false
	for _, infoHash := range infoHashes {
		zapFieldInfoHash := zap.String("infoHash", infoHash)
		created, found, err := c.availabilityCache.Get(infoHash)
		if err != nil {
			c.logger.Error("Couldn't decode availability cache item", zap.Error(err), zapFieldInfoHash, zapFieldDebridSite, zapFieldAPItoken)
			requestRequired = true
			url += "/" + infoHash
		} else if !found {
			infoHashesNotFound = true
			requestRequired = true
			url += "/" + infoHash
		} else if time.Since(created) > (c.cacheAge) {
			infoHashesExpired = true
			requestRequired = true
			url += "/" + infoHash
		} else {
			infoHashesValid = true
			result = append(result, infoHash)
		}
	}
	if infoHashesNotFound {
		if !infoHashesExpired && !infoHashesValid {
			c.logger.Debug("No info_hash found in availability cache", zapFieldDebridSite, zapFieldAPItoken)
		} else {
			c.logger.Debug("Some info_hash not found in availability cache", zapFieldDebridSite, zapFieldAPItoken)
		}
	}
	if infoHashesExpired {
		if !infoHashesNotFound && !infoHashesValid {
			c.logger.Debug("Availability for all info_hash cached as valid, but they're expired", zapFieldDebridSite, zapFieldAPItoken)
		} else {
			c.logger.Debug("Availability for some info_hash cached as valid, but items are expired", zapFieldDebridSite, zapFieldAPItoken)
		}
	}
	if infoHashesValid {
		if !infoHashesNotFound && !infoHashesExpired {
			c.logger.Debug("Availability for all info_hash cached as valid", zapFieldDebridSite, zapFieldAPItoken)
		} else {
			c.logger.Debug("Availability for some info_hash cached as valid", zapFieldDebridSite, zapFieldAPItoken)
		}
	}

	// Only make HTTP request if we didn't find all hashes in the cache yet
	if requestRequired {
		resBytes, err := c.get(ctx, url, apiToken)
		if err != nil {
			c.logger.Error("Couldn't check torrents' instant availability on real-debrid.com", zap.Error(err), zapFieldDebridSite, zapFieldAPItoken)
		} else {
			// Note: This iterates through all elements with the key being the info_hash
			gjson.ParseBytes(resBytes).ForEach(func(key gjson.Result, value gjson.Result) bool {
				// We don't care about the exact contents for now.
				// If something was found we can assume the instantly available file of the torrent is the streamable video.
				if len(value.Get("rd").Array()) > 0 {
					infoHash := key.String()
					infoHash = strings.ToUpper(infoHash)
					result = append(result, infoHash)
					// Create cache item
					if err = c.availabilityCache.Set(infoHash); err != nil {
						c.logger.Error("Couldn't cache availability", zap.Error(err), zapFieldDebridSite, zapFieldAPItoken)
					}
				}
				return true
			})
		}
	}
	return result
}

func (c *Client) GetStreamURL(ctx context.Context, magnetURL, apiToken string, remote bool) (string, error) {
	zapFieldDebridSite := zap.String("debridSite", "RealDebrid")
	zapFieldAPItoken := zap.String("apiToken", apiToken)
	c.logger.Debug("Adding torrent to RealDebrid...", zapFieldDebridSite, zapFieldAPItoken)
	data := url.Values{}
	data.Set("magnet", magnetURL)
	resBytes, err := c.post(ctx, c.baseURL+"/rest/1.0/torrents/addMagnet", apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't add torrent to RealDebrid: %v", err)
	}
	c.logger.Debug("Finished adding torrent to RealDebrid", zapFieldDebridSite, zapFieldAPItoken)
	rdTorrentURL := gjson.GetBytes(resBytes, "uri").String()

	// Check RealDebrid torrent info

	c.logger.Debug("Checking torrent info...", zapFieldDebridSite, zapFieldAPItoken)
	// Use configured base URL, which could be a proxy that we want to go through
	rdTorrentURL, err = replaceURL(rdTorrentURL, c.baseURL)
	if err != nil {
		return "", fmt.Errorf("Couldn't replace URL which was retrieved from an HTML link: %v", err)
	}
	resBytes, err = c.get(ctx, rdTorrentURL, apiToken)
	if err != nil {
		return "", fmt.Errorf("Couldn't get torrent info from real-debrid.com: %v", err)
	}
	torrentID := gjson.GetBytes(resBytes, "id").String()
	if torrentID == "" {
		return "", errors.New("Couldn't get torrent info from real-debrid.com: response body doesn't contain \"id\" key")
	}
	fileResults := gjson.GetBytes(resBytes, "files").Array()
	if len(fileResults) == 0 || (len(fileResults) == 1 && fileResults[0].Raw == "") {
		return "", errors.New("Couldn't get torrent info from real-debrid.com: response body doesn't contain \"files\" key")
	}
	// TODO: Not required if we pass the instant available file ID from the availability check, but probably no huge performance implication
	fileID, err := selectFileID(ctx, fileResults)
	if err != nil {
		return "", fmt.Errorf("Couldn't find proper file in torrent: %v", err)
	}
	c.logger.Debug("Torrent info OK", zapFieldDebridSite, zapFieldAPItoken)

	// Add torrent to RealDebrid downloads

	c.logger.Debug("Adding torrent to RealDebrid downloads...", zapFieldDebridSite, zapFieldAPItoken)
	data = url.Values{}
	data.Set("files", fileID)
	_, err = c.post(ctx, c.baseURL+"/rest/1.0/torrents/selectFiles/"+torrentID, apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't add torrent to RealDebrid downloads: %v", err)
	}
	c.logger.Debug("Finished adding torrent to RealDebrid downloads", zapFieldDebridSite, zapFieldAPItoken)

	// Get torrent info (again)

	c.logger.Debug("Checking torrent status...", zapFieldDebridSite, zapFieldAPItoken)
	torrentStatus := ""
	waitForDownloadSeconds := 5
	waitedForDownloadSeconds := 0
	for torrentStatus != "downloaded" {
		resBytes, err = c.get(ctx, rdTorrentURL, apiToken)
		if err != nil {
			return "", fmt.Errorf("Couldn't get torrent info from real-debrid.com: %v", err)
		}
		torrentStatus = gjson.GetBytes(resBytes, "status").String()
		// Stop immediately if an error occurred.
		// Possible status: magnet_error, magnet_conversion, waiting_files_selection, queued, downloading, downloaded, error, virus, compressing, uploading, dead
		if torrentStatus == "magnet_error" ||
			torrentStatus == "error" ||
			torrentStatus == "virus" ||
			torrentStatus == "dead" {
			return "", fmt.Errorf("Bad torrent status: %v", torrentStatus)
		}
		// If status is before downloading (magnet_conversion, queued) or downloading, only wait 5 seconds
		// Note: This first condition also matches on waiting_files_selection, compressing and uploading, but these should never occur (we already selected a file and we're not uploading/compressing anything), but in case for some reason they match, well ok wait for 5 seconds as well.
		// Also matches future additional statuses that don't exist in the API yet. Well ok wait for 5 seconds as well.
		zapFieldTorrentStatus := zap.String("torrentStatus", torrentStatus)
		if torrentStatus != "downloading" && torrentStatus != "downloaded" {
			if waitedForDownloadSeconds < waitForDownloadSeconds {
				zapFieldRemainingWait := zap.String("remainingWait", strconv.Itoa(waitForDownloadSeconds-waitedForDownloadSeconds)+"s")
				c.logger.Debug("Waiting for download...", zapFieldRemainingWait, zapFieldTorrentStatus, zapFieldDebridSite, zapFieldAPItoken)
				waitedForDownloadSeconds++
			} else {
				zapFieldWaited := zap.String("waited", strconv.Itoa(waitForDownloadSeconds)+"s")
				c.logger.Debug("Torrent not downloading yet", zapFieldWaited, zapFieldTorrentStatus, zapFieldDebridSite, zapFieldAPItoken)
				return "", fmt.Errorf("Torrent still waiting for download (currently %v) on real-debrid.com after waiting for %v seconds", torrentStatus, waitForDownloadSeconds)
			}
		} else if torrentStatus == "downloading" {
			if waitedForDownloadSeconds < waitForDownloadSeconds {
				remainingWait := strconv.Itoa(waitForDownloadSeconds-waitedForDownloadSeconds) + "s"
				c.logger.Debug("Torrent downloading...", zap.String("remainingWait", remainingWait), zapFieldTorrentStatus, zapFieldDebridSite, zapFieldAPItoken)
				waitedForDownloadSeconds++
			} else {
				zapFieldWaited := zap.String("waited", strconv.Itoa(waitForDownloadSeconds)+"s")
				c.logger.Debug("Torrent still downloading", zapFieldWaited, zapFieldTorrentStatus, zapFieldDebridSite, zapFieldAPItoken)
				return "", fmt.Errorf("Torrent still %v on real-debrid.com after waiting for %v seconds", torrentStatus, waitForDownloadSeconds)
			}
		}
		time.Sleep(time.Second)
	}
	debridURL := gjson.GetBytes(resBytes, "links").Array()[0].String()
	c.logger.Debug("Torrent is downloaded", zapFieldDebridSite, zapFieldAPItoken)

	// Unrestrict link

	c.logger.Debug("Unrestricting link...", zapFieldDebridSite, zapFieldAPItoken)
	data = url.Values{}
	data.Set("link", debridURL)
	if remote {
		data.Set("remote", "1")
	}
	resBytes, err = c.post(ctx, c.baseURL+"/rest/1.0/unrestrict/link", apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't unrestrict link: %v", err)
	}
	streamURL := gjson.GetBytes(resBytes, "download").String()
	c.logger.Debug("Unrestricted link", zap.String("unrestrictedLink", streamURL), zapFieldDebridSite, zapFieldAPItoken)

	return streamURL, nil
}

// GetTorrents returns the *downloaded* torrents from RealDebrid.
func (c *Client) GetTorrents(ctx context.Context, apiToken string, logger *zap.Logger) ([]Torrent, error) {
	logger.Debug("Getting torrents...")
	url := c.baseURL + "/rest/1.0/torrents"
	resBytes, err := c.get(ctx, url, apiToken)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get torrents from real-debrid.com: %v", err)
	}
	var torrents []Torrent
	if err = json.Unmarshal(resBytes, &torrents); err != nil {
		return nil, fmt.Errorf("Couldn't unmarshal torrents: %v", err)
	}
	logger.Debug("Got torrents", zap.Int("torrentCount", len(torrents)))

	// Filter downloaded ones
	i := 0
	for _, torrent := range torrents {
		if torrent.Status == "downloaded" {
			torrents[i] = torrent
			i++
		}
	}
	downloadedTorrents := torrents[:i]

	return downloadedTorrents, nil
}

func (c *Client) get(ctx context.Context, url, apiToken string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create GET request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	for headerKey, headerVal := range c.extraHeaders {
		req.Header.Add(headerKey, headerVal)
	}
	// In case RD blocks requests based on User-Agent
	fakeVersion := strconv.Itoa(rand.Intn(10000))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0."+fakeVersion+".149 Safari/537.36")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't send GET request: %v", err)
	}
	defer res.Body.Close()

	// Check server response
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("Invalid token")
		} else if res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("Account locked")
		}
		resBody, _ := ioutil.ReadAll(res.Body)
		if len(resBody) == 0 {
			return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v')", res.Status, url)
		}
		return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v'; response body: '%s')", res.Status, url, resBody)
	}

	return ioutil.ReadAll(res.Body)
}

func (c *Client) post(ctx context.Context, url, apiToken string, data url.Values) ([]byte, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("Couldn't create POST request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for headerKey, headerVal := range c.extraHeaders {
		req.Header.Add(headerKey, headerVal)
	}
	// In case RD blocks requests based on User-Agent
	fakeVersion := strconv.Itoa(rand.Intn(10000))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0."+fakeVersion+".149 Safari/537.36")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't send POST request: %v", err)
	}
	defer res.Body.Close()

	// Check server response.
	// Different RealDebrid API POST endpoints return different status codes.
	if res.StatusCode != http.StatusCreated &&
		res.StatusCode != http.StatusNoContent &&
		res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("Invalid token")
		} else if res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("Account locked")
		}
		resBody, _ := ioutil.ReadAll(res.Body)
		if len(resBody) == 0 {
			return nil, fmt.Errorf("bad HTTP response status: %v (POST request to '%v')", res.Status, url)
		}
		return nil, fmt.Errorf("bad HTTP response status: %v (POST request to '%v'; response body: '%s')", res.Status, url, resBody)
	}

	return ioutil.ReadAll(res.Body)
}

func selectFileID(ctx context.Context, fileResults []gjson.Result) (string, error) {
	// Precondition check
	if len(fileResults) == 0 {
		return "", fmt.Errorf("Empty slice of files")
	}

	var fileID int64 // ID inside JSON starts with 1
	var size int64
	for _, res := range fileResults {
		if res.Get("bytes").Int() > size {
			size = res.Get("bytes").Int()
			fileID = res.Get("id").Int()
		}
	}

	if fileID == 0 {
		return "", fmt.Errorf("No file ID found")
	}

	return strconv.FormatInt(fileID, 10), nil
}

func replaceURL(origURL, newBaseURL string) (string, error) {
	// Replace by configured URL, which could be a proxy that we want to go through
	url, err := url.Parse(origURL)
	if err != nil {
		return "", fmt.Errorf("Couldn't parse URL. URL: %v; error: %v", origURL, err)
	}
	origBaseURL := url.Scheme + "://" + url.Host
	return strings.Replace(origURL, origBaseURL, newBaseURL, 1), nil
}
