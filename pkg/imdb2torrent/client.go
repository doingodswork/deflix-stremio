package imdb2torrent

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

var (
	magnet2InfoHashRegex = regexp.MustCompile(`btih:.+?&`)     // The "?" makes the ".+" non-greedy
	regexMagnet          = regexp.MustCompile(`'magnet:?.+?'`) // The "?" makes the ".+" non-greedy
)

type MagnetSearcher interface {
	Find(ctx context.Context, imdbID string) ([]Result, error)
	IsSlow() bool
}

type Client struct {
	timeout     time.Duration
	siteClients map[string]MagnetSearcher
	logger      *zap.Logger
}

func NewClient(siteClients map[string]MagnetSearcher, timeout time.Duration, logger *zap.Logger) *Client {
	return &Client{
		timeout:     timeout,
		siteClients: siteClients,
		logger:      logger,
	}
}

// FindMagnets tries to find magnet URLs for the given IMDb ID.
// It only returns 720p, 1080p, 1080p 10bit, 2160p and 2160p 10bit videos.
// It caches results once they're found.
// It can return an empty slice and no error if no actual error occurred (for example if torrents where found but no >=720p videos).
func (c *Client) FindMagnets(ctx context.Context, imdbID string) ([]Result, error) {
	zapFieldID := zap.String("imdbID", imdbID)

	clientCount := len(c.siteClients)
	resChan := make(chan []Result, clientCount)
	errChan := make(chan error, clientCount)

	// Start all clients' searches in parallel.

	// Use a single timer that we can stop later, because with `case time.After()` ther will be lots of timers that won't be GCed.
	timer := time.NewTimer(c.timeout)
	quickSkipTimer := time.NewTimer(2 * time.Second)
	for k, v := range c.siteClients {
		// Note: Let's not close the channels in the senders, as it would make the receiver's code more complex. The GC takes care of that.
		go func(clientName string, siteClient MagnetSearcher) {
			zapFieldTorrentSite := zap.String("torrentSite", clientName)
			c.logger.Debug("Finding torrents...", zapFieldID, zapFieldTorrentSite)
			siteResChan := make(chan []Result)
			siteErrChan := make(chan error)
			go func() {
				siteStart := time.Now()
				results, err := siteClient.Find(ctx, imdbID)
				if err != nil {
					c.logger.Warn("Couldn't find torrents", zap.Error(err), zapFieldID, zapFieldTorrentSite)
					siteErrChan <- err
				} else {
					duration := time.Since(siteStart).Milliseconds()
					durationString := strconv.FormatInt(duration, 10)
					c.logger.Debug("Found torrents", zap.Int("torrentCount", len(results)), zap.String("duration", durationString+"ms"), zapFieldID, zapFieldTorrentSite)
					siteResChan <- results
				}
			}()
			timeoutChan := timer.C
			if siteClient.IsSlow() {
				timeoutChan = quickSkipTimer.C
			}
			select {
			case res := <-siteResChan:
				resChan <- res
			case err := <-siteErrChan:
				errChan <- err
			case <-timeoutChan:
				c.logger.Warn("Finding torrents timed out. It will continue to run in the background.", zapFieldID, zapFieldTorrentSite)
				resChan <- nil
			}
		}(k, v)
	}

	// Collect results from all clients.

	var combinedResults []Result
	var errs []error
	dupRemovalRequired := false
	// For each client we get either a result or an error.
	// The timeout is handled in the site specific goroutine, because if we would use it here, and there were 4 clients and a timeout of 5 seconds, it could lead to 4*5=20 seconds of waiting time.
	for i := 0; i < clientCount; i++ {
		select {
		case results := <-resChan:
			if !dupRemovalRequired && len(combinedResults) > 0 && len(results) > 0 {
				dupRemovalRequired = true
			}
			combinedResults = append(combinedResults, results...)
		case err := <-errChan:
			errs = append(errs, err)
		}
	}
	timer.Stop()
	quickSkipTimer.Stop()

	returnErrors := len(errs) == clientCount

	// Return error (only) if all torrent sites returned actual errors (and not just empty results)
	if returnErrors {
		errsMsg := "Couldn't find torrents on any site: "
		for i := 1; i <= clientCount; i++ {
			errsMsg += fmt.Sprintf("%v.: %v; ", i, errs[i-1])
		}
		errsMsg = strings.TrimSuffix(errsMsg, "; ")
		return nil, fmt.Errorf(errsMsg)
	}

	// Remove duplicates.
	// Only necessary if we got non-empty results from more than one torrent site.
	var noDupResults []Result
	if dupRemovalRequired {
		infoHashes := map[string]struct{}{}
		for _, result := range combinedResults {
			if _, ok := infoHashes[result.InfoHash]; !ok {
				noDupResults = append(noDupResults, result)
				infoHashes[result.InfoHash] = struct{}{}
			}
		}
	} else {
		noDupResults = combinedResults
	}

	if len(noDupResults) == 0 {
		c.logger.Warn("Couldn't find ANY torrents", zapFieldID)
	}

	return noDupResults, nil
}

func (c *Client) GetMagnetSearchers() map[string]MagnetSearcher {
	return c.siteClients
}

type Result struct {
	// Movie title, e.g. "Big Buck Bunny"
	Title string
	// Video resolution and source, e.g. "720p" or "720p (web)"
	Quality string
	// Torrent info_hash
	InfoHash string
	// MagnetURL, usually containing the info_hash, torrent name and a list of torrent trackers
	MagnetURL string
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

func createMagnetURL(ctx context.Context, infoHash, title string, trackers []string) string {
	magnetURL := "magnet:?xt=urn:btih:" + infoHash + "&dn=" + url.QueryEscape(title)
	for _, tracker := range trackers {
		magnetURL += "&tr" + tracker
	}
	return magnetURL
}
