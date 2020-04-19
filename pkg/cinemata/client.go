package cinemata

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type movie struct {
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
	cache      *fastcache.Cache
}

func NewClient(ctx context.Context, opts ClientOptions, cache *fastcache.Cache) Client {
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
	if movieGob, ok := c.cache.HasGet(nil, []byte(imdbID)); ok {
		movie, created, err := fromCacheEntry(ctx, movieGob)
		if err != nil {
			logger.WithError(err).Error("Couldn't decode movie")
		} else if time.Since(created) < (24 * time.Hour * 30) {
			logger.Debug("Hit cache for movie, returning result")
			return movie.Name, movie.Year, nil
		} else {
			expiredSince := time.Since(created.Add(24 * time.Hour * 30))
			logger.WithField("expiredSince", expiredSince).Debug("Hit cache for movie, but entry is expired")
		}
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
	movie := movie{
		Name: movieName,
		Year: movieYearInt,
	}
	if movieGob, err := newCacheEntry(ctx, movie); err != nil {
		logger.WithError(err).WithField("cache", "movie").Error("Couldn't create cache entry for movie")
	} else {
		c.cache.Set([]byte(imdbID), movieGob)
	}

	return movieName, movieYearInt, nil
}
