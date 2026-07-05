package kraken

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func pairsServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The live AssetPairs response shape (captured 2026-07-05): result keyed by pair name,
// pair_decimals/lot_decimals carrying the wire precisions.
const btcusdPairs = `{"error":[],"result":{"BTC/USD":{"altname":"XBTUSD","pair_decimals":1,"lot_decimals":8}}}`

func TestLiveEmptySymbol(t *testing.T) {
	if _, err := Live(context.Background(), LiveConfig{}); err == nil {
		t.Fatal("empty symbol must error")
	}
}

func TestResolveDefaults(t *testing.T) {
	rest, ws, client := LiveConfig{}.resolve()
	if rest != defaultRESTBase || ws != defaultWSBase || client != http.DefaultClient {
		t.Fatalf("resolve() = %q %q %v, want the documented defaults", rest, ws, client)
	}
}

func TestLiveSurfacesRequestErrors(t *testing.T) {
	// a RESTBase that cannot form a request (control byte in the URL)
	if _, err := Live(context.Background(), LiveConfig{Symbol: "BTC/USD", RESTBase: "http://\x7f"}); err == nil {
		t.Fatal("unbuildable request must error")
	}
	// a server that is already gone (connection refused)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := Live(context.Background(), LiveConfig{Symbol: "BTC/USD", RESTBase: url}); err == nil {
		t.Fatal("a dead AssetPairs endpoint must error")
	}
}

func TestLiveSurfacesPairsStatus(t *testing.T) {
	srv := pairsServer(t, "", http.StatusInternalServerError)
	_, err := Live(context.Background(), LiveConfig{Symbol: "BTC/USD", RESTBase: srv.URL, Client: srv.Client()})
	if !errors.Is(err, ErrPairsStatus) {
		t.Fatalf("err = %v, want ErrPairsStatus", err)
	}
}

func TestLiveSurfacesPairsErrors(t *testing.T) {
	cases := []struct{ name, body string }{
		{"api error", `{"error":["EQuery:Unknown asset pair"],"result":{}}`},
		{"empty result", `{"error":[],"result":{}}`},
		{"two pairs", `{"error":[],"result":{"A":{"pair_decimals":1,"lot_decimals":8},"B":{"pair_decimals":1,"lot_decimals":8}}}`},
		{"not json", `<html>maintenance</html>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := pairsServer(t, c.body, http.StatusOK)
			if _, err := Live(context.Background(), LiveConfig{Symbol: "BTC/USD", RESTBase: srv.URL, Client: srv.Client()}); err == nil {
				t.Fatal("must error, no guessed default scales")
			}
		})
	}
}

// Live wired against local servers (REST for AssetPairs, a real local WebSocket for
// the book) must assemble a source whose checksum seam drives the engine to a bound,
// verified book, recover from drift by redialing for a fresh in-band snapshot, and
// tear the socket down on Close — the whole live path proven without touching Kraken.
func TestLiveEndToEndLocal(t *testing.T) {
	rest := pairsServer(t, btcusdPairs, http.StatusOK)

	gotPath := make(chan string, 1)
	gotSub := make(chan []byte, 1)
	firstClosed := make(chan struct{})
	secondClosed := make(chan struct{})
	bids, asks := goldenBook()
	newBids := append([]market.Level{{Price: 452836, Size: 123456}}, bids...)
	updateChecksum := Checksum(newBids[:10], asks)

	var conns atomic.Int32
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotPath <- r.URL.Path:
		default:
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, sub, err := c.Read(r.Context()) // the subscribe request
		if err != nil {
			return
		}
		switch conns.Add(1) {
		case 1: // first subscription: bind, one clean update, then drift
			select {
			case gotSub <- sub:
			default:
			}
			_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"channel":"status","type":"update","data":[{"system":"online"}]}`))
			_ = c.Write(r.Context(), websocket.MessageText, wireFrame("snapshot", bids, asks, goldenChecksum))
			_ = c.Write(r.Context(), websocket.MessageText, wireFrame("update", []market.Level{{Price: 452836, Size: 123456}}, nil, updateChecksum))
			_ = c.Write(r.Context(), websocket.MessageText, wireFrame("update", []market.Level{{Price: 452830, Size: 1}}, nil, 12345)) // wrong checksum: drift
			_, _, _ = c.Read(r.Context())                                                                                              // returns when the redial closes this socket
			close(firstClosed)
		default: // the drift resync's fresh subscription
			_ = c.Write(r.Context(), websocket.MessageText, wireFrame("snapshot", bids, asks, goldenChecksum))
			_, _, _ = c.Read(r.Context()) // returns when the client disconnects
			close(secondClosed)
		}
	}))
	defer ws.Close()

	src, err := Live(context.Background(), LiveConfig{
		Symbol:   "BTC/USD",
		RESTBase: rest.URL,
		WSBase:   "ws" + strings.TrimPrefix(ws.URL, "http"),
		Client:   rest.Client(),
	})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if p, q := src.Scales(); p != 1 || q != 8 {
		t.Fatalf("Scales() = %d/%d, want the AssetPairs precisions 1/8", p, q)
	}

	eng := book.New(src, 10).WithChecksum(Checksum).WithMaxDepth(BookDepth)
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if ok, err := eng.Step(context.Background()); !ok || err != nil {
		t.Fatalf("clean step: ok=%v err=%v", ok, err)
	}
	if eng.ChecksumMismatches() != 0 || eng.Gaps() != 0 {
		t.Fatalf("mismatches=%d gaps=%d, want 0/0", eng.ChecksumMismatches(), eng.Gaps())
	}
	if v := eng.View(); v.Crosses() || v.Bids[0].Price != 452836 || v.LastSeq != 2 {
		t.Fatalf("view = best bid %+v seq %d, want the streamed bid at synthetic seq 2", v.Bids[0], v.LastSeq)
	}

	// the drift update: mismatch -> resync -> redial -> rebind from the second socket
	if ok, err := eng.Step(context.Background()); !ok || err != nil {
		t.Fatalf("drift step: ok=%v err=%v", ok, err)
	}
	if eng.ChecksumMismatches() != 1 || eng.Resyncs() != 1 {
		t.Fatalf("mismatches=%d resyncs=%d, want 1/1 after the drift", eng.ChecksumMismatches(), eng.Resyncs())
	}
	if v := eng.View(); v.Crosses() || v.Bids[0] != bids[0] {
		t.Fatalf("post-drift view best bid %+v, want the golden book rebound", v.Bids[0])
	}
	select {
	case <-firstClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("the redial did not close the first socket")
	}

	select {
	case p := <-gotPath:
		if p != "/v2" {
			t.Fatalf("WS path = %q, want /v2", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket was never dialed")
	}
	var sub struct {
		Method string `json:"method"`
		Params struct {
			Channel string   `json:"channel"`
			Symbol  []string `json:"symbol"`
			Depth   int      `json:"depth"`
		} `json:"params"`
	}
	select {
	case raw := <-gotSub:
		if err := json.Unmarshal(raw, &sub); err != nil {
			t.Fatalf("subscribe message %q: %v", raw, err)
		}
		if sub.Method != "subscribe" || sub.Params.Channel != "book" || sub.Params.Depth != BookDepth ||
			len(sub.Params.Symbol) != 1 || sub.Params.Symbol[0] != "BTC/USD" {
			t.Fatalf("subscribe = %+v, want book/BTC-USD/depth %d", sub, BookDepth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscribe request never arrived")
	}

	_ = src.Close()
	select {
	case <-secondClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not tear down the WebSocket")
	}
	// a redial after Close must dial nothing and surface an error, not leak a stream
	if _, err := src.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot after Close must fail: nothing may redial a closed source")
	}
}
