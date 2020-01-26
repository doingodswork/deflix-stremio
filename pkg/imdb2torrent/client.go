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

	results, err := c.checkYTS(imdbID)
	if err != nil {
		log.Println("No torrents found on YTS:", err)
	} else if len(results) == 0 {
		log.Println("No torrents found on YTS")
	}

	return results, err
}

type Result struct {
	Title string
	// For example "720p" or "720p (web)"
	Quality   string
	InfoHash  string
	MagnetURL string
}
