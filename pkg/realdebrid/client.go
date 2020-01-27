package realdebrid

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	rdBaseURL = "https://api.real-debrid.com/rest/1.0"
)

type Client struct {
	httpClient *http.Client
}

func NewClient(timeout time.Duration) Client {
	return Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c Client) TestToken(apiToken string) error {
	log.Println("Testing token...")
	resBytes, err := c.get(rdBaseURL+"/user", apiToken)
	if err != nil {
		return fmt.Errorf("Couldn't fetch user info from real-debrid.com with the provided token: %v", err)
	}
	if !gjson.GetBytes(resBytes, "id").Exists() {
		return fmt.Errorf("Couldn't parse user info response from real-debrid.com")
	}
	log.Println("Token OK")
	return nil
}

func (c Client) CheckInstantAvailability(apiToken string, infoHashes ...string) []string {
	result := []string{}
	for _, infoHash := range infoHashes {
		resBytes, err := c.get(rdBaseURL+"/torrents/instantAvailability/"+infoHash, apiToken)
		if err != nil {
			log.Println("Couldn't check torrent's instant availability on real-debrid.com:", err)
		} else {
			// Note: our info_hash is uppercase, real-debrid.com returns a lowercase one
			rds := gjson.GetBytes(resBytes, strings.ToLower(infoHash)).Get("rd").Array()
			if len(rds) == 0 {
				log.Println("Torrent not instantly available on real-debrid.com")
			} else {
				// We don't care about the exact contents for now.
				// If something was found we can assume the instantly available file of the torrent is the streamable video.
				result = append(result, infoHash)
			}
		}
	}
	return result
}

func (c Client) GetStreamURL(magnetURL, apiToken string) (string, error) {
	log.Println("Adding torrent to RealDebrid...")
	data := url.Values{}
	data.Set("magnet", magnetURL)
	resBytes, err := c.post(rdBaseURL+"/torrents/addMagnet", apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't add torrent to RealDebrid: %v", err)
	}
	log.Println("Finished adding torrent to RealDebrid")
	rdTorrentURL := gjson.GetBytes(resBytes, "uri").String()

	// Check RealDebrid torrent info

	log.Println("Checking torrent info...")
	resBytes, err = c.get(rdTorrentURL, apiToken)
	if err != nil {
		return "", fmt.Errorf("Couldn't get torrent info from real-debrid.com: %v", err)
	}
	torrentID := gjson.GetBytes(resBytes, "id").String()
	fileResults := gjson.GetBytes(resBytes, "files").Array()
	fileID, err := selectFileID(fileResults)
	if err != nil {
		return "", fmt.Errorf("Couldn't find proper file in torrent: %v", err)
	}
	log.Println("Torrent info OK")

	// Add torrent to RealDebrid downloads

	log.Println("Adding torrent to RealDebrid downloads...")
	data = url.Values{}
	data.Set("files", fileID)
	_, err = c.post(rdBaseURL+"/torrents/selectFiles/"+torrentID, apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't add torrent to RealDebrid downloads: %v", err)
	}
	log.Println("Finished adding torrent to RealDebrid downloads")

	// Get torrent info (again)

	log.Println("Checking torrent status...")
	torrentStatus := ""
	waitForDownloadSeconds := 5
	waitedForDownloadSeconds := 0
	for torrentStatus != "downloaded" {
		resBytes, err = c.get(rdTorrentURL, apiToken)
		if err != nil {
			return "", fmt.Errorf("Couldn't get torrent info from real-debrid.com: %v", err)
		}
		torrentStatus = gjson.GetBytes(resBytes, "status").String()
		// Stop immediately if an error occurred.
		// Possible status: magnet_error, magnet_conversion, waiting_files_selection, queued, downloading, downloaded, error, virus, compressing, uploading, dead
		if torrentStatus == "magnet_error" ||
			torrentStatus == "error" ||
			torrentStatus == "virus" ||
			torrentStatus == "dead" {
			return "", fmt.Errorf("Bad torrent status: %v", torrentStatus)
		}
		// If status is before downloading (magnet_conversion, queued) or downloading, only wait 5 seconds
		// Note: This first condition also matches on waiting_files_selection, compressing and uploading, but these should never occur (we already selected a file and we're not uploading/compressing anything), but in case for some reason they match, well ok wait for 5 seconds as well.
		// Also matches future additional statuses that don't exist in the API yet. Well ok wait for 5 seconds as well.
		if torrentStatus != "downloading" && torrentStatus != "downloaded" {
			if waitedForDownloadSeconds < waitForDownloadSeconds {
				log.Printf("Torrent status: %v. Waiting for download for %v seconds...\n", torrentStatus, waitForDownloadSeconds-waitedForDownloadSeconds)
				waitedForDownloadSeconds++
			} else {
				log.Println("Torrent still "+torrentStatus+" after waiting", waitForDownloadSeconds, "seconds")
				return "", fmt.Errorf("Torrent still waiting for download (currently %v) on real-debrid.com after waiting for %v seconds", torrentStatus, waitForDownloadSeconds)
			}
		} else if torrentStatus == "downloading" {
			if waitedForDownloadSeconds < waitForDownloadSeconds {
				log.Println("Torrent downloading. Waiting for", waitForDownloadSeconds-waitedForDownloadSeconds, "seconds...")
				waitedForDownloadSeconds++
			} else {
				log.Println("Torrent still "+torrentStatus+" after waiting", waitForDownloadSeconds, "seconds")
				return "", fmt.Errorf("Torrent still %v on real-debrid.com after waiting for %v seconds", torrentStatus, waitForDownloadSeconds)
			}
		}
		time.Sleep(time.Second)
	}
	debridURL := gjson.GetBytes(resBytes, "links").Array()[0].String()
	log.Println("Torrent is downloaded")

	// Unrestrict link

	log.Println("Unrestricting link...")
	data = url.Values{}
	data.Set("link", debridURL)
	// if remote {
	// 	data.Set("remote", "1")
	// }
	resBytes, err = c.post(rdBaseURL+"/unrestrict/link", apiToken, data)
	if err != nil {
		return "", fmt.Errorf("Couldn't unrestrict link: %v", err)
	}
	streamURL := gjson.GetBytes(resBytes, "download").String()
	log.Println("Unrestricted link:", streamURL)

	return streamURL, nil
}

func (c Client) get(url, apiToken string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create GET request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't send GET request: %v", err)
	}
	defer res.Body.Close()

	// Check server response
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("Invalid token")
		} else if res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("Account locked")
		}
		return nil, fmt.Errorf("bad HTTP response status: %v (GET request to %v)", res.Status, url)
	}

	return ioutil.ReadAll(res.Body)
}

func (c Client) post(url, apiToken string, data url.Values) ([]byte, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("Couldn't create POST request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't send POST request: %v", err)
	}
	defer res.Body.Close()

	// Check server response.
	// Different RealDebrid API POST endpoints return different status codes.
	if res.StatusCode != http.StatusCreated &&
		res.StatusCode != http.StatusNoContent &&
		res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("Invalid token")
		} else if res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("Account locked")
		}
		return nil, fmt.Errorf("bad HTTP response status: %v (POST request to %v)", res.Status, url)
	}

	return ioutil.ReadAll(res.Body)
}

func selectFileID(fileResults []gjson.Result) (string, error) {
	// Precondition check
	if len(fileResults) == 0 {
		return "", fmt.Errorf("Empty slice of files")
	}

	var fileID int64 // ID inside JSON starts with 1
	var size int64
	for _, res := range fileResults {
		if res.Get("bytes").Int() > size {
			size = res.Get("bytes").Int()
			fileID = res.Get("id").Int()
		}
	}

	if fileID == 0 {
		return "", fmt.Errorf("No file ID found")
	}

	return strconv.FormatInt(fileID, 10), nil
}
