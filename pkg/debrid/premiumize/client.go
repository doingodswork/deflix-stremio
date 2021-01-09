package premiumize

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
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
	UseOAUTH2    bool
	// When setting this to true, the user's original IP address is read from the context parameter with the key "debrid_originIP".
	ForwardOriginIP bool
}

func NewClientOpts(baseURL string, timeout, cacheAge time.Duration, extraHeaders []string, useOAUTH2 bool, forwardOriginIP bool) ClientOptions {
	return ClientOptions{
		BaseURL:         baseURL,
		Timeout:         timeout,
		CacheAge:        cacheAge,
		ExtraHeaders:    extraHeaders,
		UseOAUTH2:       useOAUTH2,
		ForwardOriginIP: forwardOriginIP,
	}
}

var DefaultClientOpts = ClientOptions{
	BaseURL:  "https://www.premiumize.me/api",
	Timeout:  5 * time.Second,
	CacheAge: 24 * time.Hour,
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	// For API key validity
	apiKeyCache debrid.Cache
	// For info_hash instant availability
	availabilityCache debrid.Cache
	cacheAge          time.Duration
	extraHeaders      map[string]string
	useOAUTH2         bool
	forwardOriginIP   bool
	logger            *zap.Logger
}

func NewClient(opts ClientOptions, apiKeyCache, availabilityCache debrid.Cache, logger *zap.Logger) (*Client, error) {
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
		apiKeyCache:       apiKeyCache,
		availabilityCache: availabilityCache,
		cacheAge:          opts.CacheAge,
		extraHeaders:      extraHeaderMap,
		useOAUTH2:         opts.UseOAUTH2,
		logger:            logger,
	}, nil
}

func (c *Client) TestAPIkey(ctx context.Context, keyOrToken string) error {
	zapFieldDebridSite := zap.String("debridSite", "Premiumize")
	zapFieldAPIkey := zap.String("keyOrToken", keyOrToken)
	c.logger.Debug("Testing API key...", zapFieldDebridSite, zapFieldAPIkey)

	// Check cache first.
	// Note: Only when an API key is valid a cache item was created, becausean API key is probably valid for another 24 hours, while whenan API key is invalid it's likely that the user makes a payment to Premiumize to extend his premium status and make his API key valid again *within* 24 hours.
	created, found, err := c.apiKeyCache.Get(keyOrToken)
	if err != nil {
		c.logger.Error("Couldn't decode API key cache item", zap.Error(err), zapFieldDebridSite, zapFieldAPIkey)
	} else if !found {
		c.logger.Debug("API key not found in cache", zapFieldDebridSite, zapFieldAPIkey)
	} else if time.Since(created) > (24 * time.Hour) {
		expiredSince := time.Since(created.Add(24 * time.Hour))
		c.logger.Debug("API key cached as valid, but item is expired", zap.Duration("expiredSince", expiredSince), zapFieldDebridSite, zapFieldAPIkey)
	} else {
		c.logger.Debug("API key cached as valid", zapFieldDebridSite, zapFieldAPIkey)
		return nil
	}

	resBytes, err := c.get(ctx, c.baseURL+"/account/info", keyOrToken)
	if err != nil {
		return fmt.Errorf("Couldn't fetch user info from www.premiumize.me with the provided API key: %v", err)
	}
	if gjson.GetBytes(resBytes, "status").String() != "success" {
		errMsg := gjson.GetBytes(resBytes, "message").String()
		return fmt.Errorf("Got error response from www.premiumize.me: %v", errMsg)
	}

	c.logger.Debug("API key OK", zapFieldDebridSite, zapFieldAPIkey)

	// Create cache item
	if err = c.apiKeyCache.Set(keyOrToken); err != nil {
		c.logger.Error("Couldn't cache API key", zap.Error(err), zapFieldDebridSite, zapFieldAPIkey)
	}

	return nil
}

func (c *Client) CheckInstantAvailability(ctx context.Context, keyOrToken string, infoHashes ...string) []string {
	zapFieldDebridSite := zap.String("debridSite", "Premiumize")
	zapFieldAPItoken := zap.String("keyOrToken", keyOrToken)

	// Precondition check
	if len(infoHashes) == 0 {
		return nil
	}

	// Only check the ones of which we don't know that they're valid (or which our knowledge that they're valid is more than 24 hours old).
	// We don't cache unavailable ones, because that might change often!
	var result []string
	infoHashesNotFound := false
	infoHashesExpired := false
	infoHashesValid := false
	requestRequired := false
	var unknownAvailailabilityValues []string
	for _, infoHash := range infoHashes {
		zapFieldInfoHash := zap.String("infoHash", infoHash)
		created, found, err := c.availabilityCache.Get(infoHash)
		if err != nil {
			c.logger.Error("Couldn't decode availability cache item", zap.Error(err), zapFieldInfoHash, zapFieldDebridSite, zapFieldAPItoken)
			requestRequired = true
			unknownAvailailabilityValues = append(unknownAvailailabilityValues, infoHash)
		} else if !found {
			infoHashesNotFound = true
			requestRequired = true
			unknownAvailailabilityValues = append(unknownAvailailabilityValues, infoHash)
		} else if time.Since(created) > (c.cacheAge) {
			infoHashesExpired = true
			requestRequired = true
			unknownAvailailabilityValues = append(unknownAvailailabilityValues, infoHash)
		} else {
			infoHashesValid = true
			result = append(result, infoHash)
		}
	}
	var unknownAvailabilityData url.Values
	if len(unknownAvailailabilityValues) > 0 {
		unknownAvailabilityData = url.Values{"items[]": unknownAvailailabilityValues}
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
		url := c.baseURL + "/cache/check"
		resBytes, err := c.post(ctx, url, keyOrToken, unknownAvailabilityData, false)
		if err != nil {
			c.logger.Error("Couldn't check torrents' instant availability on www.premiumize.me", zap.Error(err), zapFieldDebridSite, zapFieldAPItoken)
			return nil
		}
		if gjson.GetBytes(resBytes, "status").String() != "success" {
			errMsg := gjson.GetBytes(resBytes, "message").String()
			c.logger.Error("Got error response from www.premiumize.me", zap.String("errorMessage", errMsg))
			return nil
		}
		boolResponse := gjson.ParseBytes(resBytes).Get("response").Array()
		for i, boolItem := range boolResponse {
			isAvailable := boolItem.Bool()
			if !isAvailable {
				continue
			}
			infoHash := unknownAvailailabilityValues[i]
			infoHash = strings.ToUpper(infoHash)
			result = append(result, infoHash)
			// Create cache item
			if err = c.availabilityCache.Set(infoHash); err != nil {
				c.logger.Error("Couldn't cache availability", zap.Error(err), zapFieldDebridSite, zapFieldAPItoken)
			}
		}
	}
	return result
}

func (c *Client) GetStreamURL(ctx context.Context, magnetURL, keyOrToken string) (string, error) {
	zapFieldDebridSite := zap.String("debridSite", "Premiumize")
	zapFieldAPIkey := zap.String("keyOrToken", keyOrToken)
	c.logger.Debug("Adding magnet to Premiumize...", zapFieldDebridSite, zapFieldAPIkey)
	data := url.Values{}
	data.Set("src", magnetURL)
	// Different from RealDebrid, Premiumize asks for the original IP only for directdl requests
	if c.forwardOriginIP && ctx.Value("debrid_originIP") != nil {
		ip := ctx.Value("debrid_originIP").(string)
		data.Add("download_ip", ip)
	}
	resBytes, err := c.post(ctx, c.baseURL+"/transfer/directdl", keyOrToken, data, true)
	if err != nil {
		return "", fmt.Errorf("Couldn't add magnet to Premiumize: %v", err)
	}
	if gjson.GetBytes(resBytes, "status").String() != "success" {
		errMsg := gjson.GetBytes(resBytes, "message").String()
		return "", fmt.Errorf("Got error response from www.premiumize.me: %v", errMsg)
	}
	c.logger.Debug("Finished adding magnet to Premiumize", zapFieldDebridSite, zapFieldAPIkey)
	content := gjson.GetBytes(resBytes, "content").Array()
	ddlLink, err := selectLink(ctx, content)
	if err != nil {
		return "", fmt.Errorf("Couldn't find proper link in magnet status: %v", err)
	} else if ddlLink == "" {
		return "", fmt.Errorf("Couldn't find proper link in magnet status")
	}
	c.logger.Debug("Created direct download link", zap.String("ddlLink", ddlLink), zapFieldDebridSite, zapFieldAPIkey)

	return ddlLink, nil
}

func (c *Client) get(ctx context.Context, url, keyOrToken string) ([]byte, error) {
	if c.useOAUTH2 {
		url += "?access_token=" + keyOrToken
	} else {
		url += "?apikey=" + keyOrToken
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create GET request: %v", err)
	}
	for headerKey, headerVal := range c.extraHeaders {
		req.Header.Add(headerKey, headerVal)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't send GET request: %v", err)
	}
	defer res.Body.Close()

	// Check server response
	if res.StatusCode != http.StatusOK {
		resBody, _ := ioutil.ReadAll(res.Body)
		if len(resBody) == 0 {
			return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v')", res.Status, url)
		}
		return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v'; response body: '%s')", res.Status, url, resBody)
	}

	return ioutil.ReadAll(res.Body)
}

func (c *Client) post(ctx context.Context, urlString, keyOrToken string, data url.Values, form bool) ([]byte, error) {
	if c.useOAUTH2 {
		urlString += "?access_token=" + keyOrToken
	} else {
		urlString += "?apikey=" + keyOrToken
	}
	var req *http.Request
	var err error
	if form {
		req, err = http.NewRequest("POST", urlString, strings.NewReader(data.Encode()))
	} else {
		// map[string][]string
		for k, vals := range data {
			for _, val := range vals {
				urlString += "&" + url.QueryEscape(k) + "=" + url.QueryEscape(val)
			}
		}
		req, err = http.NewRequest("POST", urlString, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("Couldn't create POST request: %v", err)
	}
	if form {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for headerKey, headerVal := range c.extraHeaders {
		req.Header.Add(headerKey, headerVal)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't send POST request: %v", err)
	}
	defer res.Body.Close()

	// Check server response
	if res.StatusCode != http.StatusOK {
		resBody, _ := ioutil.ReadAll(res.Body)
		if len(resBody) == 0 {
			return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v')", res.Status, urlString)
		}
		return nil, fmt.Errorf("bad HTTP response status: %v (GET request to '%v'; response body: '%s')", res.Status, urlString, resBody)
	}

	return ioutil.ReadAll(res.Body)
}

func selectLink(ctx context.Context, linkResults []gjson.Result) (string, error) {
	// Precondition check
	if len(linkResults) == 0 {
		return "", fmt.Errorf("Empty slice of content")
	}

	var link string
	var size int64
	for _, res := range linkResults {
		if res.Get("size").Int() > size {
			size = res.Get("size").Int()
			link = res.Get("link").String()
		}
	}

	if link == "" {
		return "", fmt.Errorf("No link found")
	}

	return link, nil
}
