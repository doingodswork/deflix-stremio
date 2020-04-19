package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
)

const (
	bigBuckBunnyMagnet = `magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big+Buck+Bunny&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F&xs=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2Fbig-buck-bunny.torrent`
)

var (
	baseURL      = flag.String("baseURL", "https://api.real-debrid.com", "Base URL of RealDebrid")
	apiToken     = flag.String("apiToken", "", "RealDebrid API token")
	extraHeaders = flag.String("extraHeaders", "", "Additional headers to set, for example for a proxy. Format: \"X-Foo: bar\". Separated by newline characters (\"\\n\")")
)

func main() {
	ctx := context.Background()
	flag.Parse()

	// Precondition checks
	if *apiToken == "" {
		log.Fatal("apiToken CLI argument must not be empty")
	}
	// No precondition check for extraHeaders - `realdebrid.NewClient()` already does that

	// Parse extra headers
	var extraHeaderSlice []string
	if *extraHeaders != "" {
		headers := strings.Split(*extraHeaders, "\n")
		for _, header := range headers {
			header = strings.TrimSpace(header)
			if header != "" {
				extraHeaderSlice = append(extraHeaderSlice, header)
			}
		}
	}

	rdClientOpts := realdebrid.NewClientOpts(*baseURL, 5*time.Second, time.Duration(0), extraHeaderSlice)
	// Note: We can pass nil for the cache parameters here because we don't call a method that uses the caches.
	rdClient, err := realdebrid.NewClient(ctx, rdClientOpts, nil, nil)
	if err != nil {
		log.Fatalf("Couldn't create RD client: %v", err)
	}
	streamURL, err := rdClient.GetStreamURL(ctx, bigBuckBunnyMagnet, *apiToken, false)
	if err != nil {
		log.Fatalf("Couldn't get stream URL: %v", err)
	}
	fmt.Printf("Stream URL retrieved successfully: %v\n", streamURL)
}
