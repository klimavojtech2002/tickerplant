package market_test

import (
	"fmt"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// A wire decimal string parses to an exact scaled integer and formats back to its
// canonical form, with no floating point on the path.
func ExampleParseScaled() {
	v, _ := market.ParseScaled("123.45", 2)
	fmt.Println(v)
	fmt.Println(market.FormatScaled(v, 2))
	// Output:
	// 12345
	// 123.45
}
