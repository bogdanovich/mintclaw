package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

var (
	reHeading  = regexp.MustCompile(`(?m)^#{1,6}\s+([^\n]+)`)
	reBoldStar = regexp.MustCompile(`\*\*(.+?)\*\*`)
)

const (
	defaultMediaGroupDelay                  = 500 * time.Millisecond
	telegramFileMetadataFirstAttemptTimeout = 30 * time.Second
	telegramFileMetadataRetryTimeout        = 20 * time.Second
	telegramFileMetadataTotalTimeout        = 50 * time.Second
	telegramFileMetadataMaxAttempts         = 2
	telegramFileMetadataRetryDelay          = 250 * time.Millisecond
	telegramCaptionLimit                    = 1024
	telegramTextLimit                       = 4096
)

var errTelegramMessageTooLong = errors.New("telegram message too long")

type TelegramChannel struct {
	*channels.BaseChannel
	bot       *telego.Bot
	bh        *th.BotHandler
	bc        *config.Channel
	chatIDsMu sync.Mutex
	chatIDs   map[string]int64
	selfMu    sync.RWMutex
	selfID    int64
	selfName  string
	ctx       context.Context
	cancel    context.CancelFunc
	tgCfg     *config.TelegramSettings

	registerFunc      func(context.Context, []commands.Definition) error
	commandRegDelayFn func(int) time.Duration
	commandRegCancel  context.CancelFunc
	startBotHandlerFn func() error
	handlerRun        atomic.Uint64

	mediaGroupMu    sync.Mutex
	mediaGroups     map[string]*telegramMediaGroup
	mediaGroupDelay time.Duration
}

type telegramMediaGroup struct {
	messages   []*telego.Message
	timer      *time.Timer
	generation uint64
}

type telegramMessageParts struct {
	content    []string
	mediaPaths []string
}

func NewTelegramChannel(
	bc *config.Channel,
	telegramCfg *config.TelegramSettings,
	bus *bus.MessageBus,
) (*TelegramChannel, error) {
	channelName := bc.Name()
	var opts []telego.BotOption

	if telegramCfg.Proxy != "" {
		proxyURL, parseErr := url.Parse(telegramCfg.Proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", telegramCfg.Proxy, parseErr)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	} else if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		// Use environment proxy if configured
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}))
	}

	if baseURL := strings.TrimRight(strings.TrimSpace(telegramCfg.BaseURL), "/"); baseURL != "" {
		opts = append(opts, telego.WithAPIServer(baseURL))
	}
	opts = append(opts, telego.WithLogger(logger.NewLogger("telego")))

	bot, err := telego.NewBot(telegramCfg.Token.String(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	base := channels.NewBaseChannel(
		channelName,
		telegramCfg,
		bus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(4000),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	ch := &TelegramChannel{
		BaseChannel: base,
		bot:         bot,
		bc:          bc,
		chatIDs:     make(map[string]int64),
		tgCfg:       telegramCfg,

		mediaGroups:     make(map[string]*telegramMediaGroup),
		mediaGroupDelay: telegramMediaGroupDelay(telegramCfg),
	}
	return ch, nil
}
