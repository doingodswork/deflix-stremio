Deflix Stremio addon
====================

[Deflix](https://www.deflix.tv) addon for [Stremio](https://stremio.com)

Finds movies on YTS, The Pirate Bay, 1337x and ibit and automatically turns your selected torrent into a cached HTTP stream from a debrid provider like RealDebrid, for high speed 4k streaming and **no P2P uploading**.

Currently supported providers:

- [x] <https://real-debrid.com>

> More providers will be supported in the future!

Other upcoming features: Support for TV shows, more custom options (e.g. show *all single torrents* instead of grouped by quality) and more

Contents
--------

1. [Install](#install)
2. [Run locally](#run-locally)
   1. [Configuration](#configuration)
   2. [Warning](#warning)
3. [Tools](#tools)
   1. [rd-tester](#rd-tester)
   2. [rd-proxy](#rd-proxy)
4. [Disclaimer](#disclaimer)

Install
-------

This addon is a remote addon, so it's an HTTP web service and Stremio just sends HTTP requests to it. You dont't need to run any untrusted code on your machine.

Here's the official Deflix website, that guides you through the installation: <https://www.deflix.tv/stremio>

But it's just a few simple steps, so you can do it without the website as well:

1. Get your RealDebrid API token from <https://real-debrid.com/apitoken>
2. Enter the addon URL in the search box of the addons section of Stremio, like this:
   - `https://stremio.deflix.tv/YOUR-API-TOKEN/manifest.json`  
     > (replace `YOUR-API-TOKEN` by your actual API token!)

That's it!

Optionally you can also add `-remote` to your token, which will lead to your "remote traffic" being used, which allows you to share your RealDebrid account (and API token) with friends. (⚠️When sharing your account and *not* using remote traffic, you might get suspended - see RealDebrid's [terms](https://real-debrid.com/terms) and [faq](https://real-debrid.com/faq)!)

Run locally
-----------

Alternatively you can also run the addon locally and use that in Stremio. The addon is written in Go and compiles to a single executable file without dependencies, so it's really easy to run on your machine.

You can use one of the precompiled binaries from GitHub:

1. Download the binary for your OS from <https://github.com/doingodswork/deflix-stremio/releases>
2. Simply run the executable binary
3. To stop the program press `Ctrl-C` (or `⌃-C` on macOS)

Or use Docker:

1. `docker pull doingodswork/deflix-stremio`
2. `docker run --name deflix-stremio -p 8080:8080 doingodswork/deflix-stremio`
3. To stop the container: `docker stop deflix-stremio`

Then similar to installing the publicly hosted addon you enter the URL in the search box of the addon section of Stremio. But as URL you use `http://localhost:8080/YOUR-API-TOKEN/manifest.json`.

### Configuration

The following options can be configured via either command line argument or environment variable:

```text
Usage of deflix-stremio:
  -baseURL1337x string
        Base URL for 1337x (default "https://1337x.to")
  -baseURLibit string
        Base URL for ibit (default "https://ibit.am")
  -baseURLrd string
        Base URL for RealDebrid (default "https://api.real-debrid.com")
  -baseURLtpb string
        Base URL for the TPB API (default "https://apibay.org")
  -baseURLyts string
        Base URL for YTS (default "https://yts.mx")
  -bindAddr string
        Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces. (default "localhost")
  -cacheAgeRD duration
        Max age of cache entries for instant availability responses from RealDebrid. The format must be acceptable by Go's 'time.ParseDuration()', for example "24h". (default 24h0m0s)
  -cacheAgeTorrents duration
        Max age of cache entries for torrents found per IMDb ID. The format must be acceptable by Go's 'time.ParseDuration()', for example "24h". (default 24h0m0s)
  -cacheMaxMB int
        Max number of megabytes to be used for the in-memory torrent cache. Default (and minimum!) is 32 MB. (default 32)
  -cachePath string
        Path for loading a persisted cache on startup and persisting the current cache in regular intervals. An empty value will lead to 'os.UserCacheDir()+"/deflix-stremio/"'.
  -envPrefix string
        Prefix for environment variables
  -extraHeadersRD string
        Additional HTTP request headers to set for requests to RealDebrid, in a format like "X-Foo: bar", separated by newline characters ("\n")
  -logLevel string
        Log level to show only logs with the given and more severe levels. Can be "trace", "debug", "info", "warn", "error", "fatal", "panic". (default "debug")
  -port int
        Port to listen on (default 8080)
  -rootURL string
        Redirect target for the root (default "https://www.deflix.tv")
  -socksProxyAddrTPB string
        SOCKS5 proxy address for accessing TPB, required for accessing TPB via the TOR network (where "127.0.0.1:9050" would be typical value)
  -streamURLaddr string
        Address to be used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid (default "http://localhost:8080")
```

If you want to configure deflix-stremio via environment variables, you can use the according environment variable keys, like this: `baseURL1337x` -> `BASE_URL_1337X`. If you want to use an environment variable prefix you have to set it with the command line argument (for example `-envPrefix DEFLIX` and then the environment variable for the previous example would be `DEFLIX_BASE_URL_1337X`.

### Warning

If you *run* this web service on your local laptop or server, i.e. if you *self-host* this, you should know the following:

Deflix doesn't download or upload any torrents, but it *does* send HTTP requests to YTS, The Pirate Bay, 1337x and ibit, which *might* be illegal in some countries. Streaming movies from RealDebrid *might* also be illegal in some countries.

> To encrypt your traffic so that your ISP can't see where those HTTP requests are sent and to not expose your real IP address to RealDebrid you can use a VPN.

Tools
-----

This repository also contains some useful tools for running deflix-stremio in production:

### rd-tester

`rd-tester` is a command line program that tests if the RealDebrid API can be used from the machine where the program is executed. It uses the magnet URL of "Big Buck Bunny" to do so and requires a RealDebrid API token.

```text
Usage of rd-tester:
  -apiToken string
        RealDebrid API token
  -baseURL string
        Base URL of RealDebrid (default "https://api.real-debrid.com")
  -extraHeaders string
        Additional headers to set, for example for a proxy. Format: "X-Foo: bar". Separated by newline characters ("\n")
```

### rd-proxy

`rd-proxy` is a *reverse* proxy for proxying requests to RealDebrid. This can be necessary when RealDebrid blocks the IP of the server where `deflix-stremio` runs on, because you can then use a bunch of cheap, low-powered servers with fresh IPs for proxying, without having to run `deflix-stremio` on them.

```text
  -apiKeyHeader string
        Header key for the API key, e.g. "X-Proxy-Apikey"
  -apiKeys string
        List of comma separated API keys that the reverse proxy allows
  -bindAddr string
        Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces. (default "localhost")
  -logRequest
        Log the full request object
  -port int
        Port to listen on (default 8080)
  -targetURL string
        Reverse proxy target URL (default "https://api.real-debrid.com")
```

Disclaimer
----------

Deflix

- doesn't *host* any media files or torrents
- doesn't *provide link lists* to media files or torrents
- isn't a *torrent indexer*
- doesn't *facilitate the sharing* of any media files or torrents
