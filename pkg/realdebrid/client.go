package realdebrid

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type Client struct {
	httpClient *http.Client
	// For API token validity
	tokenCache *fastcache.Cache
	// For info_hash instant availability
	availabilityCache *fastcache.Cache
	cacheAge          time.Duration
	rdBaseURL         string
	extraHeaderKey    string
	extraHeaderVal    string
}

func NewClient(ctx context.Context, timeout time.Duration, tokenCache, availabilityCache *fastcache.Cache, cacheAge time.Duration, rdBaseURL, extraHeader string) (Client, error) {
	// Precondition check
	if rdBaseURL == "" {
		return Client{}, errors.New("rdBaseURL parameter must not be empty")
	}
	if extraHeader != "" {
		colonIndex := strings.Index(extraHeader, ":")
		if colonIndex <= 0 || colonIndex == len(extraHeader)-1 {
			return Client{}, errors.New("extraHeader parameter must have a format like \"X-Foo: bar\"")
		}
	}

	var extraHeaderKey string
	var extraHeaderVal string
	if extraHeader != "" {
		extraHeaderParts := strings.SplitN(extraHeader, ":", 2)
		extraHeaderKey = strings.TrimSpace(extraHeaderParts[0])
		extraHeaderVal = strings.TrimSpace(extraHeaderParts[1])
	}

	return Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		tokenCache:        tokenCache,
		availabilityCache: availabilityCache,
		cacheAge:          cacheAge,
		rdBaseURL:         rdBaseURL,
		extraHeaderKey:    extraHeaderKey,
		extraHeaderVal:    extraHeaderVal,
	}, nil
}

func (c Client) TestToken(ctx context.Context, apiToken string) error {
	logger := log.WithContext(ctx).WithField("apiToken", apiToken)
	logger.Debug("Testing token...")

	// Check cache first.
	// Note: Only when a token is valid a cache entry was created, because a token is probably valid for another 24 hours, while when a token is invalid it's likely that the user makes a payment to RealDebrid to extend his premium status and make his token valid again *within* 24 hours.
	if tokenGob, ok := c.tokenCache.HasGet(nil, []byte(apiToken)); ok {
		created, err := fromCacheEntry(ctx, tokenGob)
		if err != nil {
			logger.WithError(err).Error("Couldn't decode token cache entry")
		} else if time.Since(created) < (24 * time.Hour) {
			logger.Debug("Token cached as valid")
			return nil
		} else {
			expiredSince := time.Since(created.Add(24 * time.Hour))
			logger.WithField("expiredSince", expiredSince).Debug("Token cached as valid, but entry is expired")
		}
	}

	resBytes, err := c.get(ctx, c.rdBaseURL+"/rest/1.0/user", apiToken)
	if err != nil {
		return fmt.Errorf("Couldn't fetch user info from real-debrid.com with the provided token: %v", err)
	}
	if !gjson.GetBytes(resBytes, "id").Exists() {
		return fmt.Errorf("Couldn't parse user info response from real-debrid.com")
	}

	logger.Debug("Token OK")

	// Create cache entry
	if tokenGob, err := newCacheEntry(ctx); err != nil {
		logger.WithError(err).Error("Couldn't encode token cache entry")
	} else {
		c.tokenCache.Set([]byte(apiToken), tokenGob)
	}

	return nil
}

func (c Client) CheckInstantAvailability(ctx context.Context, apiToken string, infoHashes ...string) []string {
	logger := log.WithContext(ctx).WithField("apiToken", apiToken)

	// Precondition check
	if len(infoHashes) == 0 {
		return nil
	}

	url := c.rdBaseURL + "/rest/1.0/torrents/instantAvailability"
	// Only check the ones of which we don't know that they're valid (or which our knowledge that they're valid is more than 24 hours old).
	// We don't cache unavailable ones, because that might change often!
	var result []string
	requestRequired := false
	for _, infoHash := range infoHashes {
		if availabilityGob, ok := c.availabilityCache.HasGet(nil, []byte(infoHash)); ok {
			created, err := fromCacheEntry(ctx, availabilityGob)
			if err != nil {
				logger.WithError(err).WithField("infoHash", infoHash).Error("Couldn't decode availability cache entry")
				requestRequired = true
				url += "/" + infoHash
			} else if time.Since(created) < (c.cacheAge) {
				logger.WithField("infoHash", infoHash).Debug("Availability cached as valid")
				result = append(result, infoHash)
			} else {
				fields := log.Fields{
					"infoHash":     infoHash,
					"expiredSince": time.Since(created.Add(c.cacheAge)),
				}
				logger.WithFields(fields).Debug("Availability cached as valid, but entry is expired")
				requestRequired = true
				url += "/" + infoHash
			}
		} else {
			requestRequired = true
			url += "/" + infoHash
		}
	}

	// Only make HTTP request if we didn't find all hashes in the cache yet
	if requestRequired {
		resBytes, err := c.get(ctx, url, apiToken)
		if err != nil {
			logger.WithError(err).Error("Couldn't check torrents' instant availability on real-debrid.com")
		} else {
			// Note: This iterates through all elements with the key being the info_hash
			gjson.ParseBytes(resBytes).ForEach(func(key gjson.Result, value gjson.Result) bool {
				// We don't care about the exact contents for now.
				// If something was found we can assume the instantly available file of the torrent is the streamable video.
				if len(value.Get("rd").Array()) > 0 {
					infoHash := key.String()
					infoHash = strings.ToUpper(infoHash)
					result = append(result, infoHash)
					// Create cache entry
					if availabilityGob, err := newCacheEntry(ctx); err != nil {
						logger.WithError(err).Error("Couldn't encode availability cache entry")
					} else {
						c.availabilityCache.Set([]byte(infoHash), availabilityGob)
					}
				}
				return true
			})
		}
	}
	return result
}

func (c Client) GetStreamURL(ctx context.Context, magnetURL, apiToken string, remote bool) (string, error) {
	logger := log.WithContext(ctx).WithField("apiToken", apiToken)
	logger.Debug("Adding torrent to RealDebrid...")
	data := url.Values{}
	data.Set("magnet", magnetURL)
	resBytes, err := c.post(ctx, c.rdBaseURL+"/rest/1.0/torrents/addMagnet", apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't add torrent to RealDebrid: %v", err)
	}
	logger.Debug("Finished adding torrent to RealDebrid")
	rdTorrentURL := gjson.GetBytes(resBytes, "uri").String()

	// Check RealDebrid torrent info

	logger.Debug("Checking torrent info...")
	resBytes, err = c.get(ctx, rdTorrentURL, apiToken)
	if err != nil {
		return "", fmt.Errorf("Couldn't get torrent info from real-debrid.com: %v", err)
	}
	torrentID := gjson.GetBytes(resBytes, "id").String()
	fileResults := gjson.GetBytes(resBytes, "files").Array()
	// TODO: Not required if we pass the instant available file ID from the availability check, but probably no huge performance implication
	fileID, err := selectFileID(ctx, fileResults)
	if err != nil {
		return "", fmt.Errorf("Couldn't find proper file in torrent: %v", err)
	}
	logger.Debug("Torrent info OK")

	// Add torrent to RealDebrid downloads

	logger.Debug("Adding torrent to RealDebrid downloads...")
	data = url.Values{}
	data.Set("files", fileID)
	_, err = c.post(ctx, c.rdBaseURL+"/rest/1.0/torrents/selectFiles/"+torrentID, apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't add torrent to RealDebrid downloads: %v", err)
	}
	logger.Debug("Finished adding torrent to RealDebrid downloads")

	// Get torrent info (again)

	logger.Debug("Checking torrent status...")
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
		if torrentStatus != "downloading" && torrentStatus != "downloaded" {
			if waitedForDownloadSeconds < waitForDownloadSeconds {
				fields := log.Fields{
					"torrentStatus": torrentStatus,
					"remainingWait": strconv.Itoa(waitForDownloadSeconds-waitedForDownloadSeconds) + "s",
				}
				logger.WithFields(fields).Debug("Waiting for download...")
				waitedForDownloadSeconds++
			} else {
				fields := log.Fields{
					"torrentStatus": torrentStatus,
					"waited":        strconv.Itoa(waitForDownloadSeconds) + "s",
				}
				logger.WithFields(fields).Debug("Torrent not downloading yet")
				return "", fmt.Errorf("Torrent still waiting for download (currently %v) on real-debrid.com after waiting for %v seconds", torrentStatus, waitForDownloadSeconds)
			}
		} else if torrentStatus == "downloading" {
			if waitedForDownloadSeconds < waitForDownloadSeconds {
				remainingWait := strconv.Itoa(waitForDownloadSeconds-waitedForDownloadSeconds) + "s"
				logger.WithField("remainingWait", remainingWait).Debug("Torrent downloading...")
				waitedForDownloadSeconds++
			} else {
				fields := log.Fields{
					"torrentStatus": torrentStatus,
					"waited":        strconv.Itoa(waitForDownloadSeconds) + "s",
				}
				logger.WithFields(fields).Debug("Torrent still downloading")
				return "", fmt.Errorf("Torrent still %v on real-debrid.com after waiting for %v seconds", torrentStatus, waitForDownloadSeconds)
			}
		}
		time.Sleep(time.Second)
	}
	debridURL := gjson.GetBytes(resBytes, "links").Array()[0].String()
	logger.Debug("Torrent is downloaded")

	// Unrestrict link

	logger.Debug("Unrestricting link...")
	data = url.Values{}
	data.Set("link", debridURL)
	if remote {
		data.Set("remote", "1")
	}
	resBytes, err = c.post(ctx, c.rdBaseURL+"/rest/1.0/unrestrict/link", apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't unrestrict link: %v", err)
	}
	streamURL := gjson.GetBytes(resBytes, "download").String()
	logger.WithField("unrestrictedLink", streamURL).Debug("Unrestricted link")

	return streamURL, nil
}

func (c Client) get(ctx context.Context, url, apiToken string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create GET request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	if c.extraHeaderKey != "" {
		req.Header.Set(c.extraHeaderKey, c.extraHeaderVal)
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
		return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v')", res.Status, url)
	}

	return ioutil.ReadAll(res.Body)
}

func (c Client) post(ctx context.Context, url, apiToken string, data url.Values) ([]byte, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("Couldn't create POST request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.extraHeaderKey != "" {
		req.Header.Set(c.extraHeaderKey, c.extraHeaderVal)
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
