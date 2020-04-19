package imdb2torrent

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/net/publicsuffix"
)

func newSOCKS5httpClient(timeout time.Duration, socks5ProxyAddr string) (*http.Client, error) {
	dialer, err := proxy.SOCKS5("tcp", socks5ProxyAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create SOCKS5 dialer: %v", err)
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("Couldn't create cookie jar: %v", err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
		Jar:     jar,
		Timeout: timeout,
	}, nil
}
