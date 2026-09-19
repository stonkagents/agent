// Package: tracker/internal/solana
// Feature: StonkAgents devnet drip
// Purpose: Adapts *Client to services.DevDripChain (status shape only).

package solana

import (
	"context"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// DripChain is *Client as a services.DevDripChain.
type DripChain struct {
	*Client
}

// NewDripChain wraps c for the drip service.
func NewDripChain(c *Client) DripChain { return DripChain{Client: c} }

// GetSignatureStatus maps the RPC status to the service's confirmed/failed pair.
func (d DripChain) GetSignatureStatus(ctx context.Context, signature string) (*services.DevDripSignatureStatus, error) {
	st, err := d.Client.GetSignatureStatus(ctx, signature)
	if err != nil || st == nil {
		return nil, err
	}
	return &services.DevDripSignatureStatus{Confirmed: st.Confirmed(), Failed: st.Failed()}, nil
}

var _ services.DevDripChain = DripChain{}
