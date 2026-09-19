-- Guest API keys: map Agent guest keys (installer/onboard) to accounts.
-- Enables unified credit ledger: guest keys use same credit_balances as F-013 accounts.
CREATE TABLE guest_api_keys (
    api_key   VARCHAR(64) PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_guest_api_keys_account_id ON guest_api_keys(account_id);
