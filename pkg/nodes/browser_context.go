package nodes

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
)

const browserContextAuthorityDigestDomain = "mintclaw.browser.context.authority.v1\x00"

func BrowserContextAuthorityDigest(catalog BrowserContextCatalog) (string, error) {
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(browserContextAuthorityDigestDomain))
	_, _ = hash.Write(encoded)
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func BrowserContextAuthorityDigestMatches(digest string, catalog BrowserContextCatalog) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	expected, err := BrowserContextAuthorityDigest(catalog)
	return err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(digest)) == 1
}

func (catalog *BrowserContextCatalog) UnmarshalJSON(data []byte) error {
	type plain BrowserContextCatalog
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.OmittedTabCount < 0 {
		return fmt.Errorf("%w: malformed omitted browser tab count", ErrInvalidCapability)
	}
	for _, tab := range value.Tabs {
		if tab.OmittedFrameCount < 0 {
			return fmt.Errorf("%w: malformed omitted browser frame count", ErrInvalidCapability)
		}
		for _, frame := range tab.Frames {
			if frame.Depth < 1 || frame.Depth > 8 {
				return fmt.Errorf("%w: malformed browser frame depth", ErrInvalidCapability)
			}
		}
	}
	*catalog = BrowserContextCatalog(value)
	return nil
}

func (input *BrowserContextInput) UnmarshalJSON(data []byte) error {
	type plain BrowserContextInput
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.AuthorityBytes < 0 || value.AuthorityBytes > MaxBrowserContextInputBytes {
		return fmt.Errorf("%w: malformed browser context authority bytes", ErrInvalidCapability)
	}
	*input = BrowserContextInput(value)
	return nil
}
