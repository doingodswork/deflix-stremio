package imdb2torrent

import (
	"fmt"
	"log"
	"strings"
	"time"
)

type Client struct {
	ytsClient ytsClient
	tpbClient tpbClient
}

func NewClient(timeout time.Duration) Client {
	return Client{
		ytsClient: newYTSclient(timeout),
		tpbClient: newTPBclient(timeout),
	}
}

// FindMagnets tries to find magnet URLs for the given IMDb ID.
// It only returns 720p and 1080p videos.
// It caches results once they're found.
// It can return an empty slice and no error if no actual error occurred (for example if torrents where found but no >720p videos).
func (c Client) FindMagnets(imdbID string) ([]Result, error) {
	torrentSiteCount := 2
	resChan := make(chan []Result, torrentSiteCount)
	errChan := make(chan error, torrentSiteCount)

	// YTS
	go func() {
		log.Println("Started searching torrents on YTS...")
		results, err := c.ytsClient.check(imdbID)
		if err != nil {
			log.Println("Couldn't find torrents on YTS:", err)
			errChan <- err
		} else {
			log.Println("Found", len(results), "torrents on YTS")
			resChan <- results
		}
	}()

	// TPB
	go func() {
		log.Println("Started searching torrents on TPB...")
		results, err := c.tpbClient.check(imdbID, 2)
		if err != nil {
			log.Println("Couldn't find torrents on TPB:", err)
			errChan <- err
		} else {
			log.Println("Found", len(results), "torrents on TPB")
			resChan <- results
		}
	}()

	// TODO: Check other torrent sites

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

	// Return error (only) if all torrent sites returned actual errors (and not just empty results)
	if len(errs) == torrentSiteCount {
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
		log.Println("Couldn't find ANY torrents for IMDb ID", imdbID)
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
