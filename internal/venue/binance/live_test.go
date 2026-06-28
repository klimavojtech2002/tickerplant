package binance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func TestScaleOf(t *testing.T) {
	cases := map[string]int{
		"0.01000000":   2,
		"0.00001000":   5,
		"1.00000000":   0,
		"100.00000000": 0,
		"0.5":          1,
		"0.00000001":   8,
		"5":            0, // no decimal point at all
	}
	for in, want := range cases {
		got, err := scaleOf(in)
		if err != nil || got != want {
			t.Errorf("scaleOf(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestScaleOfMalformed(t *testing.T) {
	for _, in := range []string{"0.0x1", ""} {
		if _, err := scaleOf(in); !errors.Is(err, market.ErrMalformed) {
			t.Fatalf("scaleOf(%q) err = %v, want ErrMalformed", in, err)
		}
	}
}

func exchangeInfoServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "err", status)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchScales(t *testing.T) {
	srv := exchangeInfoServer(t, `{"symbols":[{"symbol":"BTCUSDT","filters":[
		{"filterType":"PRICE_FILTER","tickSize":"0.01000000"},
		{"filterType":"LOT_SIZE","stepSize":"0.00001000"}]}]}`, http.StatusOK)
	p, s, err := fetchScales(context.Background(), srv.Client(), srv.URL, "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if p != 2 || s != 5 {
		t.Fatalf("scales = price %d, size %d; want 2, 5", p, s)
	}
}

func TestFetchScalesSymbolNotFound(t *testing.T) {
	srv := exchangeInfoServer(t, `{"symbols":[]}`, http.StatusOK)
	if _, _, err := fetchScales(context.Background(), srv.Client(), srv.URL, "NOPE"); !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestFetchScalesMissingFilter(t *testing.T) {
	// only a PRICE_FILTER, no LOT_SIZE -> must error, not silently default a scale
	srv := exchangeInfoServer(t, `{"symbols":[{"symbol":"BTCUSDT","filters":[
		{"filterType":"PRICE_FILTER","tickSize":"0.01000000"}]}]}`, http.StatusOK)
	if _, _, err := fetchScales(context.Background(), srv.Client(), srv.URL, "BTCUSDT"); !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed (missing LOT_SIZE)", err)
	}
}

func TestFetchScalesNon200(t *testing.T) {
	srv := exchangeInfoServer(t, "", http.StatusTooManyRequests)
	if _, _, err := fetchScales(context.Background(), srv.Client(), srv.URL, "BTCUSDT"); !errors.Is(err, ErrSnapshotStatus) {
		t.Fatalf("err = %v, want ErrSnapshotStatus", err)
	}
}

func TestFetchScalesBadJSON(t *testing.T) {
	srv := exchangeInfoServer(t, `{not json`, http.StatusOK)
	if _, _, err := fetchScales(context.Background(), srv.Client(), srv.URL, "BTCUSDT"); err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestFetchScalesMalformedIncrement(t *testing.T) {
	for _, body := range []string{
		`{"symbols":[{"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0x"},{"filterType":"LOT_SIZE","stepSize":"0.001"}]}]}`,
		`{"symbols":[{"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","stepSize":"0.0y"}]}]}`,
	} {
		srv := exchangeInfoServer(t, body, http.StatusOK)
		if _, _, err := fetchScales(context.Background(), srv.Client(), srv.URL, "BTCUSDT"); !errors.Is(err, market.ErrMalformed) {
			t.Fatalf("a malformed increment must error, got %v", err)
		}
	}
}

func TestLiveRejectsEmptySymbol(t *testing.T) {
	if _, err := Live(context.Background(), LiveConfig{}); err == nil {
		t.Fatal("empty symbol must error")
	}
}

func TestLiveConfigResolve(t *testing.T) {
	if r, w, c := (LiveConfig{}).resolve(); r != defaultRESTBase || w != defaultWSBase || c != http.DefaultClient {
		t.Fatalf("empty config must default, got %q %q %v", r, w, c)
	}
	cl := &http.Client{}
	if r, w, c := (LiveConfig{RESTBase: "x", WSBase: "y", Client: cl}).resolve(); r != "x" || w != "y" || c != cl {
		t.Fatal("set values must pass through unchanged")
	}
}

func TestFetchScalesRequestError(t *testing.T) {
	if _, _, err := fetchScales(context.Background(), http.DefaultClient, "://bad", "BTCUSDT"); err == nil {
		t.Fatal("a malformed REST base must error")
	}
}

func TestFetchScalesConnError(t *testing.T) {
	srv := exchangeInfoServer(t, "{}", http.StatusOK)
	url := srv.URL
	srv.Close() // dead before the call
	if _, _, err := fetchScales(context.Background(), http.DefaultClient, url, "BTCUSDT"); err == nil {
		t.Fatal("a dead endpoint must error")
	}
}

func TestLiveSurfacesFetchError(t *testing.T) {
	srv := exchangeInfoServer(t, "", http.StatusTooManyRequests)
	_, err := Live(context.Background(), LiveConfig{Symbol: "BTCUSDT", RESTBase: srv.URL, Client: srv.Client()})
	if !errors.Is(err, ErrSnapshotStatus) {
		t.Fatalf("Live must surface the fetchScales error, got %v", err)
	}
}

// Live wired against local servers (REST for exchangeInfo + depth snapshot, a real
// local WebSocket for the diff stream) must assemble a working source that drives the
// engine to an uncrossed book — the whole live path proven without touching Binance.
func TestLiveEndToEndLocal(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "exchangeInfo"):
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","filters":[` +
				`{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","stepSize":"0.001"}]}]}`))
		default: // /api/v3/depth
			_, _ = w.Write([]byte(`{"lastUpdateId":100,"bids":[["100.00","1.000"]],"asks":[["101.00","1.000"]]}`))
		}
	}))
	defer rest.Close()

	gotPath := make(chan string, 1)
	disconnected := make(chan struct{})
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
		_ = c.Write(r.Context(), websocket.MessageText, depthFrame(101, 101, [][]string{{"100.00", "2.000"}}, nil))
		_ = c.Write(r.Context(), websocket.MessageText, depthFrame(102, 102, nil, [][]string{{"102.00", "3.000"}}))
		_, _, _ = c.Read(r.Context()) // returns when the client disconnects
		close(disconnected)
	}))
	defer ws.Close()

	src, err := Live(context.Background(), LiveConfig{
		Symbol:   "BTCUSDT",
		RESTBase: rest.URL,
		WSBase:   "ws" + strings.TrimPrefix(ws.URL, "http"),
		Client:   rest.Client(),
	})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}

	eng := book.New(src, 10)
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for range 2 { // apply the two streamed updates
		if ok, err := eng.Step(context.Background()); err != nil || !ok {
			t.Fatalf("step: ok=%v err=%v", ok, err)
		}
	}
	v := eng.View()
	if v.Crosses() {
		t.Fatal("reconstructed live book crossed")
	}
	// price scale 2, size scale 3: "100.00"->10000, "2.000"->2000
	if v.Bids[0].Price != 10000 || v.Bids[0].Size != 2000 || v.LastSeq != 102 {
		t.Fatalf("best bid = %+v at seq %d, want {10000,2000} at 102", v.Bids[0], v.LastSeq)
	}

	// the dialed stream must be the lowercase raw diff-depth path
	select {
	case p := <-gotPath:
		if p != "/ws/btcusdt@depth" {
			t.Fatalf("WS path = %q, want /ws/btcusdt@depth (lowercase, raw)", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket was never dialed")
	}

	// Close must tear down the live socket (the server's read returns on disconnect)
	_ = src.Close()
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not tear down the WebSocket")
	}
}
