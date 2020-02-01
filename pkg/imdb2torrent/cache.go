package imdb2torrent

type cache struct {
	m map[string][]Result
}

func newCache() *cache {
	return &cache{
		m: map[string][]Result{},
	}
}

func (c cache) set(imdbID string, magnets []Result) {
	c.m[imdbID] = magnets
}

func (c cache) get(imdbID string) ([]Result, bool) {
	res, ok := c.m[imdbID]
	return res, ok
}
