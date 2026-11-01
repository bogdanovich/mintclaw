package vault

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	onepassword "github.com/1password/onepassword-sdk-go"
)

func TestDiscoverySlowMetadataDoesNotExtendPrivateRetrieval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fullReads := 0
		items := &discoveryItems{
			list: func(ctx context.Context) ([]onepassword.ItemOverview, error) {
				select {
				case <-time.After(11 * time.Second):
					return []onepassword.ItemOverview{discoveryOverview("one", "Example", "example.com")}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
			get: func(ctx context.Context) (onepassword.Item, error) {
				fullReads++
				select {
				case <-time.After(11 * time.Second):
					return onepassword.Item{}, errors.New("private retrieval exceeded its budget")
				case <-ctx.Done():
					return onepassword.Item{}, ctx.Err()
				}
			},
		}
		provider := newProvider(items)
		candidates, err := provider.FindLogins(t.Context(), "vault", DiscoveryQuery{Website: "https://example.com"})
		if err != nil || len(candidates) != 1 || candidates[0].ItemID != "one" || fullReads != 0 {
			t.Fatalf("slow metadata lookup failed or read a full item: candidates=%d reads=%d error=%v",
				len(candidates), fullReads, err)
		}
		started := time.Now()
		_, err = provider.GetLogin(t.Context(), Binding{VaultID: "vault", ItemID: "one"})
		if !errors.Is(err, ErrUnavailable) || fullReads != 1 || time.Since(started) != 10*time.Second {
			t.Fatal("private retrieval inherited the longer metadata budget")
		}
	})
}

func TestDiscoveryCancellationReleasesSharedClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := &discoveryItems{list: func(ctx context.Context) ([]onepassword.ItemOverview, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		provider := newProvider(items)
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			_, err := provider.FindLogins(ctx, "vault", DiscoveryQuery{Name: "Example"})
			result <- err
		}()
		synctest.Wait()
		if items.calls != 1 {
			t.Fatal("discovery did not reach the metadata provider")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal("cancellation lost the fixed private error")
			}
		default:
			t.Fatal("discovery did not honor caller cancellation")
		}
		items.list = nil
		items.items = []onepassword.ItemOverview{discoveryOverview("one", "Example", "example.com")}
		candidates, err := provider.FindLogins(t.Context(), "vault", DiscoveryQuery{Name: "Example"})
		if err != nil || len(candidates) != 1 || items.calls != 2 {
			t.Fatal("canceled discovery retained the shared SDK slot")
		}
	})
}
