Deflix Stremio addon
====================

[Deflix](https://www.deflix.tv) addon for [Stremio](https://stremio.com)

Finds movies from many different sources and automatically turns them into cached HTTP streams with a debrid service like [RealDebrid](https://real-debrid.com), [AllDebrid](https://alldebrid.com) or [Premiumize](https://www.premiumize.me), for high speed 4k streaming and **no P2P uploading**.

Contents
--------

1. [Features](#features)
2. [Install](#install)
3. [Run locally](#run-locally)
   1. [Configuration](#configuration)
   2. [Warning](#warning)
4. [Disclaimer](#disclaimer)

Features
--------

- Supports several debrid services
  - [x] [RealDebrid](https://real-debrid.com)
  - [x] [AllDebrid](https://alldebrid.com)
  - [x] [Premiumize](https://www.premiumize.me)
  - [ ] Others can be added, please let me know which one you want to see next
- Finds movies from many different sources
  - [x] YTS
  - [x] The Pirate Bay
  - [x] 1337x
  - [x] RARBG
  - [x] ibit
  - [ ] Others like RapidMoviez and Scene-RLS are planned
- Groups streams by quality so you don't have to choose between dozens of results
  - 720p
  - 1080p
  - 1080p 10bit
  - 2160p
  - 2160p 10bit
- Configurable via the ⚙ button in Stremio

Other *upcoming* features: Support for TV shows, more sources, grouping by bitrate, more custom options (language filter, show *all single torrents* instead of grouped by quality) and more

Install
-------

This addon is a remote addon, so it's an HTTP web service and Stremio just sends HTTP requests to it. You dont't need to run any untrusted code on your machine.

Here's the official Deflix website, that guides you through the installation: <https://www.deflix.tv/stremio>

But it's just a few simple steps, so you can do it without the website as well:

1. Get your debrid service API key
   - For RealDebrid: <https://real-debrid.com/apitoken>
   - For AllDebrid: <https://alldebrid.com/apikeys/>
   - For Premiumize: <https://www.premiumize.me/account>
2. Create a JSON object with the data, like this:
   - For RealDebrid: `{"rdToken":"YOUR-API-TOKEN"}` or with an additional `"remote":true` (see below)
   - For AllDebrid: `{"adKey":"YOUR-API-KEY"}`
   - For Premiumize: `{"pmKey":"YOUR-API-KEY"}`
3. Encode the JSON as Base64URL, for example on <https://base64.guru/standards/base64url/encode> or <https://simplycalc.com/base64url-encode.php>
   - This becomes something like `eyJyZFRva2VuIjoiWU9VUi1BUEktVE9LRU4ifQ` or with a padding suffix (`==` in this case)  
     > Note: It has to be Base64URL, not Base64. For more info you can read [RFC 4648](https://tools.ietf.org/html/rfc4648#section-5).
4. Enter the addon URL in the search box of the addons section of Stremio, like this:
   - `https://stremio.deflix.tv/eyJyZFRva2VuIjoiWU9VUi1BUEktVE9LRU4ifQ/manifest.json`  
     > ⚠️ Replace `eyJyZFRva2VuIjoiWU9VUi1BUEktVE9LRU4ifQ` by your own encoded user data!

That's it!

Regarding `"remote":true` for RealDebrid: This will lead to your "remote traffic" being used, which allows you to share your RealDebrid account (and API token) with friends. (⚠️ When sharing your account and *not* using remote traffic, you might get suspended - see RealDebrid's [terms](https://real-debrid.com/terms) and [faq](https://real-debrid.com/faq)!)

Run locally
-----------

Alternatively you can also run the addon locally and use that in Stremio. The addon is written in Go and compiles to a single executable file without dependencies, so it's really easy to run on your machine.

You can use one of the precompiled binaries from GitHub:

1. Download the binary for your OS from <https://github.com/doingodswork/deflix-stremio/releases>
2. Simply run the executable binary
3. To stop the program press `Ctrl-C` (or `⌃-C` on macOS)

Or use Docker:

1. Update the image: `docker pull doingodswork/deflix-stremio`
2. Start the container: `docker run --name deflix-stremio -p 8080:8080 doingodswork/deflix-stremio`
   - > Note: `Ctrl-C` only detaches from the container. It doesn't stop it.
   - When detached, you can attach again with `docker attach deflix-stremio`
3. To stop the container: `docker stop deflix-stremio`
4. To start the (still existing) container again: `docker start deflix-stremio`

And then you can visit the addon configuration page in your browser to install the locally running addon in Stremio: <http://localhost:8080/configure>  
Or like mentioned in the [Install section](#install) you can generate the encoded user data manually and paste the URL into the search box of the addons section of Stremio.

### Configuration

The following options can be configured via either command line argument or environment variable:

```text
Usage of deflix-stremio:
  -baseURL string
        Base URL of this service. It's used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid, AllDebrid and Premiumize. If you enable OAuth2 handling this will also be used for the redirects and to determine whether the state cookie is a secure one or not. (default "http://localhost:8080")
  -baseURL1337x string
        Base URL for 1337x (default "https://1337x.to")
  -baseURLad string
        Base URL for AllDebrid (default "https://api.alldebrid.com")
  -baseURLibit string
        Base URL for ibit (default "https://ibit.am")
  -baseURLpm string
        Base URL for Premiumize (default "https://www.premiumize.me/api")
  -baseURLrarbg string
        Base URL for RARBG (default "https://torrentapi.org")
  -baseURLrd string
        Base URL for RealDebrid (default "https://api.real-debrid.com")
  -baseURLtpb string
        Base URL for the TPB API (default "https://apibay.org")
  -baseURLyts string
        Base URL for YTS (default "https://yts.mx")
  -bindAddr string
        Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces. (default "localhost")
  -cacheAgeXD duration
        Max age of cache entries for instant availability responses from RealDebrid, AllDebrid and Premiumize. The format must be acceptable by Go's 'time.ParseDuration()', for example "24h". (default 24h0m0s)
  -cachePath string
        Path for loading persisted caches on startup and persisting the current cache in regular intervals. An empty value will lead to 'os.UserCacheDir()+"/deflix-stremio/cache"'.
  -envPrefix string
        Prefix for environment variables
  -extraHeadersXD string
        Additional HTTP request headers to set for requests to RealDebrid, AllDebrid and Premiumize, in a format like "X-Foo: bar", separated by newline characters ("\n")
  -imdb2metaAddr string
        Address of the imdb2meta gRPC server. Won't be used if empty.
  -logFoundTorrents
        Set to true to log each single torrent that was found by one of the torrent site clients (with DEBUG level)
  -logLevel string
        Log level to show only logs with the given and more severe levels. Can be "debug", "info", "warn", "error". (default "debug")
  -maxAgeTorrents duration
        Max age of cache entries for torrents found per IMDb ID. The format must be acceptable by Go's 'time.ParseDuration()', for example "24h". Default is 7 days. (default 168h0m0s)
  -oauth2authURLpm string
        URL of the OAuth2 authorization endpoint of Premiumize (default "https://www.premiumize.me/authorize")
  -oauth2authURLrd string
        URL of the OAuth2 authorization endpoint of RealDebrid (default "https://api.real-debrid.com/oauth/v2/auth")
  -oauth2clientIDpm string
        Client ID for deflix-stremio on Premiumize
  -oauth2clientIDrd string
        Client ID for deflix-stremio on RealDebrid
  -oauth2clientSecretPM string
        Client secret for deflix-stremio on Premiumize
  -oauth2clientSecretRD string
        Client secret for deflix-stremio on RealDebrid
  -oauth2encryptionKey string
        OAuth2 data encryption key
  -oauth2tokenURLpm string
        URL of the OAuth2 token endpoint of Premiumize (default "https://www.premiumize.me/token")
  -oauth2tokenURLrd string
        URL of the OAuth2 token endpoint of RealDebrid (default "https://api.real-debrid.com/oauth/v2/token")
  -port int
        Port to listen on (default 8080)
  -redisAddr string
        Redis host and port, for example "localhost:6379". It's used for the redirect and stream cache. Keep empty to use in-memory go-cache.
  -redisCreds string
        Credentials for Redis. Password for Redis version 5 and older, username and password for Redis version 6 and newer. Use the colon character (":") for separating username and password. This implies you can't use a colon in the password when using Redis version 5 or older.
  -rootURL string
        Redirect target for the root (default "https://www.deflix.tv")
  -socksProxyAddrTPB string
        SOCKS5 proxy address for accessing TPB, required for accessing TPB via the TOR network (where "127.0.0.1:9050" would be typical value)
  -storagePath string
        Path for storing the data of the persistent DB which stores torrent results. An empty value will lead to 'os.UserCacheDir()+"/deflix-stremio/badger"'.
  -useOAUTH2
        Flag for indicating whether to use OAuth2 for Premiumize authorization. This leads to a different configuration webpage that doesn't require API keys. It requires a client ID to be configured.
  -webConfigurePath string
        Path to the directory with web files for the '/configure' endpoint. If empty, files compiled into the binary will be used
```

If you want to configure deflix-stremio via environment variables, you can use the according environment variable keys, like this: `baseURL1337x` -> `BASE_URL_1337X`. If you want to use an environment variable prefix you have to set it with the command line argument (for example `-envPrefix DEFLIX` and then the environment variable for the previous example would be `DEFLIX_BASE_URL_1337X`.

### Warning

If you *run* this web service on your local laptop or server, i.e. if you *self-host* this, you should know the following:

Deflix doesn't download or upload any torrents, but it *does* send HTTP requests to YTS, The Pirate Bay, 1337x, RARBG and ibit, which *might* be illegal in some countries. Streaming movies from RealDebrid, AllDebrid or Premiumize *might* also be illegal in some countries.

> To encrypt your traffic so that your ISP can't see where those HTTP requests are sent and to not expose your real IP address to RealDebrid, AllDebrid or Premiumize you can use a VPN.

Disclaimer
----------

Deflix

- doesn't *host* any media files or torrents
- doesn't *provide link lists* to media files or torrents
- isn't a *torrent indexer*
- doesn't *facilitate the sharing* of any media files or torrents
