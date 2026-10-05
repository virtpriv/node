package app

import (
	"fmt"
	"sort"

	"github.com/virtpriv/node/internal/lndrpc"
)

// CoinSelection retains outpoint identity even when a refresh removes a coin.
// Missing coins must be reviewed or cleared, never replaced by automatic selection.
type CoinSelection struct {
	outpoints map[string]bool
}

func coinOutpoint(coin lndrpc.UTXO) string {
	return fmt.Sprintf("%s:%d", coin.Txid, coin.Vout)
}

func (s *CoinSelection) Toggle(coin lndrpc.UTXO) {
	op := coinOutpoint(coin)
	if s.outpoints[op] {
		delete(s.outpoints, op)
		return
	}
	if s.outpoints == nil {
		s.outpoints = make(map[string]bool)
	}
	s.outpoints[op] = true
}

func (s CoinSelection) Contains(coin lndrpc.UTXO) bool {
	return s.outpoints[coinOutpoint(coin)]
}

func (s CoinSelection) Outpoints() []string {
	var outpoints []string
	for op := range s.outpoints {
		outpoints = append(outpoints, op)
	}
	sort.Strings(outpoints)
	return outpoints
}

func (s CoinSelection) Len() int { return len(s.outpoints) }

func (s *CoinSelection) Clear() { s.outpoints = nil }

func (s CoinSelection) Total(available []lndrpc.UTXO) (int64, error) {
	coins, err := resolveCoins(s.Outpoints(), available)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, coin := range coins {
		total += coin.AmountSats
	}
	return total, nil
}

func resolveCoins(outpoints []string, available []lndrpc.UTXO) ([]lndrpc.UTXO, error) {
	byOutpoint := make(map[string]lndrpc.UTXO, len(available))
	for _, coin := range available {
		byOutpoint[coinOutpoint(coin)] = coin
	}
	coins := make([]lndrpc.UTXO, 0, len(outpoints))
	seen := make(map[string]bool, len(outpoints))
	for _, op := range outpoints {
		if seen[op] {
			return nil, fmt.Errorf("Duplicate selected input: %s", op)
		}
		coin, ok := byOutpoint[op]
		if !ok {
			return nil, fmt.Errorf("Selected input is unavailable: %s. Clear the selection and review again", op)
		}
		seen[op] = true
		coins = append(coins, coin)
	}
	return coins, nil
}
