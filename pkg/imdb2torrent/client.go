package imdb2torrent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/doingodswork/deflix-stremio/pkg/cinemata"
	log "github.com/sirupsen/logrus"
)

var (
	magnet2InfoHashRegex = regexp.MustCompile("btih:.+?&")     // The "?" makes the ".+" non-greedy
	regexMagnet          = regexp.MustCompile("'magnet:?.+?'") // The "?" makes the ".+" non-greedy
)

type Client struct {
	timeout     time.Duration
	ytsClient   ytsClient
	tpbClient   tpbClient
	leetxClient leetxClient
	ibitClient  ibitClient
}

func NewClient(ctx context.Context, baseURLyts, baseURLtpb, baseURL1337x, baseURLibit string, timeout time.Duration, torrentCache *fastcache.Cache, cinemataCache *fastcache.Cache) Client {
	cinemataClient := cinemata.NewClient(ctx, timeout, cinemataCache)
	return Client{
		timeout:     timeout,
		ytsClient:   newYTSclient(ctx, baseURLyts, timeout, torrentCache),
		tpbClient:   newTPBclient(ctx, baseURLtpb, timeout, torrentCache),
		leetxClient: newLeetxclient(ctx, baseURL1337x, timeout, torrentCache, cinemataClient),
		ibitClient:  newIbitClient(ctx, baseURLibit, timeout, torrentCache),
	}
}

// FindMagnets tries to find magnet URLs for the given IMDb ID.
// It only returns 720p, 1080p, 1080p 10bit, 2160p and 2160p 10bit videos.
// It caches results once they're found.
// It can return an empty slice and no error if no actual error occurred (for example if torrents where found but no >=720p videos).
func (c Client) FindMagnets(ctx context.Context, imdbID string) ([]Result, error) {
	logger := log.WithContext(ctx).WithField("imdbID", imdbID)

	torrentSiteCount := 3
	resChan := make(chan []Result, torrentSiteCount)
	errChan := make(chan error, torrentSiteCount)

	// YTS
	go func() {
		logger.WithField("torrentSite", "YTS").Debug("Started searching torrents...")
		results, err := c.ytsClient.check(ctx, imdbID)
		if err != nil {
			logger.WithError(err).WithField("torrentSite", "YTS").Debug("Couldn't find torrents")
			errChan <- err
		} else {
			fields := log.Fields{
				"torrentSite":  "YTS",
				"torrentCount": len(results),
			}
			logger.WithFields(fields).Debug("Found torrents")
			resChan <- results
		}
	}()

	// TPB
	go func() {
		logger.WithField("torrentSite", "TPB").Debug("Started searching torrents...")
		results, err := c.tpbClient.check(ctx, imdbID, 2)
		if err != nil {
			logger.WithError(err).WithField("torrentSite", "TPB").Debug("Couldn't find torrents")
			errChan <- err
		} else {
			fields := log.Fields{
				"torrentSite":  "TPB",
				"torrentCount": len(results),
			}
			logger.WithFields(fields).Debug("Found torrents")
			resChan <- results
		}
	}()

	// 1337x
	go func() {
		logger.WithField("torrentSite", "1337x").Debug("Started searching torrents...")
		results, err := c.leetxClient.check(ctx, imdbID)
		if err != nil {
			logger.WithError(err).WithField("torrentSite", "1337x").Debug("Couldn't find torrents")
			errChan <- err
		} else {
			fields := log.Fields{
				"torrentSite":  "1337x",
				"torrentCount": len(results),
			}
			logger.WithFields(fields).Debug("Found torrents")
			resChan <- results
		}
	}()

	// ibit
	// Note: An initial movie search takes long, because multiple requests need to be made, but ibit uses rate limiting, so we can't do them concurrently.
	// So let's treat this special: Make the request, but only wait for 1 second (in case the cache is filled), then don't cancel the operation, but let it run in the background so the cache gets filled.
	// With the next movie search for the same IMDb ID the cache is used.
	ibitResChan := make(chan []Result)
	ibitErrChan := make(chan error)
	go func() {
		logger.WithField("torrentSite", "ibit").Debug("Started searching torrents...")
		ibitResults, err := c.ibitClient.check(ctx, imdbID)
		if err != nil {
			logger.WithError(err).WithField("torrentSite", "ibit").Debug("Couldn't find torrents")
			ibitErrChan <- err
		} else {
			fields := log.Fields{
				"torrentSite":  "ibit",
				"torrentCount": len(ibitResults),
			}
			logger.WithFields(fields).Debug("Found torrents")
			ibitResChan <- ibitResults
		}
	}()

	// Collect results from all except ibit.
	var combinedResults []Result
	var errs []error
	dupRemovalRequired := false
	for i := 0; i < torrentSiteCount; i++ {
		// No timeout for the goroutines because their HTTP client has a timeout already
		select {
		case err := <-errChan:
			errs = append(errs, err)
		case results := <-resChan:
			if !dupRemovalRequired && len(combinedResults) > 0 && len(results) > 0 {
				dupRemovalRequired = true
			}
			combinedResults = append(combinedResults, results...)
		}
	}
	close(resChan)
	close(errChan)

	returnErrors := len(errs) == torrentSiteCount

	// Now collect result from ibit if it's there.
	var closeChansOk bool
	select {
	case err := <-ibitErrChan:
		errs = append(errs, err)
		closeChansOk = true
	case results := <-ibitResChan:
		if !dupRemovalRequired && len(combinedResults) > 0 && len(results) > 0 {
			dupRemovalRequired = true
		}
		combinedResults = append(combinedResults, results...)
		returnErrors = false
		closeChansOk = true
	case <-time.After(1 * time.Second):
		logger.WithField("torrentSite", "ibit").Info("torrent search hasn't finished yet, we'll let it run in the background")
	}
	if closeChansOk {
		close(ibitErrChan)
		close(ibitResChan)
	}

	// Return error (only) if all torrent sites returned actual errors (and not just empty results)
	if returnErrors {
		errsMsg := "Couldn't find torrents on any site: "
		for i := 1; i <= torrentSiteCount; i++ {
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

type Result struct {
	Title string
	// For example "720p" or "720p (web)"
	Quality   string
	InfoHash  string
	MagnetURL string
}
