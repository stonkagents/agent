package services

import (
	"context"
	"log/slog"
	"testing"
)

type noPriceSource struct{}

func (noPriceSource) GetUsdPrice(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}

func TestUsdPriceFallsBackOnDevnetOnly(t *testing.T) {
	devnet := &HTTPRaydiumClient{programID: LaunchLabProgramDevnet, prices: noPriceSource{}, logger: slog.Default()}
	if price, ok := devnet.usdPrice(context.Background(), "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT"); !ok || price != DevnetQuoteUSDFallback {
		t.Fatalf("devnet: want fallback %v, got %v ok=%v", DevnetQuoteUSDFallback, price, ok)
	}
	mainnet := &HTTPRaydiumClient{programID: LaunchLabProgramMainnet, prices: noPriceSource{}, logger: slog.Default()}
	if _, ok := mainnet.usdPrice(context.Background(), "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT"); ok {
		t.Fatal("mainnet: an unpriced quote must stay unpriced")
	}
}
