package realdebrid

// Torrent represents a torrent on RealDebrid.
// Based on the API docs:
//
//  {
//  	"id": "string",
//  	"filename": "string",
//  	"hash": "string", // SHA1 Hash of the torrent
//  	"bytes": int, // Size of selected files only
//  	"host": "string", // Host main domain
//  	"split": int, // Split size of links
//  	"progress": int, // Possible values: 0 to 100
//  	"status": "downloaded", // Current status of the torrent: magnet_error, magnet_conversion, waiting_files_selection, queued, downloading, downloaded, error, virus, compressing, //  uploading, dead
//  	"added": "string", // jsonDate
//  	"links": [
//  		"string" // Host URL
//  	],
//  	"ended": "string", // !! Only present when finished, jsonDate
//  	"speed": int, // !! Only present in "downloading", "compressing", "uploading" status
//  	"seeders": int // !! Only present in "downloading", "magnet_conversion" status
//  }
type Torrent struct {
	ID       string `json:"id,omitempty"`
	Filename string `json:"filename,omitempty"`
	// SHA1 Hash of the torrent
	Hash string `json:"hash,omitempty"`
	// Size of selected files only
	Bytes int `json:"bytes,omitempty"`
	// Host main domain
	Host string `json:"host,omitempty"`
	// Split size of links
	Split int `json:"split,omitempty"`
	// Possible values: 0 to 100
	Progress int `json:"progress,omitempty"`
	// Current status of the torrent: magnet_error, magnet_conversion, waiting_files_selection, queued, downloading, downloaded, error, virus, compressing, //  uploading, dead
	Status string `json:"status,omitempty"`
	// jsonDate
	Added string `json:"added,omitempty"`
	// Host URL
	Links []string `json:"links,omitempty"`
	// !! Only present when finished, jsonDate
	Ended string `json:"ended,omitempty"`
	// !! Only present in "downloading", "compressing", "uploading" status
	Speed int `json:"speed,omitempty"`
	// !! Only present in "downloading", "magnet_conversion" status
	Seeders int `json:"seeders,omitempty"`
}
