package polygon

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

const chainID = 137

const erc20ABI = `[{"constant":false,"inputs":[{"name":"to","type":"address"},{"name":"value","type":"uint256"}],"name":"transfer","outputs":[{"name":"","type":"bool"}],"type":"function"}]`

// Client signs and sends USDC transfers on Polygon for the settlement
// worker. All amounts are integer six-decimal token units (big.Int) — never
// floats. The signer key is the SHOP's operator wallet: it holds USDC +
// MATIC, pays supplier invoices, and receives supplier refunds (we always
// register our own address as refund_address on crypto invoices).
type Client struct {
	eth      *ethclient.Client
	raw      *rpc.Client // raw JSON-RPC for calls ethclient doesn't expose
	token    common.Address
	key      string
	minMatic *big.Int
}

func New(rpcURL, contract, key string, minMaticWei *big.Int) (*Client, error) {
	if rpcURL == "" || contract == "" || key == "" {
		return nil, fmt.Errorf("Polygon USDC signer is not configured")
	}
	if !common.IsHexAddress(contract) {
		return nil, fmt.Errorf("invalid allowlisted USDC contract")
	}
	e, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, err
	}
	raw, err := rpc.DialContext(context.Background(), rpcURL)
	if err != nil {
		e.Close()
		return nil, err
	}
	return &Client{eth: e, raw: raw, token: common.HexToAddress(contract), key: strings.TrimPrefix(key, "0x"), minMatic: minMaticWei}, nil
}

// OperatorAddressFromKey derives the operator wallet address from a hex
// private key without any RPC connection (used to register the supplier
// refund_address at startup).
func OperatorAddressFromKey(keyHex string) string {
	key, err := crypto.HexToECDSA(strings.TrimPrefix(keyHex, "0x"))
	if err != nil {
		return ""
	}
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}

// OperatorAddress returns the operator wallet address derived from the
// configured private key. It is the USDC source AND the supplier refund
// address (we pay the supplier, CryptoRefills; on supplier failure the USDC comes back to us).
func (c *Client) OperatorAddress() common.Address {
	key, err := crypto.HexToECDSA(c.key)
	if err != nil {
		return common.Address{}
	}
	return crypto.PubkeyToAddress(key.PublicKey)
}

// TransferUSDCWithNonce broadcasts a USDC transfer using EXACTLY the provided
// nonce. The caller must have persisted that nonce BEFORE calling (write
// ahead): crash recovery then looks up the nonce on-chain, so a lost response
// can never lead to a second payment.
func (c *Client) TransferUSDCWithNonce(ctx context.Context, to string, units *big.Int, nonce uint64) (string, error) {
	if !common.IsHexAddress(to) || units == nil || units.Sign() <= 0 {
		return "", fmt.Errorf("invalid USDC transfer")
	}
	key, err := crypto.HexToECDSA(c.key)
	if err != nil {
		return "", err
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	balance, err := c.eth.BalanceAt(ctx, from, nil)
	if err != nil {
		return "", err
	}
	if balance.Cmp(c.minMatic) < 0 {
		return "", fmt.Errorf("MATIC gas reserve below configured minimum")
	}
	a, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		return "", err
	}
	data, err := a.Pack("transfer", common.HexToAddress(to), units)
	if err != nil {
		return "", err
	}
	msg := ethereum.CallMsg{From: from, To: &c.token, Data: data}
	gas, err := c.eth.EstimateGas(ctx, msg)
	if err != nil {
		return "", err
	}
	fee, err := c.eth.SuggestGasPrice(ctx)
	if err != nil {
		return "", err
	}
	tx := types.NewTransaction(nonce, c.token, big.NewInt(0), gas, fee, data)
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainID)), key)
	if err != nil {
		return "", err
	}
	if err := c.eth.SendTransaction(ctx, signed); err != nil {
		return "", err
	}
	return signed.Hash().Hex(), nil
}

// TransferUSDC accepts only integer six-decimal token units. It does not use
// float values; invoice parsers must convert/validate before calling it.
// NOTE: prefer TransferUSDCWithNonce with a DB-reserved nonce (crash safety);
// this convenience variant reads the nonce without a persistence hook.
func (c *Client) TransferUSDC(ctx context.Context, to string, units *big.Int) (string, error) {
	nonce, err := c.eth.PendingNonceAt(ctx, c.OperatorAddress())
	if err != nil {
		return "", err
	}
	return c.TransferUSDCWithNonce(ctx, to, units, nonce)
}

// TxByNonce returns the hash of the transaction that used this nonce for
// this address, if one exists (pending or confirmed). This is the
// crash-recovery primitive: after a restart we can prove whether a lost
// broadcast actually landed, and re-broadcast only when it did not.
func (c *Client) TxByNonce(ctx context.Context, addr common.Address, nonce uint64) (string, bool, error) {
	var result *hexutil.Bytes
	err := c.raw.CallContext(ctx, &result, "eth_getTransactionByNonce", addr, hexutil.EncodeUint64(nonce))
	if err != nil {
		return "", false, fmt.Errorf("eth_getTransactionByNonce: %w", err)
	}
	if result == nil {
		return "", false, nil
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(*result); err != nil {
		return "", false, fmt.Errorf("decode tx at nonce %d: %w", nonce, err)
	}
	return tx.Hash().Hex(), true, nil
}

// PendingNonce returns the next usable nonce for the address.
func (c *Client) PendingNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return c.eth.PendingNonceAt(ctx, addr)
}

// Receipt reports whether the mined transaction succeeded.
func (c *Client) Receipt(ctx context.Context, hash string) (bool, error) {
	r, err := c.eth.TransactionReceipt(ctx, common.HexToHash(hash))
	if err != nil {
		return false, err
	}
	return r.Status == types.ReceiptStatusSuccessful, nil
}
