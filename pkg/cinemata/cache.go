package cinemata

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"time"
)

type cacheEntry struct {
	Created time.Time
	Movie   movie
}

// newCacheEntry turns data into a single cacheEntry and returns the cacheEntry's gob-encoded bytes.
func newCacheEntry(ctx context.Context, movie movie) ([]byte, error) {
	entry := cacheEntry{
		Created: time.Now(),
		Movie:   movie,
	}
	writer := bytes.Buffer{}
	encoder := gob.NewEncoder(&writer)
	if err := encoder.Encode(entry); err != nil {
		return nil, fmt.Errorf("Couldn't encode cacheEntry: %v", err)
	}
	return writer.Bytes(), nil
}

// fromCacheEntry turns data via gob-decoding into a cacheEntry and returns its results and creation time.
func fromCacheEntry(ctx context.Context, data []byte) (movie, time.Time, error) {
	reader := bytes.NewReader(data)
	decoder := gob.NewDecoder(reader)
	var entry cacheEntry
	if err := decoder.Decode(&entry); err != nil {
		return movie{}, time.Time{}, fmt.Errorf("Couldn't decode cacheEntry: %v", err)
	}
	return entry.Movie, entry.Created, nil
}
