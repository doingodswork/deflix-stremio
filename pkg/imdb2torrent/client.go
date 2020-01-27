package imdb2torrent

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	httpClient *http.Client
	cache      *cache
}

func NewClient(timeout time.Duration) Client {
	return Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: newCache(),
	}
}

// FindMagnets tries to find magnet URLs for the given IMDb ID.
// It only returns 720p and 1080p videos.
// It caches results once they're found.
// It can return an empty slice and no error if no actual error occurred (for example if torrents where found but no >720p videos).
func (c Client) FindMagnets(imdbID string) ([]Result, error) {
	// Check cache first
	if results := c.cache.get(imdbID); len(results) > 0 {
		return results, nil
	}

	torrentSiteCount := 2
	resChan := make(chan []Result, torrentSiteCount)
	errChan := make(chan error, torrentSiteCount)

	// YTS
	go func() {
		results, err := c.checkYTS(imdbID)
		if err != nil {
			log.Println("Couldn't find torrents on YTS:", err)
			errChan <- err
		} else {
			if len(results) == 0 {
				log.Println("No torrents found on YTS")
			}
			resChan <- results
		}
	}()

	// TPB
	go func() {
		results, err := c.checkTPB(imdbID)
		if err != nil {
			log.Println("Couldn't find torrents on TPB:", err)
			errChan <- err
		} else {
			if len(results) == 0 {
				log.Println("No torrents found on TPB")
			}
			resChan <- results
		}
	}()

	// TODO: Check other torrent sites

	combinedResults := []Result{}
	errs := []error{}
	dupRemovalRequired := false
	for i := torrentSiteCount; i > 0; i-- {
		// No timeout for the goroutines because their HTTP client has a timeout already
		select {
		case err := <-errChan:
			errs = append(errs, err)
		case results := <-resChan:
			combinedResults = append(combinedResults, results...)
			if !dupRemovalRequired && len(combinedResults) > 0 && len(results) > 0 {
				dupRemovalRequired = true
			}
		}
	}

	// Return error (only) if all torrent sites returned actual errors (and not just empty results)
	if len(errs) == torrentSiteCount {
		errsMsg := "Couldn't find torrents on any site: "
		for i := 1; i <= torrentSiteCount; i++ {
			errsMsg += fmt.Sprintf("%v.: %v; ", i, errs[i])
		}
		errsMsg = strings.TrimSuffix(errsMsg, "; ")
		return nil, fmt.Errorf(errsMsg)
	}

	// Remove duplicates.
	// Only necessary if we got non-empty results from more than one torrent site.
	noDupResults := []Result{}
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

	if len(noDupResults) <= 0 {
		log.Println("Couldn't find ANY torrents for IMDb ID", imdbID)
	} else {
		c.cache.set(imdbID, noDupResults)
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
