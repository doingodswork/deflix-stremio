package imdb2torrent

import (
	"log"
	"net/http"
	"time"
)

type Client struct {
	httpClient *http.Client
	cache      map[string][]Result
}

func NewClient(timeout time.Duration) Client {
	return Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: make(map[string][]Result),
	}
}

// FindMagnets tries to find magnet URLs for the given IMDb ID.
// It only returns 720p and 1080p videos.
func (c Client) FindMagnets(imdbID string) ([]Result, error) {
	// Check cache first
	if results, ok := c.cache[imdbID]; ok {
		return results, nil
	}

	results, err := c.checkYTS(imdbID)
	if err != nil {
		log.Println("No torrents found on YTS:", err)
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
