package realdebrid

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"time"
)

// newCacheEntry turns the current time into bytes via gob encoding.
func newCacheEntry() ([]byte, error) {
	writer := bytes.Buffer{}
	encoder := gob.NewEncoder(&writer)
	if err := encoder.Encode(time.Now()); err != nil {
		return nil, fmt.Errorf("Couldn't encode cacheEntry: %v", err)
	}
	return writer.Bytes(), nil
}

// fromCacheEntry turns gob-encoded bytes into a time object.
func fromCacheEntry(data []byte) (time.Time, error) {
	reader := bytes.NewReader(data)
	decoder := gob.NewDecoder(reader)
	var entry time.Time
	if err := decoder.Decode(&entry); err != nil {
		return time.Time{}, fmt.Errorf("Couldn't decode cacheEntry: %v", err)
	}
	return entry, nil
}
