package imdb2torrent

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	magnet2InfoHashRegex = regexp.MustCompile(`btih:.+?&`)     // The "?" makes the ".+" non-greedy
	regexMagnet          = regexp.MustCompile(`'magnet:?.+?'`) // The "?" makes the ".+" non-greedy
)

type MagnetSearcher interface {
	Check(ctx context.Context, imdbID string) ([]Result, error)
}

type Client struct {
	timeout     time.Duration
	siteClients map[string]MagnetSearcher
}

func NewClient(ctx context.Context, siteClients map[string]MagnetSearcher, timeout time.Duration) Client {
	return Client{
		timeout:     timeout,
		siteClients: siteClients,
	}
}

// FindMagnets tries to find magnet URLs for the given IMDb ID.
// It only returns 720p, 1080p, 1080p 10bit, 2160p and 2160p 10bit videos.
// It caches results once they're found.
// It can return an empty slice and no error if no actual error occurred (for example if torrents where found but no >=720p videos).
func (c Client) FindMagnets(ctx context.Context, imdbID string) ([]Result, error) {
	logger := log.WithContext(ctx).WithField("imdbID", imdbID)

	clientCount := len(c.siteClients)
	resChan := make(chan []Result, clientCount)
	errChan := make(chan error, clientCount)

	// Start all clients' searches in parallel.

	// Use a single timer that we can stop later, because with `case time.After()` ther will be lots of timers that won't be GCed.
	timer := time.NewTimer(c.timeout)
	for k, v := range c.siteClients {
		// Note: Let's not close the channels in the senders, as it would make the receiver's code more complex. The GC takes care of that.
		go func(clientName string, siteClient MagnetSearcher) {
			siteLogger := logger.WithField("torrentSite", clientName)
			siteLogger.Debug("Finding torrents...")
			siteResChan := make(chan []Result)
			siteErrChan := make(chan error)
			go func() {
				results, err := siteClient.Check(ctx, imdbID)
				if err != nil {
					siteLogger.WithError(err).Warn("Couldn't find torrents")
					siteErrChan <- err
				} else {
					siteLogger.WithField("torrentCount", len(results)).Debug("Found torrents")
					siteResChan <- results
				}
			}()
			select {
			case res := <-siteResChan:
				resChan <- res
			case err := <-siteErrChan:
				errChan <- err
			case <-timer.C:
				siteLogger.Warn("Finding torrents timed out. It will continue to run in the background.")
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
		logger.Warn("Couldn't find ANY torrents")
	}

	return noDupResults, nil
}

func (c Client) GetMagnetSearchers() map[string]MagnetSearcher {
	return c.siteClients
}

type Result struct {
	Title string
	// For example "720p" or "720p (web)"
	Quality   string
	InfoHash  string
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
