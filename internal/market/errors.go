package market

import "errors"

// Shared domain sentinels. Engine and adapters wrap these with %w and callers
// match them with errors.Is, so the recovery control flow keys on values, not on
// ad-hoc strings.
var (
	// ErrCrossed: the book would have best bid >= best ask after an update.
	ErrCrossed = errors.New("book crossed")
	// ErrSequenceGap: a delta does not continue from the last applied sequence.
	ErrSequenceGap = errors.New("sequence gap")
	// ErrChecksumMismatch: a venue checksum disagrees with the local book.
	ErrChecksumMismatch = errors.New("checksum mismatch")
	// ErrStaleSnapshot: the snapshot is older than the available stream position.
	ErrStaleSnapshot = errors.New("stale snapshot")
	// ErrMalformed: an input could not be parsed into the canonical model.
	ErrMalformed = errors.New("malformed input")
	// ErrNotBootstrapped: Step was called before Bootstrap bound a book.
	ErrNotBootstrapped = errors.New("engine not bootstrapped")
)
