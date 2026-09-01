package telegram

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

func TestRunTelegramUpdatesOrderedSerializesConversationAndParallelizesChats(t *testing.T) {
	updates := make(chan telego.Update, 3)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	otherChatStarted := make(chan struct{})

	var mu sync.Mutex
	handled := make([]int, 0, 3)
	handle := func(_ context.Context, update telego.Update) error {
		if update.UpdateID == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		if update.UpdateID == 3 {
			close(otherChatStarted)
		}
		mu.Lock()
		handled = append(handled, update.UpdateID)
		mu.Unlock()
		return nil
	}

	updates <- telegramMessageUpdate(1, 100, 0)
	updates <- telegramMessageUpdate(2, 100, 0)
	updates <- telegramMessageUpdate(3, 200, 0)
	close(updates)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runTelegramUpdatesOrdered(context.Background(), updates, handle)
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first update did not start")
	}
	select {
	case <-otherChatStarted:
	case <-time.After(time.Second):
		t.Fatal("different chat should run while the first chat is blocked")
	}

	mu.Lock()
	for _, updateID := range handled {
		if updateID == 2 {
			mu.Unlock()
			t.Fatal("second same-chat update ran before the first completed")
		}
	}
	mu.Unlock()

	close(releaseFirst)
	if err := <-errCh; err != nil {
		t.Fatalf("runTelegramUpdatesOrdered() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	firstIndex := indexOfTelegramUpdate(handled, 1)
	secondIndex := indexOfTelegramUpdate(handled, 2)
	if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
		t.Fatalf("same-chat completion order = %v, want update 1 before update 2", handled)
	}
}

func TestTelegramUpdateConversationKeySeparatesTopics(t *testing.T) {
	first := telegramUpdateConversationKey(telegramMessageUpdate(1, 100, 10))
	second := telegramUpdateConversationKey(telegramMessageUpdate(2, 100, 20))
	if first == second {
		t.Fatalf("topic keys are equal: %q", first)
	}
}

func TestRunTelegramUpdatesOrderedContinuesAfterBoundedMediaLookup(t *testing.T) {
	releaseCaller := make(chan struct{})
	caller := &stubCaller{callFn: func(
		_ context.Context,
		_ string,
		_ *ta.RequestData,
	) (*ta.Response, error) {
		<-releaseCaller
		return nil, errors.New("released stalled caller")
	}}
	ch := newTestChannel(t, caller)
	updates := make(chan telego.Update, 2)
	secondHandled := make(chan struct{})
	handle := func(ctx context.Context, update telego.Update) error {
		if update.UpdateID == 1 {
			lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			_, err := ch.getFileAttempt(lookupCtx, "stalled-photo")
			if !errors.Is(err, context.DeadlineExceeded) {
				return errors.New("stalled media lookup did not reach its deadline")
			}
			return nil
		}
		close(secondHandled)
		return nil
	}

	updates <- telegramMessageUpdate(1, 100, 0)
	updates <- telegramMessageUpdate(2, 100, 0)
	close(updates)
	errCh := make(chan error, 1)
	go func() {
		errCh <- runTelegramUpdatesOrdered(t.Context(), updates, handle)
	}()

	select {
	case <-secondHandled:
	case <-time.After(time.Second):
		t.Fatal("text update stayed blocked behind the bounded media lookup")
	}
	close(releaseCaller)
	if err := <-errCh; err != nil {
		t.Fatalf("runTelegramUpdatesOrdered() error = %v", err)
	}
}

func TestTelegramUpdateConversationKeySerializesCallbackWithConversation(t *testing.T) {
	message := telegramMessageUpdate(1, 100, 10)
	callback := telego.Update{UpdateID: 2, CallbackQuery: &telego.CallbackQuery{
		Message: &telego.Message{Chat: telego.Chat{ID: 100}, MessageThreadID: 10},
	}}
	if got, want := telegramUpdateConversationKey(callback), telegramUpdateConversationKey(message); got != want {
		t.Fatalf("callback conversation key = %q, want %q", got, want)
	}
}

func telegramMessageUpdate(updateID int, chatID int64, topicID int) telego.Update {
	return telego.Update{
		UpdateID: updateID,
		Message: &telego.Message{
			MessageID:       updateID,
			MessageThreadID: topicID,
			Chat:            telego.Chat{ID: chatID},
		},
	}
}

func indexOfTelegramUpdate(updates []int, target int) int {
	for i, updateID := range updates {
		if updateID == target {
			return i
		}
	}
	return -1
}
