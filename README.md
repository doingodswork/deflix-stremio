Deflix Stremio addon
====================

[Deflix](https://deflix.tv) addon for [Stremio](https://stremio.com)

Looks up your selected movie on YTS, The Pirate Bay, 1337x and ibit and automatically turns your selected torrent into a debrid/cached stream, for high speed and **no P2P uploading**.

Currently supported providers:

- [x] <https://real-debrid.com>

> More providers will be supported in the future!

Run
---

The addon is a remote addon, so it's an HTTP web service. It's written in Go.

You can use one of the precompiled binaries from GitHub:

1. Download the binary for your OS from <https://github.com/doingodswork/deflix-stremio/releases>
2. Simply run the executable binary
3. To stop the program press `Ctrl-C` (or `⌃-C` on macOS)

Or use Docker:

1. `docker pull doingodswork/deflix-stremio`
2. `docker run --name deflix-stremio -p 8080:8080 doingodswork/deflix-stremio`
3. To stop the container: `docker stop deflix-stremio`

The following options can be configured via either command line argument or environment variable:

```text
Usage of deflix-stremio:
  -baseURL1337x string
        Base URL for 1337x (default "https://1337x.to")
  -baseURLibit string
        Base URL for ibit (default "https://ibit.am")
  -baseURLtpb string
        Base URL for TPB (default "https://thepiratebay.org")
  -baseURLyts string
        Base URL for YTS (default "https://yts.mx")
  -bindAddr string
        Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces. (default "localhost")
  -cacheMaxBytes int
        Max number of bytes to be used for the in-memory cache. Default (and minimum!) is 128 MB. (default 134217728)
  -cachePath os.UserCacheDir()+"/deflix-stremio/"
        Path for loading a persisted cache on startup and persisting the current cache in regular intervals. An empty value will lead to os.UserCacheDir()+"/deflix-stremio/"
  -envPrefix string
        Prefix for environment variables
  -logLevel string
        Log level to show only logs with the given and more severe levels. Can be "trace", "debug", "info", "warn", "error", "fatal", "panic" (default "debug")
  -port int
        Port to listen on (default 8080)
  -rootURL string
        Redirect target for the root (default "https://www.deflix.tv")
  -streamURLaddr string
        Address to be used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid (default "http://localhost:8080")
```

If you want to configure deflix-stremio via environment variables, you can use the according environment variable keys, like this: `baseURL1337x` -> `BASE_URL_1337X`. If you want to use an environment variable prefix you have to set it with the command line argument (for example `-envPrefix DEFLIX` and then the environment variable for the previous example would be `DEFLIX_BASE_URL_1337X`.

Use
---

After you started the web service with either the binary or Docker, it's running on `http://localhost:8080`.

Then:

1. Get your RealDebrid API token from <https://real-debrid.com/apitoken>
2. Enter the addon URL in the search box of the addons section of Stremio, like this:
   - `http://localhost:8080/YOUR_API_TOKEN/manifest.json`  
     > (replace `YOUR_API_TOKEN` by your actual API token!)

That's it!

Optionally you can also add `-remote` to your token, which will lead to your "remote traffic" being used, which allows you to share your RealDebrid account (and API token) with friends. (⚠️When sharing your account and *not* using remote traffic, you might get suspended - see RealDebrid's [terms](https://real-debrid.com/terms) and [faq](https://real-debrid.com/faq)!)

Warning
-------

Deflix doesn't download or upload any torrents, but it *does* send HTTP requests to YTS, The Pirate Bay and 1337x, which *might* be illegal in some countries. Streaming movies from RealDebrid *might* also be illegal in some countries.

> To encrypt your traffic so that your ISP can't see where those HTTP requests are sent and to not expose your real IP address to RealDebrid you can use a VPN.

Disclaimer
----------

Deflix

- doesn't *host* any media files or torrents
- doesn't *provide link lists* to media files or torrents
- isn't a *torrent indexer*
- doesn't *facilitate the sharing* of any media files or torrents
