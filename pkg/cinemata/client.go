package cinemata

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type Movie struct {
	Name string
	Year int
}

type ClientOptions struct {
	BaseURL string
	Timeout time.Duration
}

func NewClientOpts(baseURL string, timeout time.Duration) ClientOptions {
	return ClientOptions{
		BaseURL: baseURL,
		Timeout: timeout,
	}
}

var DefaultClientOpts = ClientOptions{
	BaseURL: "https://v3-cinemeta.strem.io",
	Timeout: 5 * time.Second,
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	cache      Cache
}

func NewClient(ctx context.Context, opts ClientOptions, cache Cache) Client {
	return Client{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
		},
		cache: cache,
	}
}

func (c Client) GetMovieNameYear(ctx context.Context, imdbID string) (string, int, error) {
	logger := log.WithContext(ctx).WithField("imdbID", imdbID)

	// Check cache first
	movie, created, found, err := c.cache.Get(imdbID)
	if err != nil {
		logger.WithError(err).Error("Couldn't decode movie")
	} else if !found {
		logger.Debug("Movie not found in cache")
	} else if time.Since(created) > (24 * time.Hour * 30) { // 30 days
		expiredSince := time.Since(created.Add(24 * time.Hour * 30))
		logger.WithField("expiredSince", expiredSince).Debug("Hit cache for movie, but item is expired")
	} else {
		logger.Debug("Hit cache for movie, returning result")
		return movie.Name, movie.Year, nil
	}

	reqUrl := c.baseURL + "/meta/movie/" + imdbID + ".json"

	res, err := c.httpClient.Get(reqUrl)
	if err != nil {
		return "", 0, fmt.Errorf("Couldn't GET %v: %v", reqUrl, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("Bad GET response: %v", res.StatusCode)
	}
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", 0, fmt.Errorf("Couldn't read response body: %v", err)
	}
	movieName := gjson.GetBytes(resBody, "meta.name").String()
	if movieName == "" {
		return "", 0, fmt.Errorf("Couldn't find movie name in Cinemata response")
	}
	movieYear := gjson.GetBytes(resBody, "meta.year").String()
	var movieYearInt int
	if movieYear != "" {
		movieYearInt, err = strconv.Atoi(movieYear)
		if err != nil {
			logger.WithField("year", movieYear).Warn("Couldn't convert string to int")
		}
	}

	// Fill cache
	movie = Movie{
		Name: movieName,
		Year: movieYearInt,
	}
	if err = c.cache.Set(imdbID, movie); err != nil {
		logger.WithError(err).WithField("cache", "movie").Error("Couldn't cache movie")
	}

	return movieName, movieYearInt, nil
}
