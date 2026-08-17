package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func telegramMediaGroupDelay(telegramCfg *config.TelegramSettings) time.Duration {
	if telegramCfg != nil && telegramCfg.MediaGroupDelayMS > 0 {
		return time.Duration(telegramCfg.MediaGroupDelayMS) * time.Millisecond
	}
	return defaultMediaGroupDelay
}

func (c *TelegramChannel) topicAllowed(topicID int) bool {
	if topicID == 0 || c == nil || c.tgCfg == nil {
		return true
	}
	topic := strconv.Itoa(topicID)
	for _, ignored := range c.tgCfg.IgnoredTopicIDs {
		if strings.TrimSpace(ignored) == topic {
			return false
		}
	}
	if len(c.tgCfg.AllowedTopicIDs) == 0 {
		return true
	}
	for _, allowed := range c.tgCfg.AllowedTopicIDs {
		if strings.TrimSpace(allowed) == topic {
			return true
		}
	}
	return false
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (polling mode)...")

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.refreshOwnBotIdentity(c.ctx)

	updates, err := c.bot.UpdatesViaLongPolling(c.ctx, &telego.GetUpdatesParams{
		Timeout: 30,
	})
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to start long polling: %w", err)
	}

	bh, err := th.NewBotHandler(c.bot, updates)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to create bot handler: %w", err)
	}
	c.bh = bh

	bh.HandleMessage(func(ctx *th.Context, message telego.Message) error {
		return c.handleMessage(ctx, &message)
	}, th.AnyMessage())
	bh.HandleCallbackQuery(func(ctx *th.Context, query telego.CallbackQuery) error {
		return c.handleInteractionCallback(ctx, query)
	})

	c.SetRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]any{
		"username": c.ownBotUsername(),
	})

	c.startCommandRegistration(c.ctx, commands.BuiltinDefinitions())

	handlerRunID := c.handlerRun.Add(1)
	runCtx := c.ctx
	go c.runBotHandler(runCtx, handlerRunID, func() error {
		return runTelegramUpdatesOrdered(runCtx, updates, func(ctx context.Context, update telego.Update) error {
			return bh.BaseGroup().HandleUpdate(ctx, c.bot, update)
		})
	})

	return nil
}

func (c *TelegramChannel) runBotHandler(
	runCtx context.Context,
	runID uint64,
	startBotHandler func() error,
) {
	err := startBotHandler()
	if runCtx.Err() != nil || c.handlerRun.Load() != runID || !c.IsRunning() {
		return
	}

	c.SetRunning(false)
	c.cleanupBackgroundWork(context.Background())
	if err != nil {
		logger.ErrorCF("telegram", "Bot handler failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	logger.WarnC("telegram", "Bot handler exited unexpectedly")
}

func (c *TelegramChannel) startBotHandler() error {
	if c.startBotHandlerFn != nil {
		return c.startBotHandlerFn()
	}
	return c.bh.Start()
}

func (c *TelegramChannel) Stop(ctx context.Context) error {
	logger.InfoC("telegram", "Stopping Telegram bot...")
	c.SetRunning(false)

	// Stop the bot handler
	if c.bh != nil {
		_ = c.bh.StopWithContext(ctx)
	}
	c.cleanupBackgroundWork(ctx)

	return nil
}

func (c *TelegramChannel) cleanupBackgroundWork(ctx context.Context) {
	c.flushPendingMediaGroups(ctx)

	// Cancel our context (stops long polling)
	if c.cancel != nil {
		c.cancel()
	}
	if c.commandRegCancel != nil {
		c.commandRegCancel()
	}
}
