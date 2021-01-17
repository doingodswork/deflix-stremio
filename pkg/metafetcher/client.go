package metafetcher

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/deflix-tv/go-stremio"
	"github.com/deflix-tv/go-stremio/pkg/cinemeta"
	"github.com/deflix-tv/imdb2meta/pb"
	"github.com/deflix-tv/imdb2torrent"
)

var _ stremio.MetaFetcher = (*Client)(nil)
var _ imdb2torrent.MetaGetter = (*Client)(nil)

// Client is used to implement stremio.MetaFetcher.
type Client struct {
	imdb2metaClient pb.MetaFetcherClient
	cinemetaClient  *cinemeta.Client
	conn            *grpc.ClientConn
	logger          *zap.Logger
}

// NewClient creates a new metafetcher client.
// One of imdb2metaAddress and cinemetaClient can be empty/nil.
// If imdb2metaAddress is passed, an imdb2meta gRPC client is created and used.
// If both are passed, for GetMovie and GetTVShow calls the imdb2meta gRPC client is used first, and only if it fails the cinemetaClient is used.
// You should call Close() when finished.
func NewClient(imdb2metaAddress string, cinemetaClient *cinemeta.Client, logger *zap.Logger) (*Client, error) {
	if imdb2metaAddress == "" && cinemetaClient == nil {
		return nil, errors.New("one of the arguments must not be empty/nil")
	}

	var imdb2metaClient pb.MetaFetcherClient
	var conn *grpc.ClientConn
	if imdb2metaAddress != "" {
		// Set up a connection to the server.
		logger.Info("Connecting to imdb2meta gRPC server...", zap.String("address", imdb2metaAddress))
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err = grpc.DialContext(ctx, imdb2metaAddress, grpc.WithInsecure(), grpc.WithBlock())
		if err != nil {
			return nil, err
		}
		imdb2metaClient = pb.NewMetaFetcherClient(conn)
		logger.Info("Connected to imdb2meta gRPC server")
	}

	return &Client{
		imdb2metaClient: imdb2metaClient,
		cinemetaClient:  cinemetaClient,
		conn:            conn,
		logger:          logger,
	}, nil
}

// GetMovie implements stremio.MetaFetcher.
// Note that if the context has a timeout and it times out during the initial imdb2meta gRPC request,
// the Cinemeta HTTP request will fail immediately.
// TODO: Do both requests in parallel?
func (c *Client) GetMovie(ctx context.Context, imdbID string) (cinemeta.Meta, error) {
	if c.imdb2metaClient != nil {
		request := &pb.MetaRequest{
			Id: imdbID,
		}
		res, err := c.imdb2metaClient.Get(ctx, request)
		if err == nil {
			// No need to fill all data *for our purposes in deflix-stremio*
			return cinemeta.Meta{
				ID:          res.GetId(),
				Name:        res.GetPrimaryTitle(),
				ReleaseInfo: strconv.Itoa(int(res.GetStartYear())),
			}, nil
		}
		c.logger.Error("Couldn't get movie from imdb2meta gRPC server. Falling back to Cinemeta.", zap.Error(err), zap.String("imdbID", imdbID))
	}
	if c.cinemetaClient != nil {
		return c.cinemetaClient.GetMovie(ctx, imdbID)
	}
	return cinemeta.Meta{}, nil
}

// GetTVShow implements stremio.MetaFetcher.
// Note that if the context has a timeout and it times out during the initial imdb2meta gRPC request,
// the Cinemeta HTTP request will fail immediately.
// TODO: Do both requests in parallel?
func (c *Client) GetTVShow(ctx context.Context, imdbID string, season, episode int) (cinemeta.Meta, error) {
	// We only need to know the title of the TV show in general, so the match for the IMDb ID we get passed is fine.
	if c.imdb2metaClient != nil {
		request := &pb.MetaRequest{
			Id: imdbID,
		}
		res, err := c.imdb2metaClient.Get(ctx, request)
		if err == nil {
			// No need to fill all data *for our purposes in deflix-stremio*
			return cinemeta.Meta{
				ID:          res.GetId(),
				Name:        res.GetPrimaryTitle(),
				ReleaseInfo: strconv.Itoa(int(res.GetStartYear())),
			}, nil
		}
		c.logger.Error("Couldn't get TV show from imdb2meta gRPC server. Falling back to Cinemeta.", zap.Error(err), zap.String("imdbID", imdbID))
	}
	if c.cinemetaClient != nil {
		return c.cinemetaClient.GetTVShow(ctx, imdbID, season, episode)
	}
	return cinemeta.Meta{}, nil
}

// GetMovieSimple implements imdb2torrent.MetaGetter.
func (c *Client) GetMovieSimple(ctx context.Context, imdbID string) (imdb2torrent.Meta, error) {
	movieMeta, err := c.GetMovie(ctx, imdbID)
	if err != nil {
		return imdb2torrent.Meta{}, err
	}
	year, err := strconv.Atoi(movieMeta.ReleaseInfo)
	if err != nil {
		c.logger.Error("Couldn't convert movieMeta.ReleaseInfo to int", zap.Error(err), zap.String("releaseInfo", movieMeta.ReleaseInfo))
		return imdb2torrent.Meta{}, err
	}
	return imdb2torrent.Meta{
		Title: movieMeta.Name,
		Year:  year,
	}, nil
}

// GetTVShowSimple implements imdb2torrent.MetaGetter.
func (c *Client) GetTVShowSimple(ctx context.Context, imdbID string, season, episode int) (imdb2torrent.Meta, error) {
	showMeta, err := c.GetTVShow(ctx, imdbID, season, episode)
	if err != nil {
		return imdb2torrent.Meta{}, err
	}
	var year int
	if len(showMeta.ReleaseInfo) > 4 {
		showMeta.ReleaseInfo = showMeta.ReleaseInfo[:4]
	}
	year, err = strconv.Atoi(showMeta.ReleaseInfo)
	if err != nil {
		c.logger.Error("Couldn't convert showMeta.ReleaseInfo to int", zap.Error(err), zap.String("releaseInfo", showMeta.ReleaseInfo))
		return imdb2torrent.Meta{}, err
	}
	return imdb2torrent.Meta{
		Title: showMeta.Name,
		Year:  year,
	}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
