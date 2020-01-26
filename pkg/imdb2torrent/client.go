package imdb2torrent

import (
	"log"
	"net/http"
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

	combinedResults := []Result{}

	// YTS
	results, err := c.checkYTS(imdbID)
	if err != nil {
		log.Println("Couldn't find torrents on YTS:", err)
	} else if len(results) == 0 {
		log.Println("No torrents found on YTS")
	} else {
		combinedResults = append(combinedResults, results...)
	}

	// TPB
	results, err = c.checkTPB(imdbID)
	if err != nil {
		log.Println("Couldn't find torrents on TPB:", err)
	} else if len(results) == 0 {
		log.Println("No torrents found on TPB")
	} else {
		combinedResults = append(combinedResults, results...)
	}

	// TODO: Check other torrent sites

	// Remove duplicates
	noDupResults := []Result{}
	infoHashes := map[string]struct{}{}
	for _, result := range combinedResults {
		if _, ok := infoHashes[result.InfoHash]; !ok {
			noDupResults = append(noDupResults, result)
			infoHashes[result.InfoHash] = struct{}{}
		}
	}

	if len(noDupResults) <= 0 {
		log.Println("Couldn't find ANY torrents for IMDb ID", imdbID)
	} else {
		c.cache.set(imdbID, noDupResults)
	}

	return noDupResults, err
}

type Result struct {
	Title string
	// For example "720p" or "720p (web)"
	Quality   string
	InfoHash  string
	MagnetURL string
}
