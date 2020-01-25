package stremio

// Manifest describes the capabilities of the addon.
// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/manifest.md
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`

	// One of the following is required
	// Note: Can only have one in code because of how Go (de-)serialization works
	//Resources     []string       `json:"resources,omitempty"`
	ResourceItems []ResourceItem `json:"resources,omitempty"`

	Types    []string      `json:"types"`
	Catalogs []CatalogItem `json:"catalogs"`

	// Optional
	IDprefixes    []string      `json:"idPrefixes,omitempty"`
	Background    string        `json:"background,omitempty"` // URL
	Logo          string        `json:"logo,omitempty"`       // URL
	ContactEmail  string        `json:"contactEmail,omitempty"`
	BehaviorHints BehaviorHints `json:"behaviorHints,omitempty"`
}

type ResourceItem struct {
	Name  string   `json:"name"`
	Types []string `json:"types"`

	// Optional
	IDprefixes []string `json:"idPrefixes,omitempty"`
}

type BehaviorHints struct {
	// Note: Must include `omitempty`, otherwise it will be included if this struct is used in another one, even if the field of the containing struct is marked as `omitempty`
	Adult bool `json:"adult,omitempty"`
}

// CatalogItem represents an item in the catalog
type CatalogItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`

	// Optional
	Extra []ExtraItem `json:"extra,omitempty"`
}

type ExtraItem struct {
	Name string `json:"name"`

	// Optional
	IsRequired   bool     `json:"isRequired,omitempty"`
	Options      []string `json:"options,omitempty"`
	OptionsLimit int      `json:"optionsLimit,omitempty"`
}

// This represents a meta item and is meant to be used within catalog responses.
// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/meta.md
//
// Note: According to a Stremio developer the catalog response can include all fields from the MetaItem as well though!
type MetaPreviewItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Poster string `json:"poster"` // URL

	// Optional
	PosterShape string `json:"posterShape,omitempty"`
	Background  string `json:"background,omitempty"` // URL
	Logo        string `json:"logo,omitempty"`       // URL
	Description string `json:"description,omitempty"`
}

// This represents a meta item and is meant to be used when info for a specific item was requested.
// It *can* be used for catalog responses (as long as a Poster URL is given), but is meant for requests for specific items.
// Catalog responses contain MetaPreviewItem objects.
// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/meta.md
type MetaItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`

	// Optional
	Poster      string         `json:"poster,omitempty"` // URL
	PosterShape string         `json:"posterShape,omitempty"`
	Background  string         `json:"background,omitempty"` // URL
	Logo        string         `json:"logo,omitempty"`       // URL
	Description string         `json:"description,omitempty"`
	IMDBrating  string         `json:"imdbRating,omitempty"`
	Released    string         `json:"released,omitempty"` // Must be ISO 8601, e.g. "2010-12-06T05:00:00.000Z"
	Links       []MetaLinkItem `json:"links,omitempty"`
	Videos      []VideoItem    `json:"videos,omitempty"`
	Runtime     string         `json:"runtime,omitempty"`
	Language    string         `json:"language,omitempty"`
	Country     string         `json:"country,omitempty"`
	Awards      string         `json:"awards,omitempty"`
	Website     string         `json:"website,omitempty"` // URL

	// Note: Omitted "genres", "director" and "cast" because they will be deprecated soon.
	// Use links instead.

	// TODO: behaviorHints
}

type MetaLinkItem struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	URL      string `json:"url"` //  // URL. Can be "Meta Links" (see https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/meta.links.md)
}

type VideoItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Released string `json:"released"` // Must be ISO 8601, e.g. "2010-12-06T05:00:00.000Z"

	// Optional
	Thumbnail string       `json:"thumbnail,omitempty"` // URL
	Streams   []StreamItem `json:"streams,omitempty"`
	Available bool         `json:"available,omitempty"`
	Episode   string       `json:"episode,omitempty"`
	Season    string       `json:"season,omitempty"`
	Trailer   string       `json:"trailer,omitempty"` // Youtube ID
	Overview  string       `json:"overview,omitempty"`
}

// StreamItem represents a stream for a MetaItem.
// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/stream.md
type StreamItem struct {
	// One of the following is required
	URL         string `json:"url,omitempty"` // URL
	YoutubeID   string `json:"ytId,omitempty"`
	InfoHash    string `json:"infoHash,omitempty"`
	ExternalURL string `json:"externalUrl,omitempty"` // URL

	// Optional
	Title     string `json:"title,omitempty"`
	FileIndex uint8  `json:"fileIdx,omitempty"` // Only when using InfoHash

	// TODO: subtitles
	// TODO: behaviorHints
}
