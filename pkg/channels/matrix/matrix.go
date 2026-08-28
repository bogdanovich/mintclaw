package matrix

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	_ "modernc.org/sqlite"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const (
	sqliteDriver = "sqlite"
	dbName       = "store.db"

	typingRefreshInterval      = 20 * time.Second
	typingServerTTL            = 30 * time.Second
	roomKindCacheTTL           = 5 * time.Minute
	roomKindCacheCleanupPeriod = 1 * time.Minute
	roomKindCacheMaxEntries    = 2048
)

type MatrixChannel struct {
	*channels.BaseChannel
	bc *config.Channel

	client *mautrix.Client
	config *config.MatrixSettings
	syncer *mautrix.DefaultSyncer

	ctx       context.Context
	cancel    context.CancelFunc
	startTime time.Time

	typingMu       sync.Mutex
	typingSessions map[string]*typingSession // roomID -> session

	roomKindCache     *roomKindCache
	localpartMentionR *regexp.Regexp

	cryptoHelper *cryptohelper.CryptoHelper
	cryptoDbPath string
}

func NewMatrixChannel(
	bc *config.Channel,
	cfg *config.MatrixSettings,
	messageBus *bus.MessageBus,
	cryptoDatabasePath string,
) (*MatrixChannel, error) {
	homeserver := strings.TrimSpace(cfg.Homeserver)
	userID := strings.TrimSpace(cfg.UserID)
	accessToken := strings.TrimSpace(cfg.AccessToken.String())
	if homeserver == "" {
		return nil, fmt.Errorf("matrix homeserver is required")
	}
	if userID == "" {
		return nil, fmt.Errorf("matrix user_id is required")
	}
	if accessToken == "" {
		return nil, fmt.Errorf("matrix access_token is required")
	}

	client, err := mautrix.NewClient(homeserver, id.UserID(userID), accessToken)
	if err != nil {
		return nil, fmt.Errorf("create matrix client: %w", err)
	}
	if cfg.DeviceID != "" {
		client.DeviceID = id.DeviceID(cfg.DeviceID)
	}

	syncer, ok := client.Syncer.(*mautrix.DefaultSyncer)
	if !ok {
		return nil, fmt.Errorf("matrix syncer is not *mautrix.DefaultSyncer")
	}

	base := channels.NewBaseChannel(
		bc.Name(),
		cfg,
		messageBus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(65536),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	ch := &MatrixChannel{
		BaseChannel:       base,
		bc:                bc,
		client:            client,
		config:            cfg,
		syncer:            syncer,
		typingSessions:    make(map[string]*typingSession),
		startTime:         time.Now(),
		roomKindCache:     newRoomKindCache(roomKindCacheMaxEntries, roomKindCacheTTL),
		localpartMentionR: localpartMentionRegexp(matrixLocalpart(client.UserID)),
		typingMu:          sync.Mutex{},
		cryptoDbPath:      cryptoDatabasePath,
	}
	return ch, nil
}

func (c *MatrixChannel) Start(ctx context.Context) error {
	logger.InfoC("matrix", "Starting Matrix channel")

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.startTime = time.Now()

	// Initialize crypto helper if database and passphrase are configured
	if c.cryptoDbPath != "" && c.config.CryptoPassphrase != "" {
		if err := c.initCrypto(ctx); err != nil {
			logger.WarnCF(
				"matrix",
				"Failed to initialize crypto, continuing without encryption support",
				map[string]any{
					"error": err.Error(),
				},
			)
		}
	}

	c.syncer.OnEventType(event.EventMessage, c.handleMessageEvent)
	c.syncer.OnEventType(event.EventEncrypted, c.handleMessageEvent)
	c.syncer.OnEventType(event.StateMember, c.handleMemberEvent)

	c.SetRunning(true)
	go c.runRoomKindCacheJanitor(c.ctx)

	go func() {
		if err := c.client.SyncWithContext(c.ctx); err != nil && c.ctx.Err() == nil {
			logger.ErrorCF("matrix", "Matrix sync stopped unexpectedly", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	logger.InfoC("matrix", "Matrix channel started")
	return nil
}

func (c *MatrixChannel) Stop(ctx context.Context) error {
	logger.InfoC("matrix", "Stopping Matrix channel")
	c.SetRunning(false)

	if c.cancel != nil {
		c.cancel()
	}
	c.stopTypingSessions(ctx)
	// Close crypto helper if initialized
	if c.cryptoHelper != nil {
		_ = c.cryptoHelper.Close()
		c.cryptoHelper = nil
		c.client.Crypto = nil
	}

	logger.InfoC("matrix", "Matrix channel stopped")
	return nil
}
