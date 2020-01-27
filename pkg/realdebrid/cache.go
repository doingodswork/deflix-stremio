package realdebrid

type cache struct {
	m map[string]struct{}
}

func newCache() *cache {
	return &cache{
		m: map[string]struct{}{},
	}
}

func (c cache) setExists(k string) {
	c.m[k] = struct{}{}
}

func (c cache) exists(k string) bool {
	_, ok := c.m[k]
	return ok
}
