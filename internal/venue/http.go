package venue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// DefaultHTTPTimeout bounds a whole REST request (connect, headers and body). A venue
// answers a depth snapshot in well under a second, so this only ever limits a stall.
const DefaultHTTPTimeout = 15 * time.Second

// ErrBodyTooLarge is returned when a reply is bigger than the bound the caller set.
var ErrBodyTooLarge = errors.New("venue: response body too large")

// NewHTTPClient returns a client whose requests end after timeout. http.DefaultClient has
// no timeout, so a venue that accepts the connection and then goes silent would hold the
// caller for ever.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// Get fetches url and returns the status and, for a 200 reply, the body read through a
// limit. A transport failure, a failure while reading the body, and a body over limit are
// marked transient (see source.Transient), except when ctx itself is done: the caller's
// own cancellation or deadline is never retried. A non-200 reply is not an error here; the
// caller maps the status with StatusErr.
func Get(ctx context.Context, client *http.Client, url string, limit int64) (body []byte, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, classify(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	body, err = ReadLimited(resp.Body, limit)
	if err != nil {
		return nil, resp.StatusCode, classify(ctx, err)
	}
	return body, resp.StatusCode, nil
}

// ReadLimited reads all of r but fails with ErrBodyTooLarge if it holds more than limit
// bytes. It reads one byte past the limit to tell "exactly limit" from "more".
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w (more than %d bytes)", ErrBodyTooLarge, limit)
	}
	return b, nil
}

// StatusErr wraps sentinel with the status, and marks it transient for 429 (rate limited)
// and 5xx (venue trouble). Any other status, such as a 400 for a bad symbol, stays
// permanent so a wrong setting fails at once instead of after the retries.
func StatusErr(sentinel error, status int) error {
	err := fmt.Errorf("%w: %d", sentinel, status)
	if status == http.StatusTooManyRequests || status >= 500 {
		return source.Transient(err)
	}
	return err
}

func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return source.Transient(err)
}
