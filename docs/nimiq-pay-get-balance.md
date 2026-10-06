# Nimiq Mini App `getBalance(address)` reference

`getBalance(address)` reads the on-chain balance of any valid Nimiq address through the Nimiq Pay provider on its active network (mainnet or testnet). The address may belong to someone else. The lookup does not require account approval, an ownership check, a transaction listener, or a prior `listAccounts()` call.

## Availability

Use `@nimiq/mini-app-sdk` 0.2.1 or later and a Nimiq Pay host that exposes balance lookups. Updating the SDK alone does not add this capability to an older host. After `init()`, check the provider before calling the method and ask the user to update Nimiq Pay if it is unavailable:

```ts
import { init } from '@nimiq/mini-app-sdk'

const nimiq = await init()

if (typeof nimiq.getBalance !== 'function') {
  console.info('Update Nimiq Pay to look up balances.')
}
```

## Parameters and result

- **`address`** (`string`, required): a valid Nimiq address. Missing, non-string, or malformed addresses are rejected.
- **Result**: `Promise<number>` through the provider returned by `init()`. The number is in luna: **100,000 luna = 1 NIM**.

A result of `0` means the lookup succeeded and the queried address has zero balance. A failed lookup rejects; it does not return zero. This is the balance of the supplied address, not a wallet-wide total.

## Network and provider behavior

The lookup uses the host's active network (mainnet or testnet). It uses the native bridge, needs no external RPC endpoint, and is unaffected by `setRPCUrl()`.

On a supported host, the generic request form performs the same lookup:

```ts
const balanceLuna = await nimiq.request({
  method: 'getBalance',
  params: { address: 'NQ07 0000 0000 0000 0000 0000 0000 0000 0000' },
})
```

The direct provider method is:

```ts
const balanceLuna = await nimiq.getBalance(
  'NQ07 0000 0000 0000 0000 0000 0000 0000 0000',
)
const balanceNim = balanceLuna / 100_000
```

Replace the example with the address to query. Both forms use Nimiq Pay's native bridge.

## Errors

The SDK provider rejects with `NimiqProviderError`. Use `NimiqProviderError.is(error)` to recognize it, then inspect `type`, `message`, and `code`:

| Type | Code | Cause |
| --- | ---: | --- |
| `INVALID_REQUEST` | `-32602` | The address is missing or invalid. |
| `NETWORK_ERROR` | `-32000` | The client is unavailable, consensus is not established, or the lookup fails or exceeds the 30-second timeout. |

```ts
import { init, NimiqProviderError } from '@nimiq/mini-app-sdk'

const nimiq = await init()

try {
  if (typeof nimiq.getBalance !== 'function') {
    console.info('Update Nimiq Pay to look up balances.')
  } else {
    const balanceLuna = await nimiq.getBalance('NQ07 0000 0000 0000 0000 0000 0000 0000 0000')
    console.log({ balanceLuna, balanceNim: balanceLuna / 100_000 })
  }
} catch (error) {
  if (NimiqProviderError.is(error)) {
    console.error(error.type, error.message, error.code)
  } else {
    throw error
  }
}
```

## Contract balances

A contract balance is not necessarily spendable. HTLC and vesting funds are held at their contract address, which may differ from the user's wallet address, and the contract's spending conditions still apply. Querying the wallet address can therefore correctly return `0` while funds exist at a separate contract address. Query the contract address itself to inspect that address's balance; do not treat the result as proof that the funds can be spent or recovered immediately.
