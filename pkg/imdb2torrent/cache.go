package imdb2torrent

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"time"
)

type cacheEntry struct {
	Created time.Time
	Results []Result
}

// NewCacheEntry turns data into a single cacheEntry and returns the cacheEntry's gob-encoded bytes.
func NewCacheEntry(data []Result) ([]byte, error) {
	entry := cacheEntry{
		Created: time.Now(),
		Results: data,
	}
	writer := bytes.Buffer{}
	encoder := gob.NewEncoder(&writer)
	if err := encoder.Encode(entry); err != nil {
		return nil, fmt.Errorf("Couldn't encode cacheEntry: %v", err)
	}
	log.Printf("New cacheEntry size: %vKB", len(writer.Bytes())/1024)
	if len(writer.Bytes()) > 64*1024 {
		log.Println("New cacheEntry is bigger than 64KB, which means it won't be stored in the cache when calling fastcache's Set() method. SetBig() (and GetBig()) must be used instead!")
	}
	return writer.Bytes(), nil
}

// FromCacheEntry turns data via gob-decoding into a cacheEntry and returns its results and creation time.
func FromCacheEntry(data []byte) ([]Result, time.Time, error) {
	reader := bytes.NewReader(data)
	decoder := gob.NewDecoder(reader)
	var entry cacheEntry
	if err := decoder.Decode(&entry); err != nil {
		return nil, time.Time{}, fmt.Errorf("Couldn't decode cacheEntry: %v", err)
	}
	return entry.Results, entry.Created, nil
}
