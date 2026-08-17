package datacommons

import (
	"encoding/json"
	"fmt"
	"io"
)

// decodeFeed streams a schema.org DataFeed document, invoking emit for every
// ClaimReview under every DataFeedItem. It decodes the dataFeedElement array one
// item at a time rather than unmarshaling the whole document, so the
// hundreds-of-MB daily feed is processed with bounded memory. Any other top-level
// field is skipped without buffering. emit's error stops the walk and is returned
// verbatim (the caller uses a sentinel to end early on the item cap).
func decodeFeed(r io.Reader, emit func(claimReview) error) error {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("datacommons: read feed key: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("datacommons: unexpected feed key token %v", keyTok)
		}
		if key != "dataFeedElement" {
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return err
		}
		for dec.More() {
			var item feedItem
			if err := dec.Decode(&item); err != nil {
				return fmt.Errorf("datacommons: decode feed item: %w", err)
			}
			for _, cr := range item.Item {
				if err := emit(cr); err != nil {
					return err
				}
			}
		}
		// Consume the closing ']' of dataFeedElement.
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("datacommons: close feed array: %w", err)
		}
	}
	return nil
}

// expectDelim reads the next token and fails unless it is the given delimiter.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("datacommons: read %q: %w", want, err)
	}
	if got, ok := tok.(json.Delim); !ok || got != want {
		return fmt.Errorf("datacommons: expected %q, got %v", want, tok)
	}
	return nil
}

// skipValue decodes and discards the value the decoder is positioned at, so an
// unrelated top-level field (e.g. @context, @type) is stepped over without
// buffering the whole document.
func skipValue(dec *json.Decoder) error {
	var discard json.RawMessage
	if err := dec.Decode(&discard); err != nil {
		return fmt.Errorf("datacommons: skip feed value: %w", err)
	}
	return nil
}
